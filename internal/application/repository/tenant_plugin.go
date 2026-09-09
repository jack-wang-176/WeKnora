package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// TenantPluginRepository manages plugin main records and their snapshot ledger.
type TenantPluginRepository interface {
	// CreatePlugin inserts a plugin row before provider‑side work starts.
	CreatePlugin(ctx context.Context, p *types.TenantPlugin) error

	// GetPlugin returns the plugin by its primary key (id).
	GetPlugin(ctx context.Context, tenantID *uint64, id string) (*types.TenantPlugin, error)

	// GetPluginByPluginID retrieves a plugin by its logical key (tenant_id, plugin_id).
	GetPluginByPluginID(ctx context.Context, tenantID *uint64, pluginID string) (*types.TenantPlugin, error)

	// ListPluginsByTenant returns all tenant‑level plugins for a given tenant
	ListPluginsByTenant(ctx context.Context, tenantID uint64) ([]*types.TenantPlugin, error)

	// ListGlobalPlugins returns all process‑level (global) plugins.
	ListGlobalPlugins(ctx context.Context) ([]*types.TenantPlugin, error)

	// ListReadyPlugins returns every plugin the host should have loaded,
	// tenant-scoped rows included: the tenant dimension lives inside plugin_id
	// (see TenantPlugin.PluginID), so the host does not need it split out and
	// filtering on tenant_id here would silently drop every tenant plugin from
	// the start-up replay.
	ListReadyPlugins(ctx context.Context) ([]*types.TenantPlugin, error)

	// UpdatePlugin updates mutable fields of a plugin (status, error, enabled,
	// container_name, etc.). This method does NOT touch envs or permissions.
	UpdatePlugin(ctx context.Context, p *types.TenantPlugin) error

	// UpdatePluginState changes the status and optionally the error message,
	// used for state‑machine transitions during install/remove.
	UpdatePluginState(ctx context.Context, id string, status string, errMsg *string) error

	// UpdatePluginRuntime updates runtime information (container name and endpoint)
	// written by the orchestrator.
	UpdatePluginRuntime(ctx context.Context, id string, containerName *string, endpoint *string) error

	// UpdateEndpointByPluginID repoints a plugin addressed by its full plugin
	// id rather than its row id, and reports how many rows it touched. The
	// extension host knows plugins by manifest id only, so this is the shape
	// its endpoint-persistence callback can call; zero rows is not an error
	// there, it means the extension is builtin or file-backed and has no row.
	UpdateEndpointByPluginID(ctx context.Context, pluginID string, endpoint string) (int64, error)

	// UpdatePluginEnvs updates only the environment variables (subject to the
	// secrets whitelist).
	UpdatePluginEnvs(ctx context.Context, id string, envs map[string]string) error

	// UpdatePluginPermissions updates only the raw permission declaration
	// for auditing purposes.
	UpdatePluginPermissions(ctx context.Context, id string, perms map[string]interface{}) error

	// DeletePlugin soft‑deletes a plugin (either tenant‑level or global).
	DeletePlugin(ctx context.Context, tenantID *uint64, id string) error

	// ListStaleInstalling finds plugins that have been in the "installing" state
	// for longer than the given threshold, for the reaper to clean up.
	ListStaleInstalling(ctx context.Context, olderThan time.Time) ([]*types.TenantPlugin, error)

	// CreateSnapshot records a plugin snapshot (usually written after the
	// provider creates a billable image).
	CreateSnapshot(ctx context.Context, s *types.TenantPluginSnapshot) error

	// ListSnapshotsByPlugin returns all snapshot records for a given plugin,
	// used for audit and troubleshooting.
	ListSnapshotsByPlugin(ctx context.Context, pluginID string) ([]*types.TenantPluginSnapshot, error)

	// DeleteSnapshotRowsByPlugin removes all snapshot rows for a plugin.
	// This should be called only when the plugin is being fully removed
	// (snapshots may be retained or purged according to policy).
	DeleteSnapshotRowsByPlugin(ctx context.Context, pluginID string) error
}

type tenantPluginRepository struct {
	db *gorm.DB
}

func NewTenantPluginRepository(db *gorm.DB) TenantPluginRepository {
	return &tenantPluginRepository{
		db: db,
	}
}

func (r *tenantPluginRepository) CreatePlugin(ctx context.Context, p *types.TenantPlugin) error {
	return r.db.WithContext(ctx).Create(p).Error
}

