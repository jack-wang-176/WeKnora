package handler

import (
	stderrors "errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// pluginProcessScopeKey marks a request as addressing process-level plugins.
const pluginProcessScopeKey = "plugin_process_scope"

// PluginProcessScope is applied to the /admin/plugins group in
// routes_plugin.go. The scope is a property of the route, like the permission
// gate: deciding it from the request body would hide the difference from
// assertAPIKeyPoliciesMatchRoutes, which can only see the route table.
func PluginProcessScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(pluginProcessScopeKey, true)
		c.Next()
	}
}

// PluginHandler serves both plugin scopes over one set of handlers.
//
// It validates nothing about the endpoint beyond trim and non-empty: endpoint
// grammar is the host's job, since a handler does not know whether the extension
// speaks gRPC or HTTP and the URL check would refuse `host:port`.
type PluginHandler struct {
	service interfaces.TenantPluginService
	host    extension.Host
	// bundle serves the rows the endpoint channel does not own. It may be nil:
	// without a container runtime there is no bundle channel to serve.
	bundle bundlePluginService
}

func NewPluginHandler(
	svc interfaces.TenantPluginService, host extension.Host, bundle bundlePluginService,
) *PluginHandler {
	return &PluginHandler{service: svc, host: host, bundle: bundle}
}

