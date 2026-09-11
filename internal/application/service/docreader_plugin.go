package service

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PluginDocumentReader sends a read to the docparser plugin named by
// ParserEngine and leaves every other engine to the builtin reader.
//
// Plugins speak the same docreader contract, so this adds no new wire format:
// the engine name is the plugin id, and the plugin is resolved per call so one
// installed after start-up needs no restart.
type PluginDocumentReader struct {
	builtin interfaces.DocumentReader
	host    extension.Host
}

var _ interfaces.DocumentReader = (*PluginDocumentReader)(nil)

// NewPluginDocumentReader wraps builtin. Without a host it returns builtin
// unchanged, so a deployment with no plugins keeps the path it had before.
func NewPluginDocumentReader(builtin interfaces.DocumentReader, host extension.Host) interfaces.DocumentReader {
	if builtin == nil || host == nil {
		return builtin
	}
	return &PluginDocumentReader{builtin: builtin, host: host}
}

// Reconnect and IsConnected stay on the builtin reader: plugin channels are
// dialled and repointed by the extension host, not by the docreader endpoints.
func (r *PluginDocumentReader) Reconnect(addr string) error { return r.builtin.Reconnect(addr) }

func (r *PluginDocumentReader) IsConnected() bool { return r.builtin.IsConnected() }

func (r *PluginDocumentReader) Read(ctx context.Context, req *types.ReadRequest) (*types.ReadResult, error) {
	reader, ok := r.pluginReader(ctx, req.ParserEngine)
	if !ok {
		return r.builtin.Read(ctx, req)
	}
	result, err := reader.Read(ctx, req)
	if err == nil {
		return result, nil
	}
	// A plugin that is down says nothing about the document, so retry on the
	// builtin engine. Anything else is the plugin's verdict on this file and
	// must reach the caller unchanged.
	if !pluginUnreachable(err) {
		return nil, err
	}
	logger.Warnf(ctx, "[DocParser] plugin %s unreachable, falling back to builtin: %v", req.ParserEngine, err)
	fallback := *req
	fallback.ParserEngine = ""
	return r.builtin.Read(ctx, &fallback)
}

// ListEngines appends the tenant's docparser plugins to the builtin engines.
// It reads manifests only: listing engines must never dial a plugin.
func (r *PluginDocumentReader) ListEngines(
	ctx context.Context, overrides map[string]string,
) ([]types.ParserEngineInfo, error) {
	plugins := r.pluginEngines(ctx)
	engines, err := r.builtin.ListEngines(ctx, overrides)
	if err != nil {
		if len(plugins) == 0 {
			return nil, err
		}
		logger.Warnf(ctx, "[DocParser] builtin ListEngines failed, listing plugins only: %v", err)
		return plugins, nil
	}
	seen := make(map[string]struct{}, len(engines))
	for _, e := range engines {
		seen[e.Name] = struct{}{}
	}
	for _, p := range plugins {
		if _, dup := seen[p.Name]; dup {
			continue
		}
		engines = append(engines, p)
	}
	return engines, nil
}

// pluginReader returns a reader bound to the plugin named by engine, or false
// when engine is not a plugin this tenant may see. An open failure degrades to
// the builtin reader rather than failing the read.
func (r *PluginDocumentReader) pluginReader(ctx context.Context, engine string) (interfaces.DocumentReader, bool) {
	m, ok := VisiblePlugin(ctx, r.host, extension.KindDocParser, engine)
	if !ok {
		return nil, false
	}
	ch, err := r.host.Open(ctx, engine)
	if err != nil {
		logger.Warnf(ctx, "[DocParser] open plugin %s failed, falling back to builtin: %v", engine, err)
		return nil, false
	}
	if m.Runtime.Transport == extension.TransportRemoteHTTP {
		return docparser.NewHTTPDocumentReader(ch), true
	}
	return docparser.NewGRPCDocumentReaderFromChannel(ch), true
}

// pluginEngines describes each visible docparser plugin as an engine. The
// manifest's capabilities are the file types it claims.
func (r *PluginDocumentReader) pluginEngines(ctx context.Context) []types.ParserEngineInfo {
	manifests := r.host.ListForTenant(extension.KindDocParser, TenantScopeFromContext(ctx))
	out := make([]types.ParserEngineInfo, 0, len(manifests))
	for _, m := range manifests {
		if m.Disabled || m.Metadata.ID == extension.DocreaderExtesnionID {
			continue
		}
		out = append(out, types.ParserEngineInfo{
			Name:        m.Metadata.ID,
			Description: m.Metadata.Description,
			FileTypes:   append([]string(nil), m.Extension.Capabilities...),
			Available:   true,
		})
	}
	return out
}

// pluginUnreachable reports whether the failure is about the transport rather
// than the document: nothing was parsed, so the builtin engine may still try.
func pluginUnreachable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Unimplemented:
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}
