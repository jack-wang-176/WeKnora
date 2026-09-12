package service

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// Teardown and the enable/disable pair. Uninstall is the install run backwards;
// disable stops at the container and keeps the row.

// lockBase derives the install lock key's id from a stored row. The lock is
// keyed on the unscoped id because that is what InstallPlugin locks on.
func lockBase(row *types.TenantPlugin) string {
	base, _ := extension.SplitID(row.PluginID)
	return base
}

// GetPlugin is the plain read, channel included, so a caller serving both
// channels can decide which one owns the row before acting on it.
func (s *TenantPluginService) GetPlugin(
	ctx context.Context, tenantID *uint64, pluginID string,
) (*types.TenantPlugin, error) {
	row, err := s.plugins.GetPluginByPluginID(ctx, tenantID, pluginID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("%s: %w", pluginID, ErrPluginNotFound)
	}
	return row, nil
}

// loadBundleRow reads the row and refuses the rows this service does not own.
func (s *TenantPluginService) loadBundleRow(
	ctx context.Context, tenantID *uint64, pluginID string,
) (*types.TenantPlugin, error) {
	row, err := s.GetPlugin(ctx, tenantID, pluginID)
	if err != nil {
		return nil, err
	}
	if row.Channel != types.PluginChannelBundle {
		return nil, fmt.Errorf("%w: %s belongs to the %s channel",
			ErrPluginInvalid, row.PluginID, row.Channel)
	}
	return row, nil
}

// UninstallPlugin marks the row and returns; the teardown runs in the
// background for the same reason the install does.
func (s *TenantPluginService) UninstallPlugin(ctx context.Context, tenantID *uint64, pluginID string) error {
	pre, err := s.loadBundleRow(ctx, tenantID, pluginID)
	if err != nil {
		return err
	}
	now := s.timestamp()
	var row *types.TenantPlugin
	mark := &installMark{}
	err = s.withInstallLock(ctx, tenantID, lockBase(pre), func(ctx context.Context) error {
		current, err := s.loadBundleRow(ctx, tenantID, pluginID)
		if err != nil {
			return err
		}
		if current.Status == types.PluginStatusRemoving {
			// canTransition treats removing -> removing as a no-op edge; saying
			// yes twice would run two teardowns over the same container.
			return fmt.Errorf("%w: %s is already being uninstalled", ErrPluginInvalid, current.PluginID)
		}
		if err := canTransition(current.Status, types.PluginStatusRemoving); err != nil {
			return err
		}
		current.Status = types.PluginStatusRemoving
		// installing_since doubles as the removal's liveness mark, which is why
		// ListStaleInstalling covers removing as well.
		current.InstallingSince = &now
		current.Error = nil
		if err := s.plugins.UpdatePlugin(ctx, current); err != nil {
			return err
		}
		row = current
		mark.set(now)
		return nil
	})
	if err != nil {
		return err
	}
	go s.runRemove(context.WithoutCancel(ctx), row, mark)
	return nil
}

// runRemove undoes the install in reverse. Deletion inverts the usual ordering
// — memory first, the row last — because the row is the only record of what is
// still to be cleaned up. Every step logs and continues: a step that did not
// land leaves an orphan, and reconciliation knows how to claim orphans.
func (s *TenantPluginService) runRemove(base context.Context, row *types.TenantPlugin, mark *installMark) {
	ctx, cancel := context.WithTimeout(base, pluginInstallBudget)
	defer cancel()
	stopHeartbeat := s.startHeartbeat(ctx, row, mark)
	defer stopHeartbeat()

	if _, err := s.host.Unregister(ctx, row.PluginID); err != nil {
		logger.Warnf(ctx, "[PluginBundle] %s: unregister failed: %v", row.PluginID, err)
	}
	if name := derefString(row.ContainerName); name != "" {
		if err := s.runtime.Stop(ctx, name); err != nil {
			logger.Warnf(ctx, "[PluginBundle] %s: stop failed: %v", row.PluginID, err)
		}
		if err := s.removeWhitelist(ctx, name); err != nil {
			logger.Warnf(ctx, "[PluginBundle] %s: whitelist removal failed: %v", row.PluginID, err)
		}
	}
	// The image stays: it is shared with every other tenant running the same
	// plugin, and pulling it again is the cheapest thing in this flow.

	if _, ok := s.stillOwns(ctx, row, mark); !ok {
		return
	}
	if err := s.plugins.DeletePlugin(ctx, row.TenantID, row.ID); err != nil {
		logger.Errorf(ctx, "[PluginBundle] %s: delete row failed: %v", row.PluginID, err)
		return
	}
	s.publishProgress(ctx, row, PluginProgress{Percent: 100, Stage: "removed"})
}

