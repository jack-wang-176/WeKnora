package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// The bundle channel's half of installation: request in, running container and
// a registered plugin out. The container is ours, so unlike the endpoint
// channel every step here has something to undo.

// A bundle plugin always serves gRPC: the image is started by this process and
// probed through the gRPC health service.
const pluginBundleTransport = string(extension.TransportRemoteGRPC)

// pluginRuntimeStore is the single write activatePluginRow needs. Narrowing it
// lets both channels' services share the helper.
type pluginRuntimeStore interface {
	UpdatePluginRuntime(ctx context.Context, id string, containerName *string, endpoint *string) error
}

// activatePluginRow is the one normalize -> store -> register sequence, shared
// by both channels so they cannot drift apart.
//
// It notifies nothing. The extension points that consume plugins ask the host
// at use time instead, which keeps the dependency pointing one way and makes
// an uninstall take effect without a second notification.
func activatePluginRow(
	ctx context.Context, plugins pluginRuntimeStore, host extension.Host, row *types.TenantPlugin,
) error {
	normalized, err := extension.NormalizeEndpoint(
		extension.Transport(row.Transport), derefString(row.Endpoint),
	)
	if err != nil {
		return fmt.Errorf("endpoint: %w", err)
	}
	if derefString(row.Endpoint) != normalized {
		// Store first, then build: the manifest has to be built from the value
		// the loader will read back after a restart.
		if err := plugins.UpdatePluginRuntime(ctx, row.ID, row.ContainerName, &normalized); err != nil {
			return fmt.Errorf("store normalized endpoint: %w", err)
		}
		row.Endpoint = &normalized
	}
	m, err := manifestFromRow(row)
	if err != nil {
		return err
	}
	// Register, never a direct write into the host's map: the reserved-id check,
	// the tenant downgrade of criticality and manifest validation all live there.
	if _, err := host.Register(ctx, m); err != nil {
		return err
	}
	return nil
}

// parsePluginManifest reads the author's declaration. Only the facts the row
// has to carry are taken from it; runtime.endpoint is ignored because the
// address belongs to the container this process starts.
//
// Manifest.Validate is not run here: it requires a non-empty endpoint, which
// does not exist until the container is up. host.Register runs it at step 7.
func parsePluginManifest(raw string) (*extension.Manifest, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w: manifest is required", ErrPluginInvalid)
	}
	var m extension.Manifest
	if err := yaml.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("%w: manifest: %v", ErrPluginInvalid, err)
	}
	if _, ok := pluginKinds[string(m.Extension.Kind)]; !ok {
		return nil, fmt.Errorf("%w: extension.type %q is not one of docparser/websearch/datasource",
			ErrPluginInvalid, m.Extension.Kind)
	}
	return &m, nil
}

// permissionsMap is the inverse of declaredPermissions in plugin_loader.go: the
// column holds the jsonb shape that function reads back, so the keys must stay
// lowercase and the slices must stay []any.
func permissionsMap(p extension.Permissions) types.JSONMap {
	return types.JSONMap{
		"network": map[string]any{
			"outbound": p.Network.Outbound,
			"allow":    anySlice(p.Network.Allow),
		},
		"filesystem": map[string]any{
			"read":  anySlice(p.Filesystem.Read),
			"write": anySlice(p.Filesystem.Write),
		},
		"secrets": anySlice(p.Secrets),
	}
}

func anySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

func newBundlePluginRow(
	req *types.PluginInstallRequest, base string, src pluginSource, m *extension.Manifest, now time.Time,
) (*types.TenantPlugin, error) {
	policy := strings.TrimSpace(req.PolicyClass)
	if policy == "" {
		policy = types.PluginPolicyScoped
	}
	if _, ok := pluginPolicyClasses[policy]; !ok {
		return nil, fmt.Errorf("%w: policy_class %q is not one of offline/scoped/open",
			ErrPluginInvalid, req.PolicyClass)
	}
	sourceURL := src.URL
	imageRef := src.URL
	row := &types.TenantPlugin{
		ID:          uuid.New().String(),
		TenantID:    req.TenantID,
		PluginID:    scopedPluginID(base, req.TenantID),
		Kind:        string(m.Extension.Kind),
		Channel:     types.PluginChannelBundle,
		Transport:   pluginBundleTransport,
		PolicyClass: policy,
		SourceURL:   &sourceURL,
		ImageRef:    &imageRef,
		// The container does not exist yet, so neither does the endpoint;
		// installing is what stops the boot replay from loading this row.
		Status:          types.PluginStatusInstalling,
		InstallingSince: &now,
		Enabled:         true,
		Envs:            widenStringMap(req.Envs),
		Permissions:     permissionsMap(m.Permissions),
	}
	if src.Ref != "" {
		ref := src.Ref
		row.SourceRef = &ref
	}
	return row, nil
}

