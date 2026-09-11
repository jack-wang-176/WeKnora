package web_search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	websearchv1 "github.com/Tencent/WeKnora/docreader/proto/plugin/websearch"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PluginSearchPath is the route a remote-http websearch plugin serves.
const PluginSearchPath = "/search"

// pluginCallTimeout caps a plugin call when the caller brought no deadline.
const pluginCallTimeout = 30 * time.Second

// PluginChannel is the slice of extension.Channel this provider needs. Close()
// is left out on purpose: the host owns the connection, this type borrows it.
// extension.Channel satisfies it structurally, so this package still does not
// import internal/extension.
type PluginChannel interface {
	Conn() any
	Reconnect(ctx context.Context) error
}

// pluginHTTPCarrier is the remote-http carrier shape, same one the docparser
// HTTP reader asserts.
type pluginHTTPCarrier interface {
	Do(req *http.Request) (*http.Response, error)
	BaseURL() string
}

// PluginProvider adapts one websearch plugin to interfaces.WebSearchProvider.
//
// It keeps no stub: the stub is a free derivation of the channel's connection,
// so deriving it per call is what makes "reconnected, still talking on the old
// connection" impossible rather than merely unlikely.
type PluginProvider struct {
	id     string
	ch     PluginChannel
	params types.WebSearchProviderParameters
}

var _ interfaces.WebSearchProvider = (*PluginProvider)(nil)

// NewPluginProvider returns a provider backed by ch. id is the scoped plugin
// id, which is also the provider type stored in the tenant's configuration.
func NewPluginProvider(id string, ch PluginChannel, params types.WebSearchProviderParameters) *PluginProvider {
	return &PluginProvider{id: id, ch: ch, params: params}
}

func (p *PluginProvider) Name() string { return p.id }

// callParams carries the non-secret half of the provider configuration. The API
// key and the proxy URL stay out: secrets reach a plugin once, as install-time
// envs, not once per request.
func (p *PluginProvider) callParams() map[string]string {
	out := make(map[string]string, len(p.params.ExtraConfig)+2)
	for k, v := range p.params.ExtraConfig {
		out[k] = v
	}
	if p.params.BaseURL != "" {
		out["base_url"] = p.params.BaseURL
	}
	if p.params.EngineID != "" {
		out["engine_id"] = p.params.EngineID
	}
	return out
}

func (p *PluginProvider) Search(
	ctx context.Context, query string, maxResults int, includeDate bool,
) ([]*types.WebSearchResult, error) {
	if p.ch == nil {
		return nil, fmt.Errorf("web search plugin %s is not connected", p.id)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, pluginCallTimeout)
		defer cancel()
	}
	switch conn := p.ch.Conn().(type) {
	case *grpc.ClientConn:
		return p.searchGRPC(ctx, conn, query, maxResults, includeDate)
	case pluginHTTPCarrier:
		return p.searchHTTP(ctx, conn, query, maxResults, includeDate)
	default:
		return nil, fmt.Errorf("web search plugin %s has no usable transport", p.id)
	}
}

func (p *PluginProvider) searchGRPC(
	ctx context.Context, conn *grpc.ClientConn, query string, maxResults int, includeDate bool,
) ([]*types.WebSearchResult, error) {
	resp, err := websearchv1.NewWebSearchClient(conn).Search(ctx, &websearchv1.SearchRequest{
		Query:       query,
		MaxResults:  int32(maxResults),
		IncludeDate: includeDate,
		Params:      p.callParams(),
	})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return nil, fmt.Errorf("web search plugin %s does not implement Search", p.id)
		}
		return nil, fmt.Errorf("web search plugin %s: %w", p.id, err)
	}
	out := make([]*types.WebSearchResult, 0, len(resp.GetResults()))
	for _, r := range resp.GetResults() {
		item := &types.WebSearchResult{
			Title:   r.GetTitle(),
			URL:     r.GetUrl(),
			Snippet: r.GetSnippet(),
			Content: r.GetContent(),
			Source:  p.sourceOr(r.GetSource()),
		}
		if ts := r.GetPublishedAt(); ts != nil {
			at := ts.AsTime()
			item.PublishedAt = &at
		}
		out = append(out, item)
	}
	return out, nil
}

type pluginSearchRequest struct {
	Query       string            `json:"query"`
	MaxResults  int               `json:"max_results"`
	IncludeDate bool              `json:"include_date"`
	Params      map[string]string `json:"params,omitempty"`
}

type pluginSearchResponse struct {
	Results []struct {
		Title       string     `json:"title"`
		URL         string     `json:"url"`
		Snippet     string     `json:"snippet"`
		Content     string     `json:"content"`
		Source      string     `json:"source"`
		PublishedAt *time.Time `json:"published_at"`
	} `json:"results"`
	Error string `json:"error"`
}

func (p *PluginProvider) searchHTTP(
	ctx context.Context, carrier pluginHTTPCarrier, query string, maxResults int, includeDate bool,
) ([]*types.WebSearchResult, error) {
	body, err := json.Marshal(pluginSearchRequest{
		Query: query, MaxResults: maxResults, IncludeDate: includeDate, Params: p.callParams(),
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		carrier.BaseURL()+PluginSearchPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := carrier.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web search plugin %s: %w", p.id, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web search plugin %s: http %d", p.id, resp.StatusCode)
	}
	var decoded pluginSearchResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("web search plugin %s: %w", p.id, err)
	}
	if decoded.Error != "" {
		return nil, fmt.Errorf("web search plugin %s: %s", p.id, decoded.Error)
	}
	out := make([]*types.WebSearchResult, 0, len(decoded.Results))
	for _, r := range decoded.Results {
		out = append(out, &types.WebSearchResult{
			Title: r.Title, URL: r.URL, Snippet: r.Snippet, Content: r.Content,
			Source: p.sourceOr(r.Source), PublishedAt: r.PublishedAt,
		})
	}
	return out, nil
}

// sourceOr defaults the result's source to the plugin id, so a result always
// says which search source produced it.
func (p *PluginProvider) sourceOr(source string) string {
	if source == "" {
		return p.id
	}
	return source
}
