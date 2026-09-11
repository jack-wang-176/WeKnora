package service

import (
	"context"
	"strconv"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/pluginruntime"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	pluginReaperCronSpec = "0 */5 * * * *"
	// pluginOrphanGrace covers the gap between docker creating the container
	// and the install storing its name: a container younger than this may
	// belong to a run that simply has not written the row yet.
	pluginOrphanGrace = 5 * time.Minute
)

// pluginReaperStore is the row slice the sweeps need.
type pluginReaperStore interface {
	ListStaleInstalling(ctx context.Context, olderThan time.Time) ([]*types.TenantPlugin, error)
	ListReadyPlugins(ctx context.Context) ([]*types.TenantPlugin, error)
	GetPluginByPluginID(ctx context.Context, tenantID *uint64, pluginID string) (*types.TenantPlugin, error)
	UpdatePlugin(ctx context.Context, p *types.TenantPlugin) error
	UpdatePluginState(ctx context.Context, id string, status string, errMsg *string) error
}

// pluginContainerReaper is the runtime half. List and Stop only: reconciliation
// must never be able to start a container as a side effect.
type pluginContainerReaper interface {
	List(ctx context.Context) ([]pluginruntime.Instance, error)
	Stop(ctx context.Context, containerName string) error
}

var (
	_ pluginReaperStore     = (repository.TenantPluginRepository)(nil)
	_ pluginContainerReaper = (pluginruntime.Runtime)(nil)
)

// StartReaper arms the five-minute sweep. Idempotent: the container invokes it
// once, but a second call must not add a second schedule.
func (s *TenantPluginService) StartReaper(ctx context.Context) error {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()
	if s.started {
		return nil
	}
	if s.cron == nil {
		s.cron = cron.New(cron.WithSeconds(), cron.WithChain(
			cron.Recover(cron.DefaultLogger),
		))
	}
	if _, err := s.cron.AddFunc(pluginReaperCronSpec, func() {
		s.Reconcile(context.Background())
	}); err != nil {
		return err
	}
	s.cron.Start()
	s.started = true
	logger.Infof(ctx, "[PluginReaper] started with 5-minute sweep")
	return nil
}

// StopReaper halts the cron and waits for an in-flight sweep to finish.
func (s *TenantPluginService) StopReaper() {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()
	if !s.started {
		return
	}
	c := s.cron.Stop()
	<-c.Done()
	s.started = false
}

// Reconcile is the whole sweep. Each step is independent and each skips itself
// on a read failure: a round that does nothing is always better than a round
// that acts on a stale picture.
func (s *TenantPluginService) Reconcile(ctx context.Context) {
	s.reapStuckRuns(ctx)
	s.reconcileContainers(ctx)
	s.reconcileRegistrations(ctx)
}

// reapStuckRuns closes out rows whose goroutine stopped beating.
func (s *TenantPluginService) reapStuckRuns(ctx context.Context) {
	cutoff := s.clock()().UTC().Add(-pluginInstallSilenceTTL)
	rows, err := s.plugins.ListStaleInstalling(ctx, cutoff)
	if err != nil {
		logger.Warnf(ctx, "[PluginReaper] stale listing failed, skipping this round: %v", err)
		return
	}
	for _, row := range rows {
		switch row.Status {
		case types.PluginStatusRemoving:
			// A teardown that stopped beating. Re-stamp first so the retry owns
			// the row, then run the same teardown again — it is idempotent.
			now := s.clock()().UTC()
			row.InstallingSince = &now
			if err := s.plugins.UpdatePlugin(ctx, row); err != nil {
				logger.Warnf(ctx, "[PluginReaper] %s: re-claim for removal failed: %v", row.PluginID, err)
				continue
			}
			mark := &installMark{}
			mark.set(now)
			go s.runRemove(context.WithoutCancel(ctx), row, mark)
		case types.PluginStatusInstalling:
			// Ask the host before declaring it dead: an install whose only lost
			// write was the final one is already serving, and marking it failed
			// would have the container sweep delete a working container.
			if _, ok := s.host.Get(row.PluginID); ok {
				if err := s.plugins.UpdatePluginState(ctx, row.ID, types.PluginStatusReady, nil); err != nil {
					logger.Warnf(ctx, "[PluginReaper] %s: mark ready failed: %v", row.PluginID, err)
				}
				continue
			}
			msg := pluginInstallInterruptedMessage
			if err := s.plugins.UpdatePluginState(ctx, row.ID, types.PluginStatusFailed, &msg); err != nil {
				logger.Warnf(ctx, "[PluginReaper] %s: mark failed: %v", row.PluginID, err)
			}
		}
	}
}