// InstallPlugin claims the row and returns. The container work runs in the
// background because an image pull outlives any sane HTTP timeout; callers
// follow it through the progress stream and the row's status.
func (s *TenantPluginService) InstallPlugin(
	ctx context.Context, req *types.PluginInstallRequest,
) (*types.TenantPlugin, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: nil request", ErrPluginInvalid)
	}
	m, err := parsePluginManifest(req.Manifest)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSpace(req.PluginID)
	if base == "" {
		base = strings.TrimSpace(m.Metadata.ID)
	}
	if base == "" {
		return nil, fmt.Errorf("%w: plugin_id is required", ErrPluginInvalid)
	}
	if strings.Contains(base, "--") {
		// The tenant suffix is composed below, never accepted from the request.
		return nil, fmt.Errorf("%w: plugin_id %q must not carry a tenant suffix", ErrPluginInvalid, base)
	}
	src, err := parsePluginSource(req)
	if err != nil {
		return nil, err
	}
	now := s.clock()().UTC()
	fresh, err := newBundlePluginRow(req, base, src, m, now)
	if err != nil {
		return nil, err
	}
	if imageRefIsFloating(src.URL) {
		logger.Warnf(ctx, "[PluginBundle] %s: image ref %q is floating, a re-install may keep the old container",
			fresh.PluginID, src.URL)
	}

	var (
		row      *types.TenantPlugin
		wasReady bool
	)
	mark := &installMark{}
	err = s.withInstallLock(ctx, req.TenantID, base, func(ctx context.Context) error {
		existing, err := s.plugins.GetPluginByPluginID(ctx, req.TenantID, fresh.PluginID)
		if err != nil {
			return err
		}
		if existing == nil {
			if err := s.plugins.CreatePlugin(ctx, fresh); err != nil {
				return err
			}
			row, mark = fresh, &installMark{}
			mark.set(now)
			return nil
		}
		if existing.Channel != types.PluginChannelBundle {
			return fmt.Errorf("%w: %s is installed through the %s channel",
				ErrPluginExists, existing.PluginID, existing.Channel)
		}
		// canTransition allows installing -> installing (a no-op edge), so the
		// "already in flight" case has to be refused before asking it.
		if existing.Status == types.PluginStatusInstalling {
			return fmt.Errorf("%w: %s is already installing", ErrPluginExists, existing.PluginID)
		}
		if err := canTransition(existing.Status, types.PluginStatusInstalling); err != nil {
			return err
		}
		// Upgrade and retry are the same write: keep the row, take the new
		// source, and let the install flow run again from the top.
		wasReady = existing.Status == types.PluginStatusReady
		existing.Kind = fresh.Kind
		existing.Transport = fresh.Transport
		existing.PolicyClass = fresh.PolicyClass
		existing.SourceURL = fresh.SourceURL
		existing.SourceRef = fresh.SourceRef
		existing.ImageRef = fresh.ImageRef
		existing.Envs = fresh.Envs
		existing.Permissions = fresh.Permissions
		existing.Status = types.PluginStatusInstalling
		existing.InstallingSince = &now
		existing.Enabled = true
		existing.Error = nil
		if err := s.plugins.UpdatePlugin(ctx, existing); err != nil {
			return err
		}
		row = existing
		mark.set(now)
		return nil
	})
	if err != nil {
		return nil, err
	}

	go s.runInstall(context.WithoutCancel(ctx), row, mark, wasReady, m.HealthCheck.Service)
	return row, nil
}

