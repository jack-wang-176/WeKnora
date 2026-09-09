package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Errors this service returns to its callers. They are sentinels rather than
// strings because the HTTP layer has to map them to different status codes and
// a handler that matched on message text would break the first time one of
// these sentences was reworded.
var (
	ErrPluginInvalid  = errors.New("plugin: invalid request")
	ErrPluginNotFound = errors.New("plugin: not found")
	ErrPluginExists   = errors.New("plugin: already installed")
	ErrPluginDisabled = errors.New("plugin: disabled")
)

// auditScopePlugin is the scope_type every plugin audit row carries, so an
// operator can pull the whole extension history of a workspace with one filter.
const auditScopePlugin = "plugin"

// pluginService owns the four writes that change what this process is willing
// to connect to: register, uninstall, enable/disable, repoint.
//
// Every one of them is ordered store-first, host-second. That is not a
// preference between two orderings, it is the only one that survives a crash in
// the middle: the host's map is rebuilt from the table on the next boot, so a
// write that reached the host but not the table disappears, while one that
// reached the table but not the host is replayed. The single exception is
// Repoint, and it is an exception in appearance only — the host writes the
// store itself there, through the callback installed in step 2, because only
// the host knows the normalized address and whether the dial actually worked.
//
// What this service deliberately does not have, compared with
// TenantSkillService: the Redis config lock and its lease. Those exist to stop
// two replicas from writing one shared artifact (a skill image pointer). The
// `endpoint` channel builds nothing and shares nothing — the whole operation is
// one row and one map entry — so a distributed lock here would buy nothing and
// cost "Redis is down, therefore no plugin can be installed". It arrives with
// the `bundle` channel, which does build images.
type pluginService struct {
	plugins repository.TenantPluginRepository
	host    extension.Host
	audit   interfaces.AuditLogService

	// locks serialises operations on one plugin within this process. The key is
	// tenant + base id rather than the stored id so that a future "same plugin,
	// two versions" lands on one lock: install, uninstall and repoint of one
	// plugin must never interleave, and they would all share the base.
	locks *keyedMutex

	now func() time.Time
}

var _ interfaces.TenantPluginService = (*pluginService)(nil)

// NewPluginService builds the plugin service. It takes the host rather than
// building one: the host is a process-wide singleton holding live connections,
// and a second one would mean two answers to "is this plugin loaded".
func NewPluginService(
	plugins repository.TenantPluginRepository,
	host extension.Host,
	audit interfaces.AuditLogService,
) interfaces.TenantPluginService {
	return &pluginService{
		plugins: plugins,
		host:    host,
		audit:   audit,
		locks:   newKeyedMutex(),
		now:     time.Now,
	}
}

// Register installs one plugin through the `endpoint` channel.
//
// Order: validate → INSERT status=installing → normalize the address into the
// row → build the manifest FROM THE ROW → host.Register → UPDATE status=ready.
//
// The manifest is built from the row and not from the request even though the
// request is where the address came from. Two reasons, and the second is the
// one that bites: the stored value has been through trimming, defaulting and a
// jsonb round trip, so a manifest built from the request can differ from the
// one the loader rebuilds after a restart — and the first Reload then sees
// isRuntimeChanged, tears down a healthy connection and redials it. The other
// reason is the `bundle` channel, where there is no address in the request at
// all; one construction path is what keeps the two channels from producing
// manifests that differ in a field nobody notices until a plugin misbehaves.
func (s *pluginService) Register(
	ctx context.Context, req *types.PluginRegisterRequest,
) (*types.TenantPlugin, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: nil request", ErrPluginInvalid)
	}
	row, err := newEndpointPluginRow(req, s.now())
	if err != nil {
		return nil, err
	}

	release, err := s.locks.lock(ctx, pluginLockKey(row.TenantID, row.PluginID))
	if err != nil {
		return nil, err
	}
	defer release()

	existing, err := s.plugins.GetPluginByPluginID(ctx, row.TenantID, row.PluginID)
	if err != nil {
		return nil, err
	}
	switch {
	case existing == nil:
		if err := s.plugins.CreatePlugin(ctx, row); err != nil {
			return nil, err
		}
	case existing.Status == types.PluginStatusFailed:
		// A failed row is the one case where re-registering overwrites instead
		// of refusing. Without this the only way out of a typo'd address is
		// uninstall-then-register, and an operator staring at a row that says
		// "failed" reasonably expects to be able to correct it in place.
		row.ID = existing.ID
		row.CreatedAt = existing.CreatedAt
		if err := s.plugins.UpdatePlugin(ctx, row); err != nil {
			return nil, err
		}
		// UpdatePlugin does not touch the two jsonb columns; they have their
		// own setters so that an envs write cannot silently revert a
		// permissions change made by another request.
		if err := s.plugins.UpdatePluginEnvs(ctx, row.ID, req.Envs); err != nil {
			return nil, err
		}
		if err := s.plugins.UpdatePluginPermissions(ctx, row.ID, req.Permissions); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%s: %w", row.PluginID, ErrPluginExists)
	}

	if err := s.activate(ctx, row); err != nil {
		s.auditPlugin(ctx, row, types.AuditActionPluginRegistered, types.AuditOutcomeFailed, map[string]any{
			"kind":      row.Kind,
			"transport": row.Transport,
			"endpoint":  derefString(row.Endpoint),
			"error":     err.Error(),
		})
		// The row is returned alongside the error on purpose: it carries
		// status=failed and the reason, which is what the caller has to show.
		return row, err
	}
	s.auditPlugin(ctx, row, types.AuditActionPluginRegistered, types.AuditOutcomeSuccess, map[string]any{
		"kind":         row.Kind,
		"channel":      row.Channel,
		"transport":    row.Transport,
		"endpoint":     derefString(row.Endpoint),
		"policy_class": row.PolicyClass,
	})
	return row, nil
}