// reconcileContainers is one pass over the runtime listing, answering both
// questions that listing can answer: which containers no longer have a row,
// and which whitelist entries no longer have a container.
func (s *TenantPluginService) reconcileContainers(ctx context.Context) {
	// Containers first, DB second. Reading the DB first and docker second
	// misses everything created in between, and that gap is what leaks.
	instances, err := s.runtime.List(ctx)
	if err != nil {
		logger.Warnf(ctx, "[PluginReaper] container listing failed, skipping this round: %v", err)
		return
	}
	grace := s.clock()().UTC().Add(-pluginOrphanGrace)
	live := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		name := inst.ContainerName
		if name == "" {
			continue
		}
		row, err := s.plugins.GetPluginByPluginID(ctx, tenantPtr(inst.TenantID), inst.PluginID)
		if err != nil {
			// Unsure means keep: a DB blip must not cost a serving container.
			logger.Warnf(ctx, "[PluginReaper] %s: row lookup failed, keeping the container: %v", name, err)
			live[name] = struct{}{}
			continue
		}
		if (row != nil && derefString(row.ContainerName) == name) ||
			(!inst.CreatedAt.IsZero() && inst.CreatedAt.After(grace)) {
			live[name] = struct{}{}
			continue
		}
		logger.Infof(ctx, "[PluginReaper] stopping orphan container %s", name)
		if err := s.runtime.Stop(ctx, name); err != nil {
			logger.Warnf(ctx, "[PluginReaper] %s: stop failed: %v", name, err)
		}
		if err := s.removeWhitelist(ctx, name); err != nil {
			logger.Warnf(ctx, "[PluginReaper] %s: whitelist removal failed: %v", name, err)
		}
	}
	s.reconcileWhitelist(ctx, live)
}

// reconcileWhitelist makes the runtime-owned half of the SSRF whitelist equal
// the set of live containers. It is two-way because either side can drift: a
// crashed install leaves an entry behind, a restored database loses one.
func (s *TenantPluginService) reconcileWhitelist(ctx context.Context, live map[string]struct{}) {
	if s.settings == nil {
		return
	}
	present := make(map[string]struct{})
	var drop []string
	for _, entry := range s.settings.PluginSSRFWhitelistEntries(ctx) {
		present[entry] = struct{}{}
		if _, ok := live[entry]; !ok {
			drop = append(drop, entry)
		}
	}
	var add []string
	for name := range live {
		if _, ok := present[name]; !ok {
			add = append(add, name)
		}
	}
	if len(add) > 0 {
		if err := s.settings.AddPluginSSRFWhitelistEntries(ctx, add...); err != nil {
			logger.Warnf(ctx, "[PluginReaper] whitelist add failed: %v", err)
		}
	}
	if len(drop) > 0 {
		if err := s.settings.RemovePluginSSRFWhitelistEntries(ctx, drop...); err != nil {
			logger.Warnf(ctx, "[PluginReaper] whitelist removal failed: %v", err)
		}
	}
}

// reconcileRegistrations makes the host's map agree with the rows. The row is
// the truth: it survives a restart, the map does not.
func (s *TenantPluginService) reconcileRegistrations(ctx context.Context) {
	rows, err := s.plugins.ListReadyPlugins(ctx)
	if err != nil {
		logger.Warnf(ctx, "[PluginReaper] ready listing failed, skipping this round: %v", err)
		return
	}
	// Both channels' ready rows, because both register into the same host and
	// this sweep must not unregister the other channel's plugins.
	serving := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		serving[row.PluginID] = struct{}{}
	}
	for _, row := range rows {
		if row.Channel != types.PluginChannelBundle {
			continue
		}
		m, ok := s.host.Get(row.PluginID)
		if ok && m.Runtime.Endpoint == derefString(row.Endpoint) {
			continue
		}
		if err := activatePluginRow(ctx, s.plugins, s.host, row); err != nil {
			logger.Warnf(ctx, "[PluginReaper] %s: re-register failed: %v", row.PluginID, err)
		}
	}

	for _, kind := range []extension.Kind{
		extension.KindDocParser, extension.KindWebSearch, extension.KindDatasource,
	} {
		for _, m := range s.host.List(kind) {
			id := m.Metadata.ID
			// Builtins and file-backed manifests are not this sweep's to hold
			// an opinion about: neither has a row to compare against.
			if m.Builtin || m.Dir != "" {
				continue
			}
			if _, ok := serving[id]; ok {
				continue
			}
			row, err := s.plugins.GetPluginByPluginID(ctx, tenantFromScopedID(id), id)
			if err != nil {
				logger.Warnf(ctx, "[PluginReaper] %s: row lookup failed, keeping the registration: %v", id, err)
				continue
			}
			// Any row at all means some flow owns this id right now — an
			// upgrade in flight, a failed row about to be retried. Only a
			// registration with no row behind it is safe to drop.
			if row != nil {
				continue
			}
			logger.Infof(ctx, "[PluginReaper] unregistering %s: no row behind it", id)
			if _, err := s.host.Unregister(ctx, id); err != nil {
				logger.Warnf(ctx, "[PluginReaper] %s: unregister failed: %v", id, err)
			}
		}
	}
}

func tenantPtr(tenantID uint64) *uint64 {
	if tenantID == 0 {
		return nil
	}
	return &tenantID
}

// tenantFromScopedID reads the tenant back out of a stored id. A malformed or
// absent suffix means process scope, which is what nil selects.
func tenantFromScopedID(id string) *uint64 {
	_, owner := extension.SplitID(id)
	if owner == "" {
		return nil
	}
	tenant, err := strconv.ParseUint(owner, 10, 64)
	if err != nil {
		return nil
	}
	return &tenant
}
