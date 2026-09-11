package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// Two route groups, not one that inspects tenant_id: the scope difference has to
// be visible to assertAPIKeyPoliciesMatchRoutes, which can only read the route
// table. Tenant scope is Viewer+ read / Admin+ write; process scope is Admin+
// throughout and full-access keys only, since those plugins serve every tenant.
// The bundle channel adds /install and /:id/events under the same rules.
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
