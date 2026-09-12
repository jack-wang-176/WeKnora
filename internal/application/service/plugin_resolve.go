package service

import (
	"context"
	"strconv"

	"github.com/Tencent/WeKnora/internal/extension"
	infra_web_search "github.com/Tencent/WeKnora/internal/infrastructure/web_search"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// TenantScopeFromContext renders the tenant the way scoped plugin ids carry it.
// No tenant in ctx means process-level plugins only.
func TenantScopeFromContext(ctx context.Context) string {
	if id, ok := types.TenantIDFromContext(ctx); ok {
		return strconv.FormatUint(id, 10)
	}
	return ""
}

// VisiblePlugin returns the manifest this tenant may use under id: right kind,
// not turned off, and either host-wide or its own. The owner check is here and
// nowhere else — copied into each consumer it becomes a cross-tenant leak the
// day one copy is wrong.
func VisiblePlugin(
	ctx context.Context, host extension.Host, kind extension.Kind, id string,
) (*extension.Manifest, bool) {
	if host == nil || id == "" {
		return nil, false
	}
	m, ok := host.Get(id)
	if !ok || m.Disabled || m.Extension.Kind != kind {
		return nil, false
	}
	if _, owner := extension.SplitID(id); owner != "" && owner != TenantScopeFromContext(ctx) {
		return nil, false
	}
	return m, true
}

// ResolveWebSearchProvider is the two-stage lookup every web search call site
// goes through: the builtin registry first, the extension host only once the
// registry has no such type. Written once so the three call sites cannot drift.
//
// A plugin that is gone is not a special case: host.Get misses and the
// registry's own "provider type %s not registered" is returned unchanged.
func ResolveWebSearchProvider(
	ctx context.Context,
	registry *infra_web_search.Registry,
	host extension.Host,
	providerType string,
	params types.WebSearchProviderParameters,
) (interfaces.WebSearchProvider, error) {
	provider, err := registry.CreateProvider(providerType, params)
	if err == nil {
		return provider, nil
	}
	if _, ok := VisiblePlugin(ctx, host, extension.KindWebSearch, providerType); !ok {
		return nil, err
	}
	ch, oerr := host.Open(ctx, providerType)
	if oerr != nil {
		return nil, oerr
	}
	return infra_web_search.NewPluginProvider(providerType, ch, params), nil
}
