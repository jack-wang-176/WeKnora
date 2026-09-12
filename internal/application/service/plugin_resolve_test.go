package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/extension"
	infra_web_search "github.com/Tencent/WeKnora/internal/infrastructure/web_search"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubChannel stands in for a dialled plugin connection. Conn() is nil, so any
// call that actually tries to talk to the plugin fails instead of hanging.
type stubChannel struct{ endpoint string }

func (c *stubChannel) Conn() any                       { return nil }
func (c *stubChannel) Endpoint() string                { return c.endpoint }
func (c *stubChannel) Healthy(context.Context) error   { return nil }
func (c *stubChannel) Reconnect(context.Context) error { return nil }
func (c *stubChannel) Close() error                    { return nil }

// stubHost is an extension.Host backed by a manifest list. openErr makes the
// open path fail, and openConn overrides the channel handed back.
type stubHost struct {
	manifests map[string]*extension.Manifest
	openErr   error
	openConn  extension.Channel
	opened    []string
}

func newStubHost(ms ...*extension.Manifest) *stubHost {
	h := &stubHost{manifests: make(map[string]*extension.Manifest, len(ms))}
	for _, m := range ms {
		h.manifests[m.Metadata.ID] = m
	}
	return h
}

func (h *stubHost) Get(id string) (*extension.Manifest, bool) {
	m, ok := h.manifests[id]
	return m, ok
}

func (h *stubHost) List(kind extension.Kind) []*extension.Manifest {
	out := make([]*extension.Manifest, 0, len(h.manifests))
	for _, m := range h.manifests {
		if kind == "" || m.Extension.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

func (h *stubHost) ListForTenant(kind extension.Kind, tenantID string) []*extension.Manifest {
	out := make([]*extension.Manifest, 0, len(h.manifests))
	for _, m := range h.List(kind) {
		if _, owner := extension.SplitID(m.Metadata.ID); owner != "" && owner != tenantID {
			continue
		}
		out = append(out, m)
	}
	sortManifests(out)
	return out
}

func sortManifests(ms []*extension.Manifest) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && ms[j].Metadata.ID < ms[j-1].Metadata.ID; j-- {
			ms[j], ms[j-1] = ms[j-1], ms[j]
		}
	}
}

func (h *stubHost) Open(_ context.Context, id string) (extension.Channel, error) {
	h.opened = append(h.opened, id)
	if h.openErr != nil {
		return nil, h.openErr
	}
	if h.openConn != nil {
		return h.openConn, nil
	}
	return &stubChannel{endpoint: "dns:///plugin:50051"}, nil
}

func (h *stubHost) Health(context.Context, string) extension.Status { return extension.Status{} }

func (h *stubHost) HealthAll(context.Context, extension.Kind) map[string]extension.Status {
	return nil
}

func (h *stubHost) Ready(context.Context) extension.Readiness { return extension.Readiness{} }
func (h *stubHost) Close(context.Context) error               { return nil }
func (h *stubHost) CloseSingle(context.Context, string) error { return nil }

func (h *stubHost) Register(context.Context, *extension.Manifest) (bool, error) {
	return false, nil
}

func (h *stubHost) Unregister(context.Context, string) (bool, error) { return false, nil }

func (h *stubHost) Reload(context.Context) (extension.ReloadResult, error) {
	return extension.ReloadResult{}, nil
}

func (h *stubHost) Reconnect(context.Context, string, string) (extension.Status, error) {
	return extension.Status{}, nil
}

func manifestOf(id string, kind extension.Kind) *extension.Manifest {
	m := &extension.Manifest{}
	m.Metadata.ID = id
	m.Extension.Kind = kind
	return m
}

func tenantCtx(id uint64) context.Context {
	return context.WithValue(context.Background(), types.TenantIDContextKey, id)
}

func TestTenantScopeFromContext(t *testing.T) {
	if got := TenantScopeFromContext(tenantCtx(9)); got != "9" {
		t.Fatalf("TenantScopeFromContext() = %q, want %q", got, "9")
	}
	// No tenant means process-level plugins only, not tenant zero's.
	if got := TenantScopeFromContext(context.Background()); got != "" {
		t.Fatalf("TenantScopeFromContext(no tenant) = %q, want empty", got)
	}
}