// runInstall does the container half. Every step before the register call is
// undoable and is undone by the deferred handler; everything after it is not.
func (s *TenantPluginService) runInstall(
	base context.Context, row *types.TenantPlugin, mark *installMark, wasReady bool, healthService string,
) {
	ctx, cancel := context.WithTimeout(base, pluginInstallBudget)
	defer cancel()
	stopHeartbeat := s.startHeartbeat(ctx, row, mark)
	defer stopHeartbeat()

	var (
		failErr    error
		registered bool
	)
	defer func() {
		if failErr == nil {
			return
		}
		if registered {
			// Past the irreversible point the plugin is already serving:
			// writing failed here would have reconciliation delete a working
			// container, so the row is left for reconciliation to repair.
			logger.Errorf(base, "[PluginBundle] %s: post-register step failed: %v", row.PluginID, failErr)
			return
		}
		cctx, ccancel := cleanupContext(base)
		defer ccancel()
		if _, ok := s.stillOwns(cctx, row, mark); !ok {
			return
		}
		if wasReady {
			// An upgrade may have replaced the container the old registration
			// points at, so memory has to let go before the row says failed.
			if _, err := s.host.Unregister(cctx, row.PluginID); err != nil {
				logger.Warnf(cctx, "[PluginBundle] %s: unregister during cleanup failed: %v", row.PluginID, err)
			}
		}
		if name := derefString(row.ContainerName); name != "" {
			if err := s.runtime.Stop(cctx, name); err != nil {
				logger.Warnf(cctx, "[PluginBundle] %s: stop during cleanup failed: %v", row.PluginID, err)
			}
			if err := s.removeWhitelist(cctx, name); err != nil {
				logger.Warnf(cctx, "[PluginBundle] %s: whitelist cleanup failed: %v", row.PluginID, err)
			}
		}
		msg := failErr.Error()
		if err := s.plugins.UpdatePluginState(cctx, row.ID, types.PluginStatusFailed, &msg); err != nil {
			logger.Errorf(cctx, "[PluginBundle] %s: mark failed: %v", row.PluginID, err)
		}
		s.publishProgress(cctx, row, PluginProgress{
			Percent: 100, Stage: "failed", Log: msg, Status: types.PluginStatusFailed,
		})
	}()

	s.publishProgress(ctx, row, PluginProgress{
		Percent: 5, Stage: "starting", Status: types.PluginStatusInstalling,
	})
	spec, err := specFor(row)
	if err != nil {
		failErr = err
		return
	}
	// Start pulls the image when it is missing, so this one call is the whole
	// multi-minute part of the install.
	inst, err := s.runtime.Start(ctx, spec)
	if err != nil {
		failErr = fmt.Errorf("start container: %w", err)
		return
	}
	row.ContainerName = &inst.ContainerName
	s.publishProgress(ctx, row, PluginProgress{
		Percent: 60, Stage: "started", Status: types.PluginStatusInstalling,
	})

	// Whitelist before the probe: the probe dials the container name and the
	// SSRF guard refuses a name the whitelist does not carry.
	if err := s.addWhitelist(ctx, inst.ContainerName); err != nil {
		failErr = fmt.Errorf("whitelist: %w", err)
		return
	}

	// DB before memory: a crash after this leaves a row reconciliation can
	// finish rather than a container nobody recorded.
	addr := inst.Address
	if err := s.plugins.UpdatePluginRuntime(ctx, row.ID, &inst.ContainerName, &addr); err != nil {
		failErr = fmt.Errorf("store runtime: %w", err)
		return
	}
	row.Endpoint = &addr

	s.publishProgress(ctx, row, PluginProgress{
		Percent: 80, Stage: "probing", Status: types.PluginStatusInstalling,
	})
	// Register validates and stores but never dials, so reachability has to be
	// proven here or a ready row can point at a container that never served.
	if err := extension.ProbeGRPC(ctx, addr, healthService, pluginProbeBudget); err != nil {
		failErr = fmt.Errorf("health probe: %w", err)
		return
	}

	if _, ok := s.stillOwns(ctx, row, mark); !ok {
		// Uninstalled or taken over while the container was starting. The
		// container we started is now an orphan; the reaper claims it.
		logger.Warnf(base, "[PluginBundle] %s: lost the row before register, leaving the rest to reconciliation",
			row.PluginID)
		return
	}
	if err := activatePluginRow(ctx, s.plugins, s.host, row); err != nil {
		failErr = fmt.Errorf("register: %w", err)
		return
	}
	registered = true
	// ---- irreversible point: the plugin is serving from here on ----
	if err := s.plugins.UpdatePluginState(ctx, row.ID, types.PluginStatusReady, nil); err != nil {
		failErr = fmt.Errorf("mark ready: %w", err)
		return
	}
	s.publishProgress(ctx, row, PluginProgress{
		Percent: 100, Stage: "ready", Status: types.PluginStatusReady,
	})
}
