package service

import (
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
)

// canTransition is the only place that says which status changes are legal.
// Six entry points share it so that adding a state is one edit, not six.
//
// There is no `disabled` status: a stopped plugin is `ready` with enabled=false.
// Enabled is already a column, and a second spelling of the same fact has no
// answer for what to do when the two disagree.
func canTransition(from, to string) error {
	if from == to {
		return nil
	}
	switch from {
	case types.PluginStatusRemoving:
		// Removal has already started touching containers and the whitelist.
		// Anything that walks it back races with the goroutine doing it.
		return fmt.Errorf("%w: plugin is being uninstalled", ErrPluginInvalid)
	case "", types.PluginStatusFailed:
		switch to {
		case types.PluginStatusInstalling, types.PluginStatusRemoving:
			return nil
		}
	case types.PluginStatusInstalling:
		switch to {
		case types.PluginStatusReady, types.PluginStatusFailed, types.PluginStatusRemoving:
			return nil
		}
	case types.PluginStatusReady:
		switch to {
		// ready -> installing is the upgrade and the re-enable path: both have
		// to run the container steps again, so both reuse the install flow's
		// heartbeat and reconciliation rather than inventing their own.
		case types.PluginStatusInstalling, types.PluginStatusRemoving, types.PluginStatusFailed:
			return nil
		}
	}
	return fmt.Errorf("%w: cannot go from %s to %s", ErrPluginInvalid, from, to)
}