// Uninstall removes the plugin from the host, then from the table.
func (s *pluginService) Uninstall(ctx context.Context, tenantID *uint64, pluginID string) error {
	release, err := s.locks.lock(ctx, pluginLockKey(tenantID, pluginID))
	if err != nil {
		return err
	}
	defer release()

	row, err := s.plugins.GetPluginByPluginID(ctx, tenantID, pluginID)
	if err != nil {
		return err
	}
	if row == nil {
		return s.classifyMissing(pluginID)
	}

	prev := row.Status
	if err := s.plugins.UpdatePluginState(ctx, row.ID, types.PluginStatusRemoving, nil); err != nil {
		return err
	}
	if _, err := s.host.Unregister(ctx, row.PluginID); err != nil && !errors.Is(err, extension.ErrNotFound) {
		// ErrNotFound is success: the caller asked for "not loaded" and it is
		// not loaded. Anything else — ErrBuiltinImmutable above all — is a
		// refusal, and the row goes back to the status it had. Back to what it
		// had, not to ready: a row that was failed is still failed, and calling
		// it ready would erase the reason it never loaded.
		if uerr := s.plugins.UpdatePluginState(ctx, row.ID, prev, row.Error); uerr != nil {
			logger.Errorf(ctx, "[PluginExtension] %s: unregister failed (%v) and the status rollback also failed: %v",
				row.PluginID, err, uerr)
		}
		s.auditPlugin(ctx, row, types.AuditActionPluginUninstalled, types.AuditOutcomeFailed, map[string]any{
			"error": err.Error(),
		})
		return err
	}
	if err := s.plugins.DeletePlugin(ctx, tenantID, row.ID); err != nil {
		return err
	}
	s.auditPlugin(ctx, row, types.AuditActionPluginUninstalled, types.AuditOutcomeSuccess, map[string]any{
		"kind":     row.Kind,
		"channel":  row.Channel,
		"endpoint": derefString(row.Endpoint),
	})
	return nil
}

