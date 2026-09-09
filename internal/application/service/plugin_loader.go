package service

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// NewManifestLoader builds the extension host's view of the plugin table.
//
// The signature of what it returns is fixed by extension.ManifestLoader — the
// host hands in the context and expects manifests back, and nothing else. That
// is the whole reason the host can stay free of any database dependency: the
// only thing crossing the boundary is a function.
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
				// A bad row is marked and skipped, never propagated: returning
				// the error would abort the whole load, and since the caller is
				// the start-up replay that means one malformed row makes every
				// plugin disappear. Skipping silently is the other wrong answer
				// — the operator would see the plugin gone with no reason given,
				// so the reason goes into the row's own error column.
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
// plugin row. It is installed on the host once, so it is also called for
// extensions that have no row at all — docreader above all. That is not an
// error: zero rows updated means "this extension is not database-backed", and
// failing the reconnect over it would break the one endpoint change that works
// today.
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
// Every field it does not set is deliberate: the row records what was installed,
// not what the plugin claimed about itself, and anything reconstructed here
// would be this function's opinion rather than the author's declaration.
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
		// Everything in this table was installed at run time, so nothing in it
		// may be required: a required extension that fails health turns /readyz
		// red for the whole process, and letting an installed plugin do that
		// hands one tenant's bad plugin the power to take the pod out of
		// rotation. Validate downgrades tenant-scoped ids on its own; this
		// covers the process-level rows it would leave alone.
		Criticality: extension.CriticalityOptional,
		// Disabled is an operator decision, not a health signal: Health
		// short-circuits on it instead of spending a dial timeout, and Ready
		// buckets it away from Degraded.
		Disabled: !row.Enabled,
	}
	// The stored permissions are the author's declaration, replayed as written.
	// PolicyClass is deliberately not folded in here: it is enforced by the
	// runtime's network mode, and writing it into Permissions.Network.Outbound
	// would make every offline plugin unloadable — outbound=none on a remote
	// transport is rejected as unenforceable, which is exactly right for a
	// declaration and exactly wrong for a container-level guarantee.
	m.Permissions = declaredPermissions(row.Permissions)
	return m, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// envMap narrows the stored JSON object to the string map the runtime takes.
// A non-string value is an error rather than a fmt.Sprintf: it means the row
// was written by something that did not agree with this schema, and coercing it
// would hide that until the plugin misbehaves with a value like "map[a:1]".
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
// host validates. It reads what is there and nothing more — an absent section
// stays at its zero value, which Validate already treats as "unspecified".
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