// DisablePlugin stops the container and keeps the row. It is synchronous
// because stopping is fast; only the first step can fail the request, since
// after the row says disabled the rest is cleanup reconciliation also does.
func (s *TenantPluginService) DisablePlugin(
	ctx context.Context, tenantID *uint64, pluginID string,
) (*types.TenantPlugin, error) {
	pre, err := s.loadBundleRow(ctx, tenantID, pluginID)
	if err != nil {
		return nil, err
	}
	var row *types.TenantPlugin
	err = s.withInstallLock(ctx, tenantID, lockBase(pre), func(ctx context.Context) error {
		current, err := s.loadBundleRow(ctx, tenantID, pluginID)
		if err != nil {
			return err
		}
		if current.Status == types.PluginStatusRemoving {
			return fmt.Errorf("%w: %s is being uninstalled", ErrPluginInvalid, current.PluginID)
		}
		row = current
		if !current.Enabled {
			return nil
		}
		current.Enabled = false
		current.Error = nil
		return s.plugins.UpdatePlugin(ctx, current)
	})
	if err != nil {
		return nil, err
	}

	if _, err := s.host.Unregister(ctx, row.PluginID); err != nil {
		logger.Warnf(ctx, "[PluginBundle] %s: unregister failed: %v", row.PluginID, err)
	}
	if name := derefString(row.ContainerName); name != "" {
		if err := s.runtime.Stop(ctx, name); err != nil {
			logger.Warnf(ctx, "[PluginBundle] %s: stop failed: %v", row.PluginID, err)
		}
		if err := s.removeWhitelist(ctx, name); err != nil {
			logger.Warnf(ctx, "[PluginBundle] %s: whitelist removal failed: %v", row.PluginID, err)
		}
	}
	// The address belonged to the container that just stopped; clearing it is
	// what stops enable from handing out a port nobody is listening on.
	if err := s.plugins.UpdatePluginRuntime(ctx, row.ID, nil, nil); err != nil {
		logger.Warnf(ctx, "[PluginBundle] %s: clear runtime failed: %v", row.PluginID, err)
	}
	row.ContainerName, row.Endpoint = nil, nil
	row.Enabled = false
	return row, nil
}

// EnablePlugin starts the container again. It is asynchronous where disable is
// synchronous because starting means a container and a health probe, the same
// work the install does — so it writes status=installing and reuses that flow,
// heartbeat and reconciliation included.
func (s *TenantPluginService) EnablePlugin(
	ctx context.Context, tenantID *uint64, pluginID string,
) (*types.TenantPlugin, error) {
	pre, err := s.loadBundleRow(ctx, tenantID, pluginID)
	if err != nil {
		return nil, err
	}
	now := s.timestamp()
	var (
		row     *types.TenantPlugin
		already bool
	)
	mark := &installMark{}
	err = s.withInstallLock(ctx, tenantID, lockBase(pre), func(ctx context.Context) error {
		current, err := s.loadBundleRow(ctx, tenantID, pluginID)
		if err != nil {
			return err
		}
		row = current
		if current.Enabled && current.Status == types.PluginStatusReady {
			already = true
			return nil
		}
		if current.Status == types.PluginStatusInstalling {
			return fmt.Errorf("%w: %s is already installing", ErrPluginInvalid, current.PluginID)
		}
		if err := canTransition(current.Status, types.PluginStatusInstalling); err != nil {
			return err
		}
		current.Status = types.PluginStatusInstalling
		current.InstallingSince = &now
		current.Enabled = true
		current.Error = nil
		// Start hands out a fresh address every time; keeping the stored one
		// would register a port the new container does not own.
		current.Endpoint = nil
		if err := s.plugins.UpdatePlugin(ctx, current); err != nil {
			return err
		}
		mark.set(now)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if already {
		return row, nil
	}
	go s.runInstall(context.WithoutCancel(ctx), row, mark, false)
	return row, nil
}
