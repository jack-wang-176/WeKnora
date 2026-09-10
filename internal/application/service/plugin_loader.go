package service

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// NewManifestLoader builds the extension host's view of the plugin table. Only
// a function crosses the boundary, which is how the host stays free of any
// database dependency.
func NewManifestLoader(
	repo repository.TenantPluginRepository,
	hostVersion string,
	reserved map[string]struct{},
) extension.ManifestLoader {
	return func(ctx context.Context) ([]*extension.Manifest, error) {
		rows, err := repo.ListReadyPlugins(ctx)
		if err != nil {
			return nil, err
		}
		manifests := make([]*extension.Manifest, 0, len(rows))
		for _, row := range rows {
			m, err := manifestFromRow(row)
			if err == nil {
				err = extension.ReplayManifest(m, hostVersion, reserved)
			}
			if err != nil {
				// Marked and skipped, never propagated: the caller is the
				// start-up replay, so returning the error would make one
				// malformed row hide every plugin. The reason goes into the
				// row's error column rather than being dropped.
				msg := err.Error()
				if uerr := repo.UpdatePluginState(ctx, row.ID, types.PluginStatusFailed, &msg); uerr != nil {
					logger.Errorf(ctx, "[PluginExtension] plugin %s rejected (%v), and marking it failed also failed: %v",
						row.PluginID, err, uerr)
				} else {
					logger.Warnf(ctx, "[PluginExtension] plugin %s rejected: %v", row.PluginID, err)
				}
				continue
			}
			manifests = append(manifests, m)
		}
		return manifests, nil
	}
}

// NewEndpointPersister writes a reconnect's normalized endpoint back to the
// plugin row. Installed once, so it also runs for extensions that have no row
// (docreader): zero rows updated means "not database-backed", not an error.
func NewEndpointPersister(repo repository.TenantPluginRepository) extension.EndpointPersistFunc {
	return func(ctx context.Context, id, normalizedEndpoint string) error {
		n, err := repo.UpdateEndpointByPluginID(ctx, id, normalizedEndpoint)
		if err != nil {
			return err
		}
		if n == 0 {
			logger.Debugf(ctx, "[PluginExtension] endpoint change for %s not persisted: no plugin row", id)
		}
		return nil
	}
}

// manifestFromRow rebuilds the manifest the host needs from one stored row.
// Fields it leaves unset are deliberate: the row records what was installed,
// not what the plugin claimed about itself.
func manifestFromRow(row *types.TenantPlugin) (*extension.Manifest, error) {
	env, err := envMap(row.Envs)
	if err != nil {
		return nil, fmt.Errorf("envs: %w", err)
	}
	m := &extension.Manifest{
		Metadata: extension.Metadata{
			ID:   row.PluginID,
			Name: row.PluginID,
		},
		Extension: extension.ExtensionSpec{
			Kind: extension.Kind(row.Kind),
		},
		Runtime: extension.Runtime{
			Transport: extension.Transport(row.Transport),
			Endpoint:  derefString(row.Endpoint),
			Env:       env,
		},
		// Nothing installed at run time may be required: a required extension
		// that fails health turns /readyz red for the whole process. Validate
		// downgrades tenant-scoped ids on its own; this covers the
		// process-level rows it leaves alone.
		Criticality: extension.CriticalityOptional,
		// Disabled is an operator decision, not a health signal: Health
		// short-circuits on it instead of spending a dial timeout, and Ready
		// buckets it away from Degraded.
		Disabled: !row.Enabled,
	}
	// The author's declaration, replayed as written. PolicyClass is deliberately
	// not folded in: it is enforced by the runtime's network mode, and
	// outbound=none on a remote transport is rejected as unenforceable, which
	// would make every offline plugin unloadable.
	m.Permissions = declaredPermissions(row.Permissions)
	return m, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// envMap narrows the stored JSON object to the string map the runtime takes. A
// non-string value is an error rather than a fmt.Sprintf: coercing it would
// hide the schema disagreement until the plugin misbehaves.
func envMap(raw types.JSONMap) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%q is %T, want string", k, v)
		}
		out[k] = s
	}
	return out, nil
}

// declaredPermissions reads the stored declaration back into the struct the
// host validates. An absent section stays at its zero value, which Validate
// already treats as "unspecified".
func declaredPermissions(raw types.JSONMap) extension.Permissions {
	var p extension.Permissions
	if network, ok := raw["network"].(map[string]any); ok {
		if outbound, ok := network["outbound"].(string); ok {
			p.Network.Outbound = outbound
		}
		p.Network.Allow = stringSlice(network["allow"])
	}
	if fs, ok := raw["filesystem"].(map[string]any); ok {
		p.Filesystem.Read = stringSlice(fs["read"])
		p.Filesystem.Write = stringSlice(fs["write"])
	}
	p.Secrets = stringSlice(raw["secrets"])
	return p
}

func stringSlice(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
