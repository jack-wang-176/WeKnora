package handler

import (
	"context"
	stderrors "errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	pluginEventPollInterval = 2 * time.Second
	pluginEventMaxDuration  = 30 * time.Minute
)

// bundlePluginService is the `bundle` channel's half of the plugin surface. It
// is a dependency of its own rather than part of interfaces.TenantPluginService
// because the two channels are two services, and a row belongs to exactly one.
type bundlePluginService interface {
	InstallPlugin(ctx context.Context, req *types.PluginInstallRequest) (*types.TenantPlugin, error)
	UninstallPlugin(ctx context.Context, tenantID *uint64, pluginID string) error
	EnablePlugin(ctx context.Context, tenantID *uint64, pluginID string) (*types.TenantPlugin, error)
	DisablePlugin(ctx context.Context, tenantID *uint64, pluginID string) (*types.TenantPlugin, error)
	GetPlugin(ctx context.Context, tenantID *uint64, pluginID string) (*types.TenantPlugin, error)
	LastProgress(ctx context.Context, tenantID *uint64, pluginID string) (service.PluginProgress, bool)
	SubscribeProgress(
		ctx context.Context, tenantID *uint64, pluginID string,
	) (<-chan service.PluginProgress, func(), error)
}

// Install godoc
// @Summary      Install a plugin from an image
// @Description  Starts a bundle install and returns the claimed row. The image
// @Description  pull outlives the request, so the status is followed through
// @Description  the events stream or by re-reading the plugin.
// @Tags         Plugin
// @Accept       json
// @Produce      json
// @Param        request  body      types.PluginInstallRequest  true  "Install request"
// @Success      202      {object}  pluginResponse
// @Failure      400      {object}  apperrors.AppError
// @Failure      409      {object}  apperrors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /plugins/install [post]
func (h *PluginHandler) Install(c *gin.Context) {
	if h.bundle == nil {
		_ = c.Error(apperrors.NewBadRequestError("the bundle channel is not enabled on this deployment"))
		return
	}
	var req types.PluginInstallRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	// The route decides the scope, never the body: a tenant-scoped request that
	// could name tenant_id would be installing into somebody else's namespace.
	req.TenantID = pluginScope(c)
	row, err := h.bundle.InstallPlugin(c.Request.Context(), &req)
	if err != nil {
		respondPluginServiceError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, toPluginResponse(row))
}

// bundleRow reports whether :id names a bundle plugin, so the lifecycle routes
// the two channels share can hand the request to the right one. A read failure
// or a missing row is not a bundle row — the endpoint channel reports those in
// its own words.
func (h *PluginHandler) bundleRow(c *gin.Context) bool {
	if h.bundle == nil {
		return false
	}
	row, err := h.bundle.GetPlugin(c.Request.Context(), pluginScope(c), c.Param("id"))
	return err == nil && row != nil && row.Channel == types.PluginChannelBundle
}

// uninstallBundle answers 202: unregistering, stopping the container and
// dropping the whitelist entry all happen after the reply.
func (h *PluginHandler) uninstallBundle(c *gin.Context) {
	if err := h.bundle.UninstallPlugin(c.Request.Context(), pluginScope(c), c.Param("id")); err != nil {
		respondPluginServiceError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true})
}

// setBundleEnabled mirrors the asymmetry in the service: enable starts a
// container and answers 202, disable has finished by the time it returns.
func (h *PluginHandler) setBundleEnabled(c *gin.Context, enabled bool) {
	ctx, scope, id := c.Request.Context(), pluginScope(c), c.Param("id")
	var (
		row  *types.TenantPlugin
		err  error
		code = http.StatusOK
	)
	if enabled {
		row, err = h.bundle.EnablePlugin(ctx, scope, id)
		code = http.StatusAccepted
	} else {
		row, err = h.bundle.DisablePlugin(ctx, scope, id)
	}
	if err != nil {
		respondPluginServiceError(c, err)
		return
	}
	c.JSON(code, toPluginResponse(row))
}

type pluginInstallEvent struct {
	Percent int    `json:"percent"`
	Stage   string `json:"stage"`
	Log     string `json:"log,omitempty"`
	Status  string `json:"status,omitempty"`
	Done    bool   `json:"done"`
}

// Terminal stages, as published by the service when a run ends.
const (
	pluginStageReady   = "ready"
	pluginStageFailed  = "failed"
	pluginStageRemoved = "removed"
	// pluginStageDetached is the handler's own: the stream stopped following a
	// run that is still going. It is not a verdict on the install.
	pluginStageDetached = "detached"
)

func pluginEventFromProgress(p service.PluginProgress) pluginInstallEvent {
	return pluginInstallEvent{
		Percent: p.Percent,
		Stage:   p.Stage,
		Log:     p.Log,
		Status:  p.Status,
		Done: p.Stage == pluginStageReady ||
			p.Stage == pluginStageFailed ||
			p.Stage == pluginStageRemoved,
	}
}