func TestVisiblePlugin(t *testing.T) {
	off := manifestOf("off--1", extension.KindWebSearch)
	off.Disabled = true
	host := newStubHost(
		manifestOf("serp", extension.KindWebSearch),
		manifestOf("serp--1", extension.KindWebSearch),
		manifestOf("serp--2", extension.KindWebSearch),
		manifestOf("notion--1", extension.KindDatasource),
		off,
	)

	cases := []struct {
		name string
		ctx  context.Context
		kind extension.Kind
		id   string
		want bool
	}{
		{"host wide", tenantCtx(1), extension.KindWebSearch, "serp", true},
		{"own tenant", tenantCtx(1), extension.KindWebSearch, "serp--1", true},
		{"another tenant", tenantCtx(1), extension.KindWebSearch, "serp--2", false},
		{"scoped without a tenant", context.Background(), extension.KindWebSearch, "serp--1", false},
		{"wrong kind", tenantCtx(1), extension.KindWebSearch, "notion--1", false},
		{"disabled", tenantCtx(1), extension.KindWebSearch, "off--1", false},
		{"unknown id", tenantCtx(1), extension.KindWebSearch, "absent", false},
		{"empty id", tenantCtx(1), extension.KindWebSearch, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := VisiblePlugin(tc.ctx, host, tc.kind, tc.id); ok != tc.want {
				t.Fatalf("VisiblePlugin(%q) = %v, want %v", tc.id, ok, tc.want)
			}
		})
	}
	// A deployment with no host resolves nothing rather than panicking.
	if _, ok := VisiblePlugin(tenantCtx(1), nil, extension.KindWebSearch, "serp"); ok {
		t.Fatal("VisiblePlugin(nil host) = true, want false")
	}
}

type stubProvider struct{ name string }

func (p stubProvider) Name() string { return p.name }

func (p stubProvider) Search(context.Context, string, int, bool) ([]*types.WebSearchResult, error) {
	return nil, nil
}

func registryWith(id string) *infra_web_search.Registry {
	r := infra_web_search.NewRegistry()
	r.Register(id, func(types.WebSearchProviderParameters) (interfaces.WebSearchProvider, error) {
		return stubProvider{name: id}, nil
	})
	return r
}

// The builtin registry is asked first, so installing a plugin can never
// reroute a builtin provider type.
func TestResolveWebSearchProviderPrefersTheRegistry(t *testing.T) {
	host := newStubHost(manifestOf("bing", extension.KindWebSearch))
	got, err := ResolveWebSearchProvider(tenantCtx(1), registryWith("bing"), host, "bing",
		types.WebSearchProviderParameters{})
	if err != nil {
		t.Fatalf("ResolveWebSearchProvider() = %v", err)
	}
	if _, isPlugin := got.(*infra_web_search.PluginProvider); isPlugin {
		t.Fatal("a plugin shadowed a registered builtin provider type")
	}
	if len(host.opened) != 0 {
		t.Fatalf("the host was opened %v although the registry answered", host.opened)
	}
}

func TestResolveWebSearchProviderFallsBackToThePlugin(t *testing.T) {
	host := newStubHost(manifestOf("serp--1", extension.KindWebSearch))
	got, err := ResolveWebSearchProvider(tenantCtx(1), registryWith("bing"), host, "serp--1",
		types.WebSearchProviderParameters{})
	if err != nil {
		t.Fatalf("ResolveWebSearchProvider() = %v", err)
	}
	if got.Name() != "serp--1" {
		t.Fatalf("provider Name() = %q, want the plugin id", got.Name())
	}
}

// A type that is neither registered nor a visible plugin keeps the registry's
// own message: "gone plugin" is not a separate failure mode.
func TestResolveWebSearchProviderKeepsTheRegistryError(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		host extension.Host
		id   string
	}{
		{"no host", tenantCtx(1), nil, "serp--1"},
		{"unknown type", tenantCtx(1), newStubHost(), "serp--1"},
		{"another tenant", tenantCtx(1), newStubHost(manifestOf("serp--2", extension.KindWebSearch)), "serp--2"},
		{"wrong kind", tenantCtx(1), newStubHost(manifestOf("notion--1", extension.KindDatasource)), "notion--1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveWebSearchProvider(tc.ctx, registryWith("bing"), tc.host, tc.id,
				types.WebSearchProviderParameters{})
			if err == nil || !strings.Contains(err.Error(), "not registered") {
				t.Fatalf("ResolveWebSearchProvider() = %v, want the registry's own error", err)
			}
		})
	}
}

func TestResolveWebSearchProviderReportsAnOpenFailure(t *testing.T) {
	host := newStubHost(manifestOf("serp--1", extension.KindWebSearch))
	host.openErr = errors.New("dial refused")
	_, err := ResolveWebSearchProvider(tenantCtx(1), registryWith("bing"), host, "serp--1",
		types.WebSearchProviderParameters{})
	if err == nil || !strings.Contains(err.Error(), "dial refused") {
		t.Fatalf("ResolveWebSearchProvider() = %v, want the open failure", err)
	}
}