func (r *tenantPluginRepository) GetPlugin(ctx context.Context, tenantID *uint64, id string) (*types.TenantPlugin, error) {
	var p types.TenantPlugin
	query := r.db.WithContext(ctx)
	if tenantID == nil {
		query = query.Where("tenant_id IS NULL AND id = ?", id)
	} else {
		query = query.Where("tenant_id = ? AND id = ?", *tenantID, id)
	}
	err := query.First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *tenantPluginRepository) GetPluginByPluginID(ctx context.Context, tenantID *uint64, pluginID string) (*types.TenantPlugin, error) {
	var p types.TenantPlugin
	query := r.db.WithContext(ctx)
	if tenantID == nil {
		query = query.Where("tenant_id IS NULL AND plugin_id = ?", pluginID)
	} else {
		query = query.Where("tenant_id = ? AND plugin_id = ?", *tenantID, pluginID)
	}
	err := query.First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *tenantPluginRepository) ListPluginsByTenant(ctx context.Context, tenantID uint64) ([]*types.TenantPlugin, error) {
	var list []*types.TenantPlugin
	err := r.db.WithContext(ctx).
		Where("tenant_id = ?", tenantID).
		Order("created_at ASC").
		Find(&list).Error
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (r *tenantPluginRepository) ListGlobalPlugins(ctx context.Context) ([]*types.TenantPlugin, error) {
	var list []*types.TenantPlugin
	err := r.db.WithContext(ctx).
		Where("tenant_id IS NULL").
		Order("created_at ASC").
		Find(&list).Error
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (r *tenantPluginRepository) ListReadyPlugins(ctx context.Context) ([]*types.TenantPlugin, error) {
	var list []*types.TenantPlugin
	err := r.db.WithContext(ctx).
		Where("status = ?", types.PluginStatusReady).
		Order("created_at ASC").
		Find(&list).Error
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (r *tenantPluginRepository) UpdateEndpointByPluginID(
	ctx context.Context, pluginID string, endpoint string,
) (int64, error) {
	tx := r.db.WithContext(ctx).
		Model(&types.TenantPlugin{}).
		Where("plugin_id = ?", pluginID).
		Updates(map[string]any{
			"endpoint":   endpoint,
			"updated_at": time.Now(),
		})
	return tx.RowsAffected, tx.Error
}

// UpdatePlugin updates mutable fields excluding envs and permissions.
// This mirrors UpdateSkill – we list all updatable columns explicitly.
func (r *tenantPluginRepository) UpdatePlugin(ctx context.Context, p *types.TenantPlugin) error {
	return r.db.WithContext(ctx).
		Model(&types.TenantPlugin{}).
		Where("id = ?", p.ID).
		Updates(map[string]any{
			"kind":             p.Kind,
			"channel":          p.Channel,
			"transport":        p.Transport,
			"endpoint":         p.Endpoint,
			"policy_class":     p.PolicyClass,
			"source_url":       p.SourceURL,
			"source_ref":       p.SourceRef,
			"source_sha":       p.SourceSHA,
			"image_ref":        p.ImageRef,
			"container_name":   p.ContainerName,
			"status":           p.Status,
			"installing_since": p.InstallingSince,
			"enabled":          p.Enabled,
			"error":            p.Error,
			"updated_at":       time.Now(),
		}).Error
}

func (r *tenantPluginRepository) UpdatePluginState(ctx context.Context, id string, status string, errMsg *string) error {
	updates := map[string]any{
		"status":     status,
		"updated_at": time.Now(),
	}
	if errMsg != nil {
		updates["error"] = *errMsg
	} else {
		updates["error"] = nil // clear error if nil
	}
	return r.db.WithContext(ctx).
		Model(&types.TenantPlugin{}).
		Where("id = ?", id).
		Updates(updates).Error
}

func (r *tenantPluginRepository) UpdatePluginRuntime(ctx context.Context, id string, containerName *string, endpoint *string) error {
	updates := map[string]any{
		"updated_at": time.Now(),
	}
	if containerName != nil {
		updates["container_name"] = *containerName
	} else {
		updates["container_name"] = nil
	}
	if endpoint != nil {
		updates["endpoint"] = *endpoint
	} else {
		updates["endpoint"] = nil
	}
	return r.db.WithContext(ctx).
		Model(&types.TenantPlugin{}).
		Where("id = ?", id).
		Updates(updates).Error
}

func (r *tenantPluginRepository) UpdatePluginEnvs(ctx context.Context, id string, envs map[string]string) error {
	return r.db.WithContext(ctx).
		Model(&types.TenantPlugin{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"envs":       types.JSONMap(toAnyMap(envs)),
			"updated_at": time.Now(),
		}).Error
}

func (r *tenantPluginRepository) UpdatePluginPermissions(ctx context.Context, id string, perms map[string]interface{}) error {
	return r.db.WithContext(ctx).
		Model(&types.TenantPlugin{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"permissions": types.JSONMap(perms),
			"updated_at":  time.Now(),
		}).Error
}

// DeletePlugin soft‑deletes the plugin. If tenantID is non‑nil, we also
// ensure the plugin belongs to that tenant to prevent cross‑tenant deletion.
func (r *tenantPluginRepository) DeletePlugin(ctx context.Context, tenantID *uint64, id string) error {
	query := r.db.WithContext(ctx).Where("id = ?", id)
	if tenantID != nil {
		query = query.Where("tenant_id = ?", *tenantID)
	}
	// GORM soft‑delete: sets deleted_at
	return query.Delete(&types.TenantPlugin{}).Error
}

// toAnyMap widens a string map so it can be stored through types.JSONMap.
func toAnyMap(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (r *tenantPluginRepository) ListStaleInstalling(ctx context.Context, olderThan time.Time) ([]*types.TenantPlugin, error) {
	var list []*types.TenantPlugin
	err := r.db.WithContext(ctx).
		Where("status IN ? AND installing_since IS NOT NULL AND installing_since < ?",
			[]string{types.PluginStatusInstalling, types.PluginStatusRemoving}, olderThan).
		Find(&list).Error
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (r *tenantPluginRepository) CreateSnapshot(ctx context.Context, s *types.TenantPluginSnapshot) error {
	return r.db.WithContext(ctx).Create(s).Error
}

func (r *tenantPluginRepository) ListSnapshotsByPlugin(ctx context.Context, pluginID string) ([]*types.TenantPluginSnapshot, error) {
	var list []*types.TenantPluginSnapshot
	err := r.db.WithContext(ctx).
		Where("plugin_id = ?", pluginID).
		Order("created_at ASC").
		Find(&list).Error
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (r *tenantPluginRepository) DeleteSnapshotRowsByPlugin(ctx context.Context, pluginID string) error {
	return r.db.WithContext(ctx).
		Where("plugin_id = ?", pluginID).
		Delete(&types.TenantPluginSnapshot{}).Error
}
