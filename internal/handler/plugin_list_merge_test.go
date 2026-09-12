package handler

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// listOnlyHost answers list requests from manifests and panics on anything that
// would touch the wire, so a test fails loudly if a list request starts dialling.
type listOnlyHost struct {
	manifests []*extension.Manifest
}

func (h *listOnlyHost) ListForTenant(kind extension.Kind, tenantID string) []*extension.Manifest {
	out := make([]*extension.Manifest, 0, len(h.manifests))
	for _, m := range h.manifests {
		if m.Extension.Kind != kind {
			continue
		}
		if _, owner := extension.SplitID(m.Metadata.ID); owner != "" && owner != tenantID {
			continue
		}
		out = append(out, m)
	}
	return out
}

func (h *listOnlyHost) List(extension.Kind) []*extension.Manifest        { return h.manifests }
func (h *listOnlyHost) Get(string) (*extension.Manifest, bool)           { return nil, false }
func (h *listOnlyHost) Ready(context.Context) extension.Readiness        { return extension.Readiness{} }
func (h *listOnlyHost) Close(context.Context) error                      { return nil }
func (h *listOnlyHost) CloseSingle(context.Context, string) error        { return nil }
func (h *listOnlyHost) Unregister(context.Context, string) (bool, error) { return false, nil }

func (h *listOnlyHost) Open(context.Context, string) (extension.Channel, error) {
	panic("a list request must not dial a plugin")
}

func (h *listOnlyHost) Health(context.Context, string) extension.Status {
	panic("a list request must not probe a plugin")
}

func (h *listOnlyHost) HealthAll(context.Context, extension.Kind) map[string]extension.Status {
	panic("a list request must not probe a plugin")
}

func (h *listOnlyHost) Register(context.Context, *extension.Manifest) (bool, error) {
	return false, nil
}

func (h *listOnlyHost) Reload(context.Context) (extension.ReloadResult, error) {
	return extension.ReloadResult{}, nil
}

func (h *listOnlyHost) Reconnect(context.Context, string, string) (extension.Status, error) {
	return extension.Status{}, nil
}

func pluginManifest(id string, kind extension.Kind, priority int) *extension.Manifest {
	m := &extension.Manifest{}
	m.Metadata.ID = id
	m.Extension.Kind = kind
	m.Extension.Priority = priority
	m.Extension.AuthType = "api_key"
	m.Extension.Capabilities = []string{"incremental"}
	return m
}

func tenantContext(tenantID uint64) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(types.TenantIDContextKey.String(), tenantID)
	return c
}

func TestTenantScopeRendersTheRequestTenant(t *testing.T) {
	if got := tenantScope(tenantContext(7)); got != "7" {
		t.Fatalf("tenantScope() = %q, want %q", got, "7")
	}
	gin.SetMode(gin.TestMode)
	anon, _ := gin.CreateTestContext(httptest.NewRecorder())
	if got := tenantScope(anon); got != "0" {
		t.Fatalf("tenantScope(no tenant) = %q, want %q", got, "0")
	}
}

func TestPluginDisplayNameFallsBackToTheID(t *testing.T) {
	m := pluginManifest("notion--1", extension.KindDatasource, 0)
	if got := pluginDisplayName(m); got != "notion--1" {
		t.Fatalf("pluginDisplayName() = %q, want the id", got)
	}
	m.Metadata.Name = "Notion"
	if got := pluginDisplayName(m); got != "Notion" {
		t.Fatalf("pluginDisplayName() = %q, want %q", got, "Notion")
	}
}

func TestVisiblePluginManifests(t *testing.T) {
	if got := visiblePluginManifests(tenantContext(1), nil, extension.KindDatasource); got != nil {
		t.Fatalf("visiblePluginManifests(nil host) = %v, want nil", got)
	}
	off := pluginManifest("off--1", extension.KindDatasource, 0)
	off.Disabled = true
	host := &listOnlyHost{manifests: []*extension.Manifest{
		pluginManifest("notion--1", extension.KindDatasource, 0),
		pluginManifest("notion--2", extension.KindDatasource, 0),
		off,
		pluginManifest("serp", extension.KindWebSearch, 0),
	}}

	got := visiblePluginManifests(tenantContext(1), host, extension.KindDatasource)
	if len(got) != 1 || got[0].Metadata.ID != "notion--1" {
		t.Fatalf("visiblePluginManifests() = %v, want only notion--1", manifestIDs(got))
	}
}

