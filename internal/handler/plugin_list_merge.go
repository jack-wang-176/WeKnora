package handler

import (
	"strconv"

	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// tenantScope renders the request's tenant the way scoped plugin ids carry it.
func tenantScope(c *gin.Context) string {
	return strconv.FormatUint(c.GetUint64(types.TenantIDContextKey.String()), 10)
}

// pluginDisplayName falls back to the id, which is always present.
func pluginDisplayName(m *extension.Manifest) string {
	if m.Metadata.Name != "" {
		return m.Metadata.Name
	}
	return m.Metadata.ID
}

// visiblePluginManifests returns the tenant's usable manifests of one kind.
//
// Manifests only — a list request must never dial plugins: one unreachable
// plugin would then decide how long the "add a data source" dialog takes to
// open. A plugin that is listed but down fails when it is actually used.
func visiblePluginManifests(c *gin.Context, host extension.Host, kind extension.Kind) []*extension.Manifest {
	if host == nil {
		return nil
	}
	out := make([]*extension.Manifest, 0, 4)
	for _, m := range host.ListForTenant(kind, tenantScope(c)) {
		if m == nil || m.Disabled {
			continue
		}
		out = append(out, m)
	}
	return out
}

// mergeWebSearchProviderTypes appends one entry per web search plugin after the
// builtin table. Builtins win on a name clash, and the static table itself is
// never touched — it is compile-time data with no tenant dimension.
func mergeWebSearchProviderTypes(c *gin.Context, host extension.Host) []types.WebSearchProviderTypeInfo {
	out := types.GetWebSearchProviderTypes()
	seen := make(map[string]struct{}, len(out))
	for _, t := range out {
		seen[t.ID] = struct{}{}
	}
	for _, m := range visiblePluginManifests(c, host, extension.KindWebSearch) {
		if _, dup := seen[m.Metadata.ID]; dup {
			continue
		}
		out = append(out, types.WebSearchProviderTypeInfo{
			ID:          m.Metadata.ID,
			Name:        pluginDisplayName(m),
			Description: m.Metadata.Description,
			DocsURL:     m.Metadata.Homepage,
		})
	}
	return out
}
