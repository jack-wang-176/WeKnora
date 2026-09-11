package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	pluginProgressTTL    = 30 * time.Minute
	pluginProgressBuffer = 16
)

// PluginProgress is transient install state. It is not persisted: the durable
// answer is tenant_plugins.status, and a percentage that outlives the run that
// produced it is worse than no percentage.
type PluginProgress struct {
	Percent int    `json:"percent"`
	Stage   string `json:"stage"`
	Log     string `json:"log,omitempty"`
	Status  string `json:"status,omitempty"`
}

// pluginProgressKey carries the tenant because that is the only thing stopping
// one workspace from reading another's install log, which names source
// addresses and error detail.
func pluginProgressKey(tenantID *uint64, pluginID string) string {
	if tenantID == nil {
		return "weknora-plugin-install:global:" + pluginID
	}
	return fmt.Sprintf("weknora-plugin-install:%d:%s", *tenantID, pluginID)
}

// publishProgress is a no-op without Redis, so whether progress is visible is a
// deployment difference and never an install failure.
func (s *TenantPluginService) publishProgress(
	ctx context.Context, row *types.TenantPlugin, p PluginProgress,
) {
	if s.redis == nil || row == nil {
		return
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return
	}
	key := pluginProgressKey(row.TenantID, row.PluginID)
	if err := s.redis.Set(ctx, key, payload, pluginProgressTTL).Err(); err != nil {
		logger.Warnf(ctx, "[PluginBundle] store progress %s failed: %v", key, err)
	}
	if err := s.redis.Publish(ctx, key, payload).Err(); err != nil {
		logger.Warnf(ctx, "[PluginBundle] publish progress %s failed: %v", key, err)
	}
}

// LastProgress lets a stream that connects late paint immediately instead of
// waiting for the next event.
func (s *TenantPluginService) LastProgress(
	ctx context.Context, tenantID *uint64, pluginID string,
) (PluginProgress, bool) {
	if s.redis == nil {
		return PluginProgress{}, false
	}
	raw, err := s.redis.Get(ctx, pluginProgressKey(tenantID, pluginID)).Bytes()
	if err != nil {
		return PluginProgress{}, false
	}
	var p PluginProgress
	if err := json.Unmarshal(raw, &p); err != nil {
		return PluginProgress{}, false
	}
	return p, true
}

// SubscribeProgress returns a nil channel without Redis; callers send one frame
// and close rather than holding a stream that can never produce an event.
func (s *TenantPluginService) SubscribeProgress(
	ctx context.Context, tenantID *uint64, pluginID string,
) (<-chan PluginProgress, func(), error) {
	if s.redis == nil {
		return nil, func() {}, nil
	}
	sub := s.redis.Subscribe(ctx, pluginProgressKey(tenantID, pluginID))
	out := make(chan PluginProgress, pluginProgressBuffer)
	go func() {
		defer close(out)
		for msg := range sub.Channel() {
			var p PluginProgress
			if err := json.Unmarshal([]byte(msg.Payload), &p); err != nil {
				continue
			}
			select {
			case out <- p:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, func() { _ = sub.Close() }, nil
}