// terminalPluginEvent derives an end-of-stream frame from the durable row, for
// every run that ends without publishing one: a run whose process died
// publishes nothing ever again. A nil row is a finished removal — the last step
// of one deletes it.
func terminalPluginEvent(row *types.TenantPlugin) (pluginInstallEvent, bool) {
	if row == nil {
		return pluginInstallEvent{
			Percent: 100, Stage: pluginStageRemoved, Status: pluginStageRemoved, Done: true,
		}, true
	}
	switch row.Status {
	case types.PluginStatusInstalling, types.PluginStatusRemoving:
		return pluginInstallEvent{}, false
	case types.PluginStatusFailed:
		return pluginInstallEvent{
			Percent: 100, Stage: pluginStageFailed, Status: row.Status,
			Log: derefStringValue(row.Error), Done: true,
		}, true
	default:
		return pluginInstallEvent{
			Percent: 100, Stage: pluginStageReady, Status: row.Status, Done: true,
		}, true
	}
}

// InstallEvents godoc
// @Summary      Follow an install or removal
// @Description  Server-sent progress for one plugin run. The stream always
// @Description  terminates: with the run's own terminal event, with one derived
// @Description  from the durable status, or with a "detached" frame when it
// @Description  stops following a run that is still going.
// @Tags         Plugin
// @Produce      text/event-stream
// @Param        id   path      string  true  "Plugin ID"
// @Success      200  {string}  string  "SSE stream of progress events"
// @Failure      404  {object}  apperrors.AppError
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /plugins/{id}/events [get]
func (h *PluginHandler) InstallEvents(c *gin.Context) {
	ctx := c.Request.Context()
	scope, id := pluginScope(c), c.Param("id")
	if h.bundle == nil {
		_ = c.Error(apperrors.NewNotFoundError(id + ": no install to follow"))
		return
	}

	// The progress key is already tenant-scoped, so this read is not what
	// isolates the stream; it is what turns "not yours" into a 404 while a
	// refusal can still be rendered as JSON, before any SSE header is written.
	row, err := h.bundle.GetPlugin(ctx, scope, id)
	if err != nil {
		respondPluginServiceError(c, err)
		return
	}

	// Subscribing before the first read means an event published between the
	// two is delivered rather than missed.
	events, release, err := h.bundle.SubscribeProgress(ctx, scope, id)
	if err != nil {
		_ = c.Error(err)
		return
	}
	defer release()

	setPluginSSEHeaders(c)

	// lastPercent is what a detached frame reports, so giving up on following a
	// run does not appear to reset its progress.
	lastPercent := 0
	if last, ok := h.bundle.LastProgress(ctx, scope, id); ok {
		event := pluginEventFromProgress(last)
		lastPercent = event.Percent
		if !emitPluginEvent(c, event) || event.Done {
			return
		}
	}
	if terminal, ok := terminalPluginEvent(row); ok {
		emitPluginEvent(c, terminal)
		return
	}
	if events == nil {
		// Nothing publishes progress without Redis. One frame stating the
		// durable status is all this connection can ever say.
		emitPluginEvent(c, pluginInstallEvent{
			Stage:  row.Status,
			Status: row.Status,
			Log:    "live progress is unavailable; poll the plugin for its status",
			Done:   true,
		})
		return
	}

	poll := time.NewTicker(pluginEventPollInterval)
	defer poll.Stop()
	deadline := time.NewTimer(pluginEventMaxDuration)
	defer deadline.Stop()

	for {
		select {
		case <-ctx.Done():
			// The client is gone. The install keeps running; only this view of
			// it ends.
			return
		case <-deadline.C:
			emitPluginEvent(c, pluginInstallEvent{
				Percent: lastPercent, Stage: pluginStageDetached, Done: true,
			})
			return
		case p, ok := <-events:
			if !ok {
				// The subscription ended under us. The poll below is the only
				// source left, so keep the connection until it reaches a
				// terminal state or the cap expires.
				events = nil
				continue
			}
			event := pluginEventFromProgress(p)
			lastPercent = event.Percent
			if !emitPluginEvent(c, event) || event.Done {
				return
			}
		case <-poll.C:
			current, err := h.bundle.GetPlugin(ctx, scope, id)
			if err != nil {
				// A deleted row is a finished removal, which has no publisher
				// left; anything else is a blip worth one more keepalive.
				if terminal, ok := terminalPluginEvent(nil); ok && stderrors.Is(err, service.ErrPluginNotFound) {
					emitPluginEvent(c, terminal)
					return
				}
				logger.Warnf(ctx, "[PluginBundle] re-read %s while streaming failed: %v", id, err)
				if !emitPluginComment(c) {
					return
				}
				continue
			}
			if terminal, ok := terminalPluginEvent(current); ok {
				emitPluginEvent(c, terminal)
				return
			}
			if !emitPluginComment(c) {
				return
			}
		}
	}
}

func emitPluginEvent(c *gin.Context, event pluginInstallEvent) bool {
	c.SSEvent("message", event)
	c.Writer.Flush()
	return c.Request.Context().Err() == nil
}

func emitPluginComment(c *gin.Context) bool {
	if _, err := c.Writer.WriteString(": keep-alive\n\n"); err != nil {
		return false
	}
	c.Writer.Flush()
	return c.Request.Context().Err() == nil
}

// setPluginSSEHeaders mirrors the skill stream's preamble; X-Accel-Buffering is
// what stops nginx from holding progress frames back.
func setPluginSSEHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
}