// SetEnabled turns a plugin off or back on.
//
// Off means unregistered from the host, not flagged in place. Under the
// `endpoint` channel there is no container to stop, so "flagged" would leave
// the connection in host.remote — still dialled by HealthAll, still counted by
// /readyz — and the operator who disabled the plugin would keep seeing it.
// Manifest.Disabled, the other shape, is for the `bundle` channel: there the
// manifest stays in the host because stopping and starting a container costs
// enough that re-enabling should not have to re-validate anything. One shape
// per channel, and this is the endpoint channel's.
func (s *pluginService) SetEnabled(
	ctx context.Context, tenantID *uint64, pluginID string, enabled bool,
) (*types.TenantPlugin, error) {
	release, err := s.locks.lock(ctx, pluginLockKey(tenantID, pluginID))
	if err != nil {
		return nil, err
	}
	defer release()

	row, err := s.plugins.GetPluginByPluginID(ctx, tenantID, pluginID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, s.classifyMissing(pluginID)
	}
	if row.Enabled == enabled {
		// Already in the requested state. No write, and no audit row either:
		// an audit trail that records non-events makes the real ones harder
		// to find.
		return row, nil
	}

	action := types.AuditActionPluginEnabled
	if !enabled {
		action = types.AuditActionPluginDisabled
	}

	row.Enabled = enabled
	if err := s.plugins.UpdatePlugin(ctx, row); err != nil {
		return nil, err
	}
	if enabled {
		if err := s.activate(ctx, row); err != nil {
			s.auditPlugin(ctx, row, action, types.AuditOutcomeFailed, map[string]any{"error": err.Error()})
			return row, err
		}
	} else if _, err := s.host.Unregister(ctx, row.PluginID); err != nil &&
		!errors.Is(err, extension.ErrNotFound) {
		row.Enabled = true
		if uerr := s.plugins.UpdatePlugin(ctx, row); uerr != nil {
			logger.Errorf(ctx, "[PluginExtension] %s: disable failed (%v) and the enabled rollback also failed: %v",
				row.PluginID, err, uerr)
		}
		s.auditPlugin(ctx, row, action, types.AuditOutcomeFailed, map[string]any{"error": err.Error()})
		return nil, err
	}
	s.auditPlugin(ctx, row, action, types.AuditOutcomeSuccess, map[string]any{
		"endpoint": derefString(row.Endpoint),
	})
	return row, nil
}

// Repoint moves a live plugin to a new address.
//
// This is the one write the service does not order itself, and that is the
// payoff of the callback installed in step 2: the host writes the store from
// inside Reconnect, at the one moment both facts are known — the normalized
// form of the address, and that the reconnect actually succeeded. A service
// that wrote the row first would be storing an address that may not answer;
// one that wrote it afterwards would leave a window where the process talks to
// an address the table has never heard of. So there is nothing to roll back
// here: if the store write fails, Reconnect has already put the channel back.
func (s *pluginService) Repoint(
	ctx context.Context, tenantID *uint64, pluginID string, endpoint string,
) (*types.TenantPlugin, error) {
	release, err := s.locks.lock(ctx, pluginLockKey(tenantID, pluginID))
	if err != nil {
		return nil, err
	}
	defer release()

	row, err := s.plugins.GetPluginByPluginID(ctx, tenantID, pluginID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, s.classifyMissing(pluginID)
	}
	if !row.Enabled {
		// Reconnect would report this as "not found", which is true of the
		// host and misleading to the caller: the plugin exists, it is off.
		return nil, fmt.Errorf("%s: %w", row.PluginID, ErrPluginDisabled)
	}
	previous := derefString(row.Endpoint)

	status, err := s.host.Reconnect(ctx, row.PluginID, endpoint)
	if err != nil {
		s.auditPlugin(ctx, row, types.AuditActionPluginRepointed, types.AuditOutcomeFailed, map[string]any{
			"from":      previous,
			"requested": endpoint,
			"error":     err.Error(),
		})
		return nil, err
	}

	// Re-read rather than assume: the stored value is the host's normalized
	// form, not the string the caller sent, and returning the request back to
	// the caller would show them an address that is not what was saved.
	updated, rerr := s.plugins.GetPluginByPluginID(ctx, row.TenantID, row.PluginID)
	if rerr != nil || updated == nil {
		if rerr != nil {
			logger.Warnf(ctx, "[PluginExtension] %s repointed, re-read failed: %v", row.PluginID, rerr)
		}
		updated = row
	}
	s.auditPlugin(ctx, updated, types.AuditActionPluginRepointed, types.AuditOutcomeSuccess, map[string]any{
		"from":  previous,
		"to":    status.Endpoint,
		"state": string(status.State),
	})
	return updated, nil
}

