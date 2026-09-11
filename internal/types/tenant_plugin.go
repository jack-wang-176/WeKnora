package types

import (
	"time"

	"gorm.io/gorm"
)

// Plugin lifecycle states. The literals match the skill install state machine, so
// the same SQL works on both tables, but the constants are separate: sharing them
// would turn "change the skill machine" into "check whether plugins break".
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

// TenantPlugin is one installed extension. TenantID is NULL for a process-level
// plugin, and Postgres leaves NULL out of unique constraints, so
// (tenant_id, plugin_id) uniqueness is two partial indexes in migration 000092.
// Queries must spell `tenant_id IS NULL` rather than compare a sentinel.
type TenantPlugin struct {
	ID       string  `gorm:"primaryKey;column:id;type:varchar(36)" json:"id"`
	TenantID *uint64 `gorm:"column:tenant_id;type:bigint;default:null" json:"tenant_id,omitempty"`
	// PluginID is the full extension id including the tenant namespace
	// (`base--tenant`, see extension.ScopedID). It is used verbatim as the key
	// for host.Register, so the base name is recomputed with extension.SplitID
	// when needed rather than stored in a second column: two columns holding
	// halves of the same fact have no answer for what to do when they disagree.
	PluginID        string     `gorm:"column:plugin_id;type:varchar(255);not null" json:"plugin_id"`
	Kind            string     `gorm:"column:kind;type:varchar(64);not null" json:"kind"`
	Channel         string     `gorm:"column:channel;type:varchar(32);not null" json:"channel"`
	Transport       string     `gorm:"column:transport;type:varchar(32);not null" json:"transport"`
	Endpoint        *string    `gorm:"column:endpoint;type:varchar(1024)" json:"endpoint,omitempty"`
	PolicyClass     string     `gorm:"column:policy_class;type:varchar(32);not null" json:"policy_class"`
	SourceURL       *string    `gorm:"column:source_url;type:varchar(1024)" json:"source_url,omitempty"`
	SourceRef       *string    `gorm:"column:source_ref;type:varchar(255)" json:"source_ref,omitempty"`
	SourceSHA       *string    `gorm:"column:source_sha;type:varchar(64)" json:"source_sha,omitempty"`
	ImageRef        *string    `gorm:"column:image_ref;type:varchar(1024)" json:"image_ref,omitempty"`
	ContainerName   *string    `gorm:"column:container_name;type:varchar(255)" json:"container_name,omitempty"`
	Status          string     `gorm:"column:status;type:varchar(32);not null;default:installing" json:"status"`
	InstallingSince *time.Time `gorm:"column:installing_since;type:timestamptz" json:"installing_since,omitempty"`
	Enabled         bool       `gorm:"column:enabled;type:bool;not null;default:true" json:"enabled"`
	Error           *string    `gorm:"column:error;type:text" json:"error,omitempty"`
	Envs            JSONMap    `gorm:"column:envs;type:jsonb;not null;default:'{}'" json:"envs"`
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

// PluginRegisterRequest installs one extension through the `endpoint` channel.
//
// PluginID is the BASE id, without a tenant suffix: the service composes the
// stored id from it and TenantID. Accepting a full id would mean re-checking it
// against the caller's tenant everywhere, and one missed path is a takeover.
type PluginRegisterRequest struct {
	// TenantID is nil for a process-level plugin.
	TenantID    *uint64           `json:"tenant_id,omitempty"`
	PluginID    string            `json:"plugin_id"`
	Kind        string            `json:"kind"`
	Transport   string            `json:"transport"`
	Endpoint    string            `json:"endpoint"`
	PolicyClass string            `json:"policy_class,omitempty"`
	Envs        map[string]string `json:"envs,omitempty"`
	// Permissions is the author's declaration, stored as received. Nothing is
	// synthesised into it: a value this code invented has no answer to "who
	// claimed this".
	Permissions map[string]any `json:"permissions,omitempty"`
}

// Where a bundle-channel plugin comes from. The type is declared by the caller
// rather than guessed from the string: a registry image ref and a git repo look
// alike, and cloning an image ref reports an error nobody can act on.
const (
	PluginSourceImage   = "image"
	PluginSourceVCS     = "vcs"
	PluginSourceArchive = "archive"
)

// PluginInstallRequest installs one extension through the `bundle` channel: this
// process builds or pulls an image and runs the container itself.
//
// PluginID is the BASE id, as in PluginRegisterRequest. Manifest is the author's
// yaml verbatim — the image is opaque until it runs, so the declaration up front
// is what rejects an illegal plugin before spending minutes on a pull.
type PluginInstallRequest struct {
	TenantID    *uint64           `json:"tenant_id,omitempty"`
	PluginID    string            `json:"plugin_id"`
	SourceType  string            `json:"source_type"`
	SourceURL   string            `json:"source_url"`
	SourceRef   string            `json:"source_ref,omitempty"`
	PolicyClass string            `json:"policy_class,omitempty"`
	Manifest    string            `json:"manifest"`
	Envs        map[string]string `json:"envs,omitempty"`
}
