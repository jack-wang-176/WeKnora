package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type bundleResponseService struct {
	bundlePluginService
	row *types.TenantPlugin
}

func (stub bundleResponseService) InstallPlugin(context.Context, *types.PluginInstallRequest) (*types.TenantPlugin, error) {
	return stub.row, nil
}

func (stub bundleResponseService) EnablePlugin(context.Context, *uint64, string) (*types.TenantPlugin, error) {
	return stub.row, nil
}

func (stub bundleResponseService) DisablePlugin(context.Context, *uint64, string) (*types.TenantPlugin, error) {
	return stub.row, nil
}

func TestPluginBundleLifecycleResponseEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &PluginHandler{bundle: bundleResponseService{row: &types.TenantPlugin{ID: "row-id", PluginID: "source--7", Channel: types.PluginChannelBundle}}}
	for _, action := range []string{"install", "enable", "disable"} {
		t.Run(action, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"plugin_id":"source","source_type":"image","source_url":"source:1","manifest":"metadata: {}"}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			ctx.Params = gin.Params{{Key: "id", Value: "source--7"}}
			wantStatus := http.StatusAccepted
			switch action {
			case "install":
				handler.Install(ctx)
			case "enable":
				handler.setBundleEnabled(ctx, true)
			case "disable":
				wantStatus = http.StatusOK
				handler.setBundleEnabled(ctx, false)
			}
			require.Equal(t, wantStatus, recorder.Code)
			var response struct {
				Success bool `json:"success"`
				Data    struct {
					PluginID string `json:"plugin_id"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			require.True(t, response.Success)
			require.Equal(t, "source--7", response.Data.PluginID)
		})
	}
}