func (s *pluginService) Get(
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

func (s *pluginService) ListForTenant(
	ctx context.Context, tenantID uint64,
) ([]*types.TenantPlugin, error) {
	return s.plugins.ListPluginsByTenant(ctx, tenantID)
}

func (s *pluginService) ListGlobal(ctx context.Context) ([]*types.TenantPlugin, error) {
	return s.plugins.ListGlobalPlugins(ctx)
}

// activate makes a stored row live and records the outcome in the row itself.
// It is shared by Register and by the enable half of SetEnabled so that the two
// cannot end up with different notions of what "loaded" means.
func (s *pluginService) activate(ctx context.Context, row *types.TenantPlugin) error {
	err := s.activateOnce(ctx, row)
	if err == nil {
		if uerr := s.plugins.UpdatePluginState(ctx, row.ID, types.PluginStatusReady, nil); uerr != nil {
			return uerr
		}
		row.Status = types.PluginStatusReady
		row.Error = nil
		return nil
	}
	// The row stays. It is the only surface an operator has for reading why a
	// plugin will not load, and a row deleted on failure takes that with it.
	msg := err.Error()
	if uerr := s.plugins.UpdatePluginState(ctx, row.ID, types.PluginStatusFailed, &msg); uerr != nil {
		logger.Errorf(ctx, "[PluginExtension] %s failed to load (%v), and recording the failure also failed: %v",
			row.PluginID, err, uerr)
	}
	row.Status = types.PluginStatusFailed
	row.Error = &msg
	return err
}

func (s *pluginService) activateOnce(ctx context.Context, row *types.TenantPlugin) error {
	normalized, err := extension.NormalizeEndpoint(
		extension.Transport(row.Transport), derefString(row.Endpoint),
	)
	if err != nil {
		return fmt.Errorf("endpoint: %w", err)
	}
	if derefString(row.Endpoint) != normalized {
		// Store first, then build. The manifest below has to be built from the
		// value the loader will read back after a restart, and that value is
		// this one — not the string the caller typed.
		if err := s.plugins.UpdatePluginRuntime(ctx, row.ID, row.ContainerName, &normalized); err != nil {
			return fmt.Errorf("store normalized endpoint: %w", err)
		}
		row.Endpoint = &normalized
	}
	m, err := manifestFromRow(row)
	if err != nil {
		return err
	}
	// Register, never a direct write into the host's map: this is where the
	// reserved-id check, the tenant downgrade of criticality and the manifest
	// validation live, and a service that assembled the map itself would be
	// bypassing all three.
	if _, err := s.host.Register(ctx, m); err != nil {
		return err
	}
	return nil
}

// classifyMissing answers "there is no row for this id" in the way that is true
// rather than the way that is convenient. An id the host holds as builtin is
// refused as immutable — docreader has no row and must not look uninstallable
// merely because the table does not mention it. Anything else the host holds
// without a row is file-backed, and this API does not manage the filesystem, so
// it is reported as not found too rather than being quietly removed from the
// host by an HTTP call.
func (s *pluginService) classifyMissing(pluginID string) error {
	if m, ok := s.host.Get(pluginID); ok && m.Builtin {
		return fmt.Errorf("%s: %w", pluginID, extension.ErrBuiltinImmutable)
	}
	return fmt.Errorf("%s: %w", pluginID, ErrPluginNotFound)
}

// pluginLockKey serialises operations on one plugin. The tenant is spelled out
// separately from the id because a nil tenant is a scope, not a missing value:
// the global "foo" and tenant 7's "foo--7" are different plugins and must not
// share a lock, while tenant 7's install and uninstall of "foo--7" must.
func pluginLockKey(tenantID *uint64, pluginID string) string {
	base, _ := extension.SplitID(pluginID)
	if tenantID == nil {
		return "weknora-plugin:global:" + base
	}
	return fmt.Sprintf("weknora-plugin:%d:%s", *tenantID, base)
}

// auditPlugin records one plugin operation. Failures to write the audit row are
// logged and swallowed: an audit backend that is down must not be able to stop
// an operator from removing a plugin that is causing an incident.
func (s *pluginService) auditPlugin(
	ctx context.Context,
	row *types.TenantPlugin,
	action types.AuditAction,
	outcome types.AuditOutcome,
	details map[string]any,
) {
	if s.audit == nil || row == nil {
		return
	}
	// tenant_id 0 marks a process-level plugin, the same system-scope
	// convention the system setting events use. It is not a tenant.
	var tenant uint64
	if row.TenantID != nil {
		tenant = *row.TenantID
	}
	payload, err := json.Marshal(details)
	if err != nil {
		payload = []byte(`{}`)
	}
	if aerr := s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenant,
		ActorUserID: auditActor(ctx),
		ActorRole:   auditActorRole(ctx),
		Action:      action,
		ScopeType:   auditScopePlugin,
		ScopeID:     row.PluginID,
		TargetType:  auditScopePlugin,
		TargetID:    row.ID,
		Outcome:     outcome,
		Details:     types.JSON(payload),
	}); aerr != nil {
		logger.Warnf(ctx, "[PluginExtension] audit %s for %s not recorded: %v", action, row.PluginID, aerr)
	}
}