// pluginResponse is the wire shape. Envs are reported as key names only: the
// values are credentials, and this is the read path.
type pluginResponse struct {
	ID          string         `json:"id"`
	TenantID    *uint64        `json:"tenant_id,omitempty"`
	PluginID    string         `json:"plugin_id"`
	Kind        string         `json:"kind"`
	Channel     string         `json:"channel"`
	Transport   string         `json:"transport"`
	Endpoint    string         `json:"endpoint,omitempty"`
	PolicyClass string         `json:"policy_class"`
	Status      string         `json:"status"`
	Enabled     bool           `json:"enabled"`
	Error       string         `json:"error,omitempty"`
	EnvKeys     []string       `json:"env_keys"`
	Permissions map[string]any `json:"permissions,omitempty"`
	State       string         `json:"state,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

func toPluginResponse(row *types.TenantPlugin) pluginResponse {
	if row == nil {
		return pluginResponse{}
	}
	keys := make([]string, 0, len(row.Envs))
	for k := range row.Envs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return pluginResponse{
		ID:          row.ID,
		TenantID:    row.TenantID,
		PluginID:    row.PluginID,
		Kind:        row.Kind,
		Channel:     row.Channel,
		Transport:   row.Transport,
		Endpoint:    derefStringValue(row.Endpoint),
		PolicyClass: row.PolicyClass,
		Status:      row.Status,
		Enabled:     row.Enabled,
		Error:       derefStringValue(row.Error),
		EnvKeys:     keys,
		Permissions: row.Permissions,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

func derefStringValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// pluginScope reads the scope the route declared. nil is the process level.
func pluginScope(c *gin.Context) *uint64 {
	if c.GetBool(pluginProcessScopeKey) {
		return nil
	}
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	return &tenantID
}

func respondPluginServiceError(c *gin.Context, err error) {
	switch {
	case stderrors.Is(err, service.ErrPluginInvalid):
		_ = c.Error(apperrors.NewBadRequestError(err.Error()))
	case stderrors.Is(err, service.ErrPluginNotFound):
		_ = c.Error(apperrors.NewNotFoundError(err.Error()))
	case stderrors.Is(err, service.ErrPluginExists), stderrors.Is(err, service.ErrPluginDisabled):
		_ = c.Error(apperrors.NewConflictError(err.Error()))
	case stderrors.Is(err, extension.ErrBuiltinImmutable):
		_ = c.Error(apperrors.NewForbiddenError(err.Error()))
	default:
		_ = c.Error(err)
	}
}

// List godoc
// @Summary      List plugins
// @Description  List the extensions installed in the caller's scope.
// @Tags         Plugin
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "Installed plugins"
// @Failure      401  {object}  map[string]interface{}  "Unauthorized"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /plugins [get]
func (h *PluginHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	var (
		rows []*types.TenantPlugin
		err  error
	)
	if tenantID := pluginScope(c); tenantID == nil {
		rows, err = h.service.ListGlobal(ctx)
	} else {
		rows, err = h.service.ListForTenant(ctx, *tenantID)
	}
	if err != nil {
		respondPluginServiceError(c, err)
		return
	}
	data := make([]pluginResponse, 0, len(rows))
	for _, row := range rows {
		data = append(data, toPluginResponse(row))
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

// Get godoc
// @Summary      Get one plugin
// @Description  Plugin detail with its install status. Credential values are never returned.
// @Tags         Plugin
// @Produce      json
// @Param        id   path      string  true  "Plugin ID"
// @Success      200  {object}  map[string]interface{}  "Plugin detail"
// @Failure      404  {object}  apperrors.AppError      "Plugin not found"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /plugins/{id} [get]
func (h *PluginHandler) Get(c *gin.Context) {
	row, err := h.service.Get(c.Request.Context(), pluginScope(c), c.Param("id"))
	if err != nil {
		respondPluginServiceError(c, err)
		return
	}
	resp := toPluginResponse(row)
	// The live state is only asked for when the row claims to be loaded: for any
	// other row the host has no entry, and probing one would report "unavailable"
	// for a plugin that was never meant to be up.
	if h.host != nil && row.Enabled && row.Status == types.PluginStatusReady {
		resp.State = string(h.host.Health(c.Request.Context(), row.PluginID).State)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": resp})
}

// Register godoc
// @Summary      Install a plugin
// @Description  Register an external extension by address and load it.
// @Tags         Plugin
// @Accept       json
// @Produce      json
// @Param        request  body      types.PluginRegisterRequest  true  "Plugin to install"
// @Success      200      {object}  map[string]interface{}       "Installed plugin"
// @Failure      400      {object}  apperrors.AppError           "Invalid request"
// @Failure      409      {object}  apperrors.AppError           "Already installed"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /plugins [post]
func (h *PluginHandler) Register(c *gin.Context) {
	var req types.PluginRegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(apperrors.NewBadRequestError("invalid plugin registration body"))
		return
	}
	// The scope comes from the route, so a tenant_id in the body is overwritten
	// rather than honoured: otherwise a tenant-scoped call could install a
	// process-level plugin by asking for one.
	req.TenantID = pluginScope(c)
	req.PluginID = strings.TrimSpace(req.PluginID)
	req.Kind = strings.TrimSpace(req.Kind)
	req.Transport = strings.TrimSpace(req.Transport)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.PolicyClass = strings.TrimSpace(req.PolicyClass)
	if req.Endpoint == "" {
		_ = c.Error(apperrors.NewBadRequestError("endpoint is required"))
		return
	}

	// A row that exists but failed to load is left in place by the service and
	// readable through GET, so only the error is rendered here.
	row, err := h.service.Register(c.Request.Context(), &req)
	if err != nil {
		respondPluginServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": toPluginResponse(row)})
}

// Uninstall godoc
// @Summary      Uninstall a plugin
// @Tags         Plugin
// @Produce      json
// @Param        id   path      string  true  "Plugin ID"
// @Success      200  {object}  map[string]interface{}  "Uninstalled"
// @Failure      403  {object}  apperrors.AppError      "Built-in extension"
// @Failure      404  {object}  apperrors.AppError      "Plugin not found"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /plugins/{id} [delete]
func (h *PluginHandler) Uninstall(c *gin.Context) {
	if h.bundleRow(c) {
		h.uninstallBundle(c)
		return
	}
	if err := h.service.Uninstall(c.Request.Context(), pluginScope(c), c.Param("id")); err != nil {
		respondPluginServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// Enable godoc
// @Summary      Enable a plugin
// @Tags         Plugin
// @Produce      json
// @Param        id   path      string  true  "Plugin ID"
// @Success      200  {object}  map[string]interface{}  "Enabled plugin"
// @Failure      404  {object}  apperrors.AppError      "Plugin not found"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /plugins/{id}/enable [post]
func (h *PluginHandler) Enable(c *gin.Context) {
	h.setEnabled(c, true)
}

// Disable godoc
// @Summary      Disable a plugin
// @Description  Unregisters the plugin from the host; it is not loaded again until enabled.
// @Tags         Plugin
// @Produce      json
// @Param        id   path      string  true  "Plugin ID"
// @Success      200  {object}  map[string]interface{}  "Disabled plugin"
// @Failure      404  {object}  apperrors.AppError      "Plugin not found"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /plugins/{id}/disable [post]
func (h *PluginHandler) Disable(c *gin.Context) {
	h.setEnabled(c, false)
}

func (h *PluginHandler) setEnabled(c *gin.Context, enabled bool) {
	if h.bundleRow(c) {
		h.setBundleEnabled(c, enabled)
		return
	}
	row, err := h.service.SetEnabled(c.Request.Context(), pluginScope(c), c.Param("id"), enabled)
	if err != nil {
		respondPluginServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": toPluginResponse(row)})
}

// Reconnect godoc
// @Summary      Move a plugin to a new address
// @Description  Repoints a loaded plugin. The stored address is the host's normalized form, which may differ from the one sent.
// @Tags         Plugin
// @Accept       json
// @Produce      json
// @Param        id       path      string                   true  "Plugin ID"
// @Param        request  body      object{endpoint=string}  true  "New endpoint"
// @Success      200      {object}  map[string]interface{}   "Repointed plugin"
// @Failure      400      {object}  apperrors.AppError       "Invalid endpoint"
// @Failure      404      {object}  apperrors.AppError       "Plugin not found"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /plugins/{id}/reconnect [post]
func (h *PluginHandler) Reconnect(c *gin.Context) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(apperrors.NewBadRequestError("invalid reconnect body"))
		return
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if endpoint == "" {
		_ = c.Error(apperrors.NewBadRequestError("endpoint is required"))
		return
	}
	row, err := h.service.Repoint(c.Request.Context(), pluginScope(c), c.Param("id"), endpoint)
	if err != nil {
		respondPluginServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": toPluginResponse(row)})
}

// SetEnvs godoc
// @Summary      Replace a plugin's credentials
// @Description  Write-only: no endpoint returns the stored values, and errors name the field, never the value.
// @Tags         Plugin
// @Accept       json
// @Produce      json
// @Param        id       path      string                              true  "Plugin ID"
// @Param        request  body      object{envs=map[string]string}      true  "Environment variables"
// @Success      200      {object}  map[string]interface{}              "Stored key names"
// @Failure      400      {object}  apperrors.AppError                  "Invalid request"
// @Failure      404      {object}  apperrors.AppError                  "Plugin not found"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /plugins/{id}/envs [put]
func (h *PluginHandler) SetEnvs(c *gin.Context) {
	var req struct {
		Envs map[string]string `json:"envs"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(apperrors.NewBadRequestError("invalid envs body"))
		return
	}
	envs := make(map[string]string, len(req.Envs))
	for k, v := range req.Envs {
		key := strings.TrimSpace(k)
		if key == "" {
			_ = c.Error(apperrors.NewBadRequestError("env name must not be empty"))
			return
		}
		envs[key] = v
	}
	row, err := h.service.SetEnvs(c.Request.Context(), pluginScope(c), c.Param("id"), envs)
	if err != nil {
		respondPluginServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": toPluginResponse(row)})
}