func manifestIDs(ms []*extension.Manifest) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Metadata.ID)
	}
	return out
}

func TestMergeWebSearchProviderTypesKeepsBuiltinsFirst(t *testing.T) {
	builtins := types.GetWebSearchProviderTypes()
	if len(builtins) == 0 {
		t.Skip("this build ships no builtin web search providers")
	}
	clash := builtins[0].ID

	host := &listOnlyHost{manifests: []*extension.Manifest{
		pluginManifest("serp--1", extension.KindWebSearch, 0),
		pluginManifest(clash, extension.KindWebSearch, 0),
	}}
	got := mergeWebSearchProviderTypes(tenantContext(1), host)
	if len(got) != len(builtins)+1 {
		t.Fatalf("merged %d provider types, want %d", len(got), len(builtins)+1)
	}
	for i, b := range builtins {
		if got[i].ID != b.ID {
			t.Fatalf("builtin %d = %q, want %q: plugins must be appended, not interleaved", i, got[i].ID, b.ID)
		}
	}
	if got[len(got)-1].ID != "serp--1" {
		t.Fatalf("last entry = %q, want serp--1", got[len(got)-1].ID)
	}
	// A plugin claiming a builtin id is dropped, so the same name can never
	// mean two different providers.
	seen := 0
	for _, p := range got {
		if p.ID == clash {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("id %q appears %d times, want 1", clash, seen)
	}
}

func TestMergeWebSearchProviderTypesWithoutAHostIsTheBuiltinTable(t *testing.T) {
	if len(mergeWebSearchProviderTypes(tenantContext(1), nil)) != len(types.GetWebSearchProviderTypes()) {
		t.Fatal("a deployment with no host must see exactly the builtin provider table")
	}
}

func TestMergeConnectorMetadataAppendsPluginsPastTheBuiltins(t *testing.T) {
	registry := datasource.NewConnectorRegistry()
	builtins := registry.ListAvailableConnectors()
	base := 0
	for _, m := range builtins {
		if m.Priority >= base {
			base = m.Priority + 1
		}
	}
	clash := builtins[0].Type

	host := &listOnlyHost{manifests: []*extension.Manifest{
		pluginManifest("late--1", extension.KindDatasource, 5),
		pluginManifest("early--1", extension.KindDatasource, 1),
		pluginManifest(clash, extension.KindDatasource, 0),
		pluginManifest("other--2", extension.KindDatasource, 0),
	}}
	got := mergeConnectorMetadata(tenantContext(1), registry, host)
	if len(got) != len(builtins)+2 {
		t.Fatalf("merged %d connectors, want %d", len(got), len(builtins)+2)
	}
	tail := got[len(builtins):]
	if tail[0].Type != "early--1" || tail[1].Type != "late--1" {
		t.Fatalf("plugin tail = %v, want [early--1 late--1]", []string{tail[0].Type, tail[1].Type})
	}
	// Offset past the builtins: a client that re-sorts by priority still
	// renders every plugin after every builtin.
	if tail[0].Priority != base+1 || tail[1].Priority != base+5 {
		t.Fatalf("plugin priorities = [%d %d], want [%d %d]", tail[0].Priority, tail[1].Priority, base+1, base+5)
	}
	if tail[0].AuthType != "api_key" || len(tail[0].Capabilities) != 1 {
		t.Fatalf("plugin metadata lost its declared fields: %+v", tail[0])
	}
}

func TestMergeConnectorMetadataWithoutAHostIsTheBuiltinList(t *testing.T) {
	registry := datasource.NewConnectorRegistry()
	if len(mergeConnectorMetadata(tenantContext(1), registry, nil)) != len(registry.ListAvailableConnectors()) {
		t.Fatal("a deployment with no host must see exactly the builtin connector list")
	}
}
