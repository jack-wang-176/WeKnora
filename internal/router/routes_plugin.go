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
// The `bundle` channel adds two endpoints the `endpoint` channel had no use
// for: /install, because a source has to be named somewhere, and /:id/events,
// because a run that outlives its request needs somewhere to report progress.
// Both share the scope rules above; events is a read, gated like the listing.
func RegisterPluginRoutes(r *gin.RouterGroup, h *handler.PluginHandler, g *rbacGuards) {
	plugins := g.apiKeyGroup(r.Group("/plugins"), apiKeyFullAccess())
	{
		plugins.GET("", g.Viewer(), h.List)
		plugins.POST("", g.Admin(), h.Register)
		plugins.POST("/install", g.Admin(), h.Install)
		plugins.GET("/:id", g.Viewer(), h.Get)
		plugins.GET("/:id/events", g.Viewer(), h.InstallEvents)
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
		admin.POST("/install", g.Admin(), h.Install)
		admin.GET("/:id", g.Admin(), h.Get)
		admin.GET("/:id/events", g.Admin(), h.InstallEvents)
		admin.DELETE("/:id", g.Admin(), h.Uninstall)
		admin.POST("/:id/enable", g.Admin(), h.Enable)
		admin.POST("/:id/disable", g.Admin(), h.Disable)
		admin.POST("/:id/reconnect", g.Admin(), h.Reconnect)
		admin.PUT("/:id/envs", g.Admin(), h.SetEnvs)
	}
}
