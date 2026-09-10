package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// Plugins get two route groups rather than one group that inspects tenant_id,
// because the permission difference between the scopes has to be visible to
// assertAPIKeyPoliciesMatchRoutes at start-up — that self-check exists to catch
// a new endpoint with no gate, and it can only read the route table.
//
// Tenant scope: Viewer+ reads, Admin+ writes. Installing points this process at
// an address of the caller's choosing, so it is not a Contributor operation.
//
// Process scope: Admin+ throughout, reads included (24 §8.1), and full-access
// API keys only — those plugins serve every tenant, and the listing alone names
// what this process will connect to.
//
// The SSE progress endpoint is deliberately absent until step 6: under the
// `endpoint` channel registration is synchronous and has no progress to report,
// and an endpoint that answers 501 reads as a broken feature.
func RegisterPluginRoutes(r *gin.RouterGroup, h *handler.PluginHandler, g *rbacGuards) {
	plugins := g.apiKeyGroup(r.Group("/plugins"), apiKeyFullAccess())
	{
		plugins.GET("", g.Viewer(), h.List)
		plugins.POST("", g.Admin(), h.Register)
		plugins.GET("/:id", g.Viewer(), h.Get)
		plugins.DELETE("/:id", g.Admin(), h.Uninstall)
		plugins.POST("/:id/enable", g.Admin(), h.Enable)
		plugins.POST("/:id/disable", g.Admin(), h.Disable)
		plugins.POST("/:id/reconnect", g.Admin(), h.Reconnect)
		plugins.PUT("/:id/envs", g.Admin(), h.SetEnvs)
	}

	admin := g.apiKeyGroup(r.Group("/admin/plugins", handler.PluginProcessScope()), apiKeyFullAccess())
	{
		admin.GET("", g.Admin(), h.List)
		admin.POST("", g.Admin(), h.Register)
		admin.GET("/:id", g.Admin(), h.Get)
		admin.DELETE("/:id", g.Admin(), h.Uninstall)
		admin.POST("/:id/enable", g.Admin(), h.Enable)
		admin.POST("/:id/disable", g.Admin(), h.Disable)
		admin.POST("/:id/reconnect", g.Admin(), h.Reconnect)
		admin.PUT("/:id/envs", g.Admin(), h.SetEnvs)
	}
}
