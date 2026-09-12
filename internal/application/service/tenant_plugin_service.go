package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/robfig/cron/v3"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/common/redislock"
	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/pluginruntime"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	// installHeartbeat refreshes installing_since while the flow is alive, so
	// the reaper's threshold is a silence budget rather than a duration budget:
	// a ten-minute image pull that keeps beating is healthy, a two-minute one
	// that stopped beating is dead.
	pluginInstallHeartbeat = 30 * time.Second
	// pluginInstallSilenceTTL is far below the skill side's hour: the heartbeat
	// ticks from its own goroutine, so even the image pull never goes quiet
	// for this long while the install is alive.
	pluginInstallSilenceTTL = 10 * time.Minute
	pluginInstallBudget     = 30 * time.Minute

	pluginInstallLockLease = 30 * time.Second
	pluginInstallLockRenew = 10 * time.Second

	// pluginCleanupBudget bounds each cleanup step separately: the cleanup
	// context outlives the request on purpose and therefore carries no deadline
	// of its own.
	pluginCleanupBudget = 30 * time.Second
	pluginProbeBudget   = 30 * time.Second

	pluginInstallInterruptedMessage = "装载进程中断: the process died before the install finished"
)

// TenantPluginService owns the `bundle` channel: plugins whose container this
// process starts and stops. The `endpoint` channel (plugin_service.go) stays
// separate — it registers an address someone else runs, so it has no image, no
// container and no whitelist entry to maintain.
type TenantPluginService struct {
	plugins  repository.TenantPluginRepository
	runtime  pluginruntime.Runtime
	host     extension.Host
	settings interfaces.SystemSettingService
	redis    *redis.Client

	now              func() time.Time
	installHeartbeat time.Duration

	// localLocks serialises installs when Redis is absent. It guards this
	// process only; a multi-replica deployment needs Redis.
	localLocks *keyedMutex

	cron    *cron.Cron
	cronMu  sync.Mutex
	started bool
}

func NewTenantPluginService(
	plugins repository.TenantPluginRepository,
	runtime pluginruntime.Runtime,
	host extension.Host,
	settings interfaces.SystemSettingService,
	redisClient *redis.Client,
) *TenantPluginService {
	return &TenantPluginService{
		plugins:          plugins,
		runtime:          runtime,
		host:             host,
		settings:         settings,
		redis:            redisClient,
		now:              time.Now,
		installHeartbeat: pluginInstallHeartbeat,
		localLocks:       newKeyedMutex(),
	}
}

func (s *TenantPluginService) clock() func() time.Time {
	if s != nil && s.now != nil {
		return s.now
	}
	return time.Now
}

func (s *TenantPluginService) timestamp() time.Time {
	return s.clock()().UTC().Truncate(time.Microsecond)
}

// withInstallLock makes "read the row, decide, write the row" atomic across
// replicas. It deliberately does not cover the whole install: a lease renewed
// across a multi-minute image pull buys nothing that the installing status and
// its heartbeat do not already provide.
func (s *TenantPluginService) withInstallLock(
	ctx context.Context, tenantID *uint64, pluginID string, fn func(context.Context) error,
) error {
	key := "weknora-plugin-install-lock:" + pluginLockKey(tenantID, pluginID)
	if s.redis == nil {
		release, err := s.localLocks.lock(ctx, key)
		if err != nil {
			return err
		}
		defer release()
		return fn(ctx)
	}
	return redislock.WithRenewableLock(
		ctx, s.redis, key, pluginInstallLockLease, pluginInstallLockRenew, fn,
	)
}

// installMark is the "is this row still mine" token. The install goroutine
// writes installing_since and remembers what it wrote; the heartbeat updates
// both together. A row whose installing_since is not the remembered value was
// taken over by a newer install, a reaper, or an uninstall.
type installMark struct {
	mu sync.Mutex
	at time.Time
}

func (m *installMark) set(t time.Time) {
	m.mu.Lock()
	m.at = t
	m.mu.Unlock()
}

func (m *installMark) get() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.at
}

// stillOwns answers what every terminal write must ask first: has anything taken
// this row away from me since I last wrote it? Without it, a goroutine the
// reaper gave up on can resurrect a ready row whose container is gone.
//
// row.Status is the phase the goroutine started in; ownership ends when the
// stored row leaves it.
func (s *TenantPluginService) stillOwns(
	ctx context.Context, row *types.TenantPlugin, mark *installMark,
) (*types.TenantPlugin, bool) {
	current, err := s.plugins.GetPlugin(ctx, row.TenantID, row.ID)
	if err != nil {
		// Unknown is not "mine": acting on a failed read is how a transient DB
		// blip turns into a wrong terminal state.
		logger.Warnf(ctx, "[PluginBundle] %s: ownership re-read failed: %v", row.PluginID, err)
		return nil, false
	}
	if current == nil || current.Status != row.Status {
		return nil, false
	}
	if current.InstallingSince == nil || !current.InstallingSince.Equal(mark.get()) {
		return nil, false
	}
	return current, true
}

// startHeartbeat refreshes installing_since until the returned function is
// called. Forgetting to stop it keeps a finished install looking alive forever,
// so every caller defers the stop on the line after the call.
func (s *TenantPluginService) startHeartbeat(
	ctx context.Context, row *types.TenantPlugin, mark *installMark,
) func() {
	interval := s.installHeartbeat
	if interval <= 0 {
		interval = pluginInstallHeartbeat
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				s.beat(ctx, row, mark)
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

func (s *TenantPluginService) beat(ctx context.Context, row *types.TenantPlugin, mark *installMark) {
	current, ok := s.stillOwns(ctx, row, mark)
	if !ok {
		return
	}
	at := s.timestamp()
	current.InstallingSince = &at
	if err := s.plugins.UpdatePlugin(ctx, current); err != nil {
		logger.Warnf(ctx, "[PluginBundle] %s: heartbeat failed: %v", row.PluginID, err)
		return
	}
	mark.set(at)
}

// specFor builds the runtime's view of a row. pluginruntime takes this local
// struct rather than the row so that it never learns about the plugin table.
func specFor(row *types.TenantPlugin) (pluginruntime.Spec, error) {
	envs, err := envMap(row.Envs)
	if err != nil {
		return pluginruntime.Spec{}, fmt.Errorf("envs: %w", err)
	}
	var tenant uint64
	if row.TenantID != nil {
		tenant = *row.TenantID
	}
	return pluginruntime.Spec{
		ID:       row.PluginID,
		TenantID: tenant,
		ImageRef: derefString(row.ImageRef),
		Policy:   row.PolicyClass,
		Envs:     envs,
	}, nil
}

// addWhitelist / removeWhitelist keep the runtime-owned half of the SSRF
// whitelist in step with the containers. Both go through SystemSettingService
// so the change reaches the in-process cache and the other replicas; writing
// the repository directly would leave peers dialling a name they still refuse.
func (s *TenantPluginService) addWhitelist(ctx context.Context, containerName string) error {
	if s.settings == nil || containerName == "" {
		return nil
	}
	return s.settings.AddPluginSSRFWhitelistEntries(ctx, containerName)
}

func (s *TenantPluginService) removeWhitelist(ctx context.Context, containerName string) error {
	if s.settings == nil || containerName == "" {
		return nil
	}
	return s.settings.RemovePluginSSRFWhitelistEntries(ctx, containerName)
}

// cleanupContext gives cleanup its own budget on a context that outlives the
// request: WithoutCancel keeps the trace and tenant values but drops the
// deadline, so without the timeout cleanup would run unbounded.
func cleanupContext(base context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(base), pluginCleanupBudget)
}
