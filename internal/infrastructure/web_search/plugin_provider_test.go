package web_search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

// httpCarrierChannel is the remote-http shape of extension.Channel: Conn()
// hands back the carrier the provider asserts on.
type httpCarrierChannel struct{ base string }

func (c httpCarrierChannel) Conn() any                       { return c }
func (c httpCarrierChannel) Reconnect(context.Context) error { return nil }
func (c httpCarrierChannel) BaseURL() string                 { return c.base }
func (c httpCarrierChannel) Do(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

// unknownTransportChannel is a channel whose connection is neither a gRPC
// connection nor an HTTP carrier.
type unknownTransportChannel struct{}

func (unknownTransportChannel) Conn() any                       { return struct{}{} }
func (unknownTransportChannel) Reconnect(context.Context) error { return nil }

func pluginSearchServer(t *testing.T, status int, body any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PluginSearchPath {
			t.Errorf("plugin was called on %q, want %q", r.URL.Path, PluginSearchPath)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPluginProviderNameIsTheScopedID(t *testing.T) {
	if got := NewPluginProvider("serp--7", nil, types.WebSearchProviderParameters{}).Name(); got != "serp--7" {
		t.Fatalf("Name() = %q, want serp--7", got)
	}
}

func TestPluginProviderSearchOverHTTP(t *testing.T) {
	srv := pluginSearchServer(t, http.StatusOK, map[string]any{
		"results": []map[string]any{
			{"title": "t1", "url": "u1", "snippet": "s1", "content": "c1", "source": "upstream"},
			{"title": "t2", "url": "u2"},
		},
	})
	p := NewPluginProvider("serp--7", httpCarrierChannel{base: srv.URL}, types.WebSearchProviderParameters{})

	got, err := p.Search(context.Background(), "q", 2, false)
	if err != nil {
		t.Fatalf("Search() = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Search() returned %d results, want 2", len(got))
	}
	if got[0].Source != "upstream" {
		t.Fatalf("results[0].Source = %q, want the plugin's own value", got[0].Source)
	}
	// A result always says where it came from, so an empty source defaults to
	// the plugin id rather than reaching the UI blank.
	if got[1].Source != "serp--7" {
		t.Fatalf("results[1].Source = %q, want the plugin id", got[1].Source)
	}
}

func TestPluginProviderSearchSurfacesPluginErrors(t *testing.T) {
	srv := pluginSearchServer(t, http.StatusOK, map[string]any{"error": "quota exhausted"})
	p := NewPluginProvider("serp--7", httpCarrierChannel{base: srv.URL}, types.WebSearchProviderParameters{})
	_, err := p.Search(context.Background(), "q", 1, false)
	if err == nil || !contains(err.Error(), "quota exhausted") {
		t.Fatalf("Search() = %v, want the plugin's own message", err)
	}
}

func TestPluginProviderSearchReportsHTTPStatus(t *testing.T) {
	srv := pluginSearchServer(t, http.StatusBadGateway, map[string]any{})
	p := NewPluginProvider("serp--7", httpCarrierChannel{base: srv.URL}, types.WebSearchProviderParameters{})
	_, err := p.Search(context.Background(), "q", 1, false)
	if err == nil || !contains(err.Error(), "http 502") {
		t.Fatalf("Search() = %v, want the status code", err)
	}
}

func TestPluginProviderSearchWithoutAChannelOrTransport(t *testing.T) {
	if _, err := NewPluginProvider("serp--7", nil, types.WebSearchProviderParameters{}).
		Search(context.Background(), "q", 1, false); err == nil ||
		!contains(err.Error(), "not connected") {
		t.Fatalf("Search(no channel) = %v, want a not-connected error", err)
	}
	if _, err := NewPluginProvider("serp--7", unknownTransportChannel{}, types.WebSearchProviderParameters{}).
		Search(context.Background(), "q", 1, false); err == nil ||
		!contains(err.Error(), "no usable transport") {
		t.Fatalf("Search(unknown transport) = %v, want a transport error", err)
	}
}

// Secrets reach a plugin once, as install-time envs. Neither the API key nor
// the proxy URL may ride along on every request.
func TestPluginProviderCallParamsWithholdsSecrets(t *testing.T) {
	p := NewPluginProvider("serp--7", nil, types.WebSearchProviderParameters{
		APIKey:      "sk-secret",
		ProxyURL:    "http://proxy.internal:8080",
		BaseURL:     "https://api.example.com",
		EngineID:    "engine-1",
		ExtraConfig: map[string]string{"region": "us"},
	})
	got := p.callParams()
	for _, leaked := range []string{"sk-secret", "http://proxy.internal:8080"} {
		for k, v := range got {
			if v == leaked {
				t.Fatalf("callParams()[%q] leaked %q", k, leaked)
			}
		}
	}
	if got["base_url"] != "https://api.example.com" || got["engine_id"] != "engine-1" || got["region"] != "us" {
		t.Fatalf("callParams() = %v, want base_url, engine_id and the extra config", got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
