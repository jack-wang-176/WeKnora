package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type pluginRouteService struct{ interfaces.TenantPluginService }

func (pluginRouteService) ListGlobal(context.Context) ([]*types.TenantPlugin, error) {
	return []*types.TenantPlugin{}, nil
}

func TestPluginProcessRoutesRequireSystemAdministrator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, systemAdmin := range []bool{false, true} {
		engine := gin.New()
		engine.Use(func(request *gin.Context) {
			ctx := context.WithValue(request.Request.Context(), types.TenantRoleContextKey, types.TenantRoleOwner)
			ctx = context.WithValue(ctx, types.SystemAdminContextKey, systemAdmin)
			request.Request = request.Request.WithContext(ctx)
		})
		guards := &rbacGuards{cfg: &config.Config{}}
		RegisterPluginRoutes(engine.Group("/api/v1"), handler.NewPluginHandler(pluginRouteService{}, nil, nil), guards)
		for _, route := range engine.Routes() {
			if _, allowed := guards.apiKeyAuthorizer.Lookup(route.Method, route.Path); len(route.Path) >= len("/api/v1/admin/") && route.Path[:len("/api/v1/admin/")] == "/api/v1/admin/" {
				require.False(t, allowed)
			}
		}
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/admin/plugins", nil))
		if systemAdmin {
			require.Equal(t, http.StatusOK, response.Code)
		} else {
			require.Equal(t, http.StatusForbidden, response.Code)
			for _, route := range engine.Routes() {
				if len(route.Path) < len("/api/v1/admin/") || route.Path[:len("/api/v1/admin/")] != "/api/v1/admin/" {
					continue
				}
				denied := httptest.NewRecorder()
				engine.ServeHTTP(denied, httptest.NewRequest(route.Method, route.Path, nil))
				require.Equal(t, http.StatusForbidden, denied.Code, route.Method+" "+route.Path)
			}
		}
	}
}
