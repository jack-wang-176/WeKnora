package service

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

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

// manifestFromRow restores the declaration, then overlays operator-owned state.
func manifestFromRow(row *types.TenantPlugin) (*extension.Manifest, error) {
	env, err := envMap(row.Envs)
	if err != nil {
		return nil, fmt.Errorf("envs: %w", err)
	}
	m := &extension.Manifest{}
	if row.Manifest != "" {
		decoder := yaml.NewDecoder(strings.NewReader(row.Manifest))
		decoder.KnownFields(true)
		if err := decoder.Decode(m); err != nil {
			return nil, fmt.Errorf("stored manifest: %w", err)
		}
	}
	m.Metadata.ID = row.PluginID
	if m.Metadata.Name == "" {
		m.Metadata.Name = row.PluginID
	}
	m.Extension.Kind = extension.Kind(row.Kind)
	m.Runtime = extension.Runtime{
		Transport: extension.Transport(row.Transport), Endpoint: derefString(row.Endpoint), Env: env,
		ManagedOffline: row.Channel == types.PluginChannelBundle && row.PolicyClass == types.PluginPolicyOffline,
	}
	m.Criticality = extension.CriticalityOptional
	m.Disabled = !row.Enabled
	m.Builtin = false
	m.Permissions, err = declaredPermissions(row.Permissions)
	if err != nil {
		return nil, fmt.Errorf("%w: permissions: %v", ErrPluginInvalid, err)
	}
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
func declaredPermissions(raw types.JSONMap) (extension.Permissions, error) {
	var permissions extension.Permissions
	encoded, err := yaml.Marshal(raw)
	if err != nil {
		return permissions, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(encoded)))
	decoder.KnownFields(true)
	err = decoder.Decode(&permissions)
	return permissions, err
}
