package types

import (
	"time"

	"gorm.io/gorm"
)

// Plugin lifecycle states. The literals match the skill install state machine
// on purpose — the same SQL an operator already knows works on both tables —
// but the constants are defined here rather than shared with the skill ones:
// sharing them turns "change the skill state machine" into "check whether it
// breaks plugins", which is exactly the coupling the separate table exists to
// avoid.
const (
	PluginStatusInstalling = "installing"
	PluginStatusReady      = "ready"
	PluginStatusFailed     = "failed"
	PluginStatusRemoving   = "removing"
)

// Channels a plugin can arrive through.
const (
	PluginChannelBuiltin  = "builtin"
	PluginChannelBundle   = "bundle"
	PluginChannelEndpoint = "endpoint"
)

// Network policy classes. These are enforced by the runtime (the container's
// network mode), not by the manifest's permission declaration — see the note
// on Permissions below.
const (
	PluginPolicyOffline = "offline"
	PluginPolicyScoped  = "scoped"
	PluginPolicyOpen    = "open"
)

// TenantPlugin is one installed extension.
//
// TenantID is NULL for a process-level plugin. Postgres leaves NULL out of
// unique constraints, so the (tenant_id, plugin_id) uniqueness is expressed as
// two partial unique indexes in migration 000092 — one for the tenant rows and
// one for the global ones. Any query that filters by tenant must therefore
// spell the NULL case out (`tenant_id IS NULL`) instead of comparing against a
// sentinel.
type TenantPlugin struct {
	ID       string  `gorm:"primaryKey;column:id;type:varchar(36)" json:"id"`
	TenantID *uint64 `gorm:"column:tenant_id;type:bigint;default:null" json:"tenant_id,omitempty"`
	// PluginID is the full extension id including the tenant namespace
	// (`base--tenant`, see extension.ScopedID). It is used verbatim as the key
	// for host.Register, so the base name is recomputed with extension.SplitID
	// when needed rather than stored in a second column: two columns holding
	// halves of the same fact have no answer for what to do when they disagree.
	PluginID        string         `gorm:"column:plugin_id;type:varchar(255);not null" json:"plugin_id"`
	Kind            string         `gorm:"column:kind;type:varchar(64);not null" json:"kind"`
	Channel         string         `gorm:"column:channel;type:varchar(32);not null" json:"channel"`
	Transport       string         `gorm:"column:transport;type:varchar(32);not null" json:"transport"`
	Endpoint        *string        `gorm:"column:endpoint;type:varchar(1024)" json:"endpoint,omitempty"`
	PolicyClass     string         `gorm:"column:policy_class;type:varchar(32);not null" json:"policy_class"`
	SourceURL       *string        `gorm:"column:source_url;type:varchar(1024)" json:"source_url,omitempty"`
	SourceRef       *string        `gorm:"column:source_ref;type:varchar(255)" json:"source_ref,omitempty"`
	SourceSHA       *string        `gorm:"column:source_sha;type:varchar(64)" json:"source_sha,omitempty"`
	ImageRef        *string        `gorm:"column:image_ref;type:varchar(1024)" json:"image_ref,omitempty"`
	ContainerName   *string        `gorm:"column:container_name;type:varchar(255)" json:"container_name,omitempty"`
	Status          string         `gorm:"column:status;type:varchar(32);not null;default:installing" json:"status"`
	InstallingSince *time.Time     `gorm:"column:installing_since;type:timestamptz" json:"installing_since,omitempty"`
	Enabled         bool           `gorm:"column:enabled;type:bool;not null;default:true" json:"enabled"`
	Error           *string        `gorm:"column:error;type:text" json:"error,omitempty"`
	Envs            JSONMap        `gorm:"column:envs;type:jsonb;not null;default:'{}'" json:"envs"`
	// Permissions is the author's declaration, stored verbatim for audit.
	// Verbatim means it carries no "already validated" marker: the replay path
	// re-runs Manifest.Validate on every read, and a marker here would invite
	// something to skip that.
	Permissions JSONMap        `gorm:"column:permissions;type:jsonb;not null;default:'{}'" json:"permissions"`
	CreatedAt   time.Time      `gorm:"column:created_at;type:timestamptz;not null;autoCreateTime" json:"created_at"`
	UpdatedAt   time.Time      `gorm:"column:updated_at;type:timestamptz;not null;autoUpdateTime" json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"column:deleted_at;type:timestamptz;index" json:"deleted_at,omitempty"`
}

// TenantPluginSnapshot is the image ledger, same shape as the skill snapshot
// table so the reconciliation logic can be reused.
type TenantPluginSnapshot struct {
	ID        string         `gorm:"primaryKey;column:id;type:varchar(36)" json:"id"`
	TenantID  *uint64        `gorm:"column:tenant_id;type:bigint;default:null" json:"tenant_id,omitempty"`
	PluginID  string         `gorm:"column:plugin_id;type:varchar(255);not null" json:"plugin_id"`
	Snapshot  JSONMap        `gorm:"column:snapshot;type:jsonb;not null;default:'{}'" json:"snapshot"`
	SourceSHA string         `gorm:"column:source_sha;type:varchar(64)" json:"source_sha"`
	CreatedAt time.Time      `gorm:"column:created_at;type:timestamptz;not null;autoCreateTime" json:"created_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;type:timestamptz;index" json:"deleted_at,omitempty"`
}

func (TenantPlugin) TableName() string {
	return "tenant_plugins"
}

func (TenantPluginSnapshot) TableName() string {
	return "tenant_plugin_snapshots"
}
