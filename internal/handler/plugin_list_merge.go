package handler

import (
	"sort"
	"strconv"

	"github.com/Tencent/WeKnora/internal/datasource"
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

// mergeConnectorMetadata appends one entry per datasource plugin after the
// builtin connectors. Builtins win on a type clash, and plugin priorities are
// offset past the builtins so a client that re-sorts still renders them last.
func mergeConnectorMetadata(
	c *gin.Context, registry *datasource.ConnectorRegistry, host extension.Host,
) []datasource.ConnectorMetadata {
	out := registry.ListAvailableConnectors()
	seen := make(map[string]struct{}, len(out))
	base := 0
	for _, m := range out {
		seen[m.Type] = struct{}{}
		if m.Priority >= base {
			base = m.Priority + 1
		}
	}
	plugins := visiblePluginManifests(c, host, extension.KindDatasource)
	sort.SliceStable(plugins, func(i, j int) bool {
		return plugins[i].Extension.Priority < plugins[j].Extension.Priority
	})
	for _, m := range plugins {
		if _, dup := seen[m.Metadata.ID]; dup {
			continue
		}
		out = append(out, datasource.ConnectorMetadata{
			Type:         m.Metadata.ID,
			Name:         pluginDisplayName(m),
			Description:  m.Metadata.Description,
			Icon:         m.Metadata.Icon,
			Priority:     base + m.Extension.Priority,
			AuthType:     m.Extension.AuthType,
			Capabilities: append([]string(nil), m.Extension.Capabilities...),
		})
	}
	return out
}
