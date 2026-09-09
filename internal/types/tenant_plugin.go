package types

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
)

type JSONB map[string]interface{}

func (j JSONB) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal(j)
}

func (j *JSONB) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return errors.New("failed to scan JSONB: invalid type")
	}
	return json.Unmarshal(bytes, j)
}

type TenantPlugin struct {
	ID              string         `gorm:"primaryKey;column:id;type:varchar(36)" json:"id"`
	TenantID        *int64         `gorm:"column:tenant_id;type:bigint;default:null" json:"tenant_id,omitempty"`
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
	Envs            JSONB          `gorm:"column:envs;type:jsonb;not null;default:'{}'" json:"envs"`
	Permissions     JSONB          `gorm:"column:permissions;type:jsonb;not null;default:'{}'" json:"permissions"`
	CreatedAt       time.Time      `gorm:"column:created_at;type:timestamptz;not null;autoCreateTime" json:"created_at"`
	UpdatedAt       time.Time      `gorm:"column:updated_at;type:timestamptz;not null;autoUpdateTime" json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"column:deleted_at;type:timestamptz;index" json:"deleted_at,omitempty"`
}

type TenantPluginSnapshot struct {
	ID        string    `gorm:"primaryKey;column:id;type:varchar(36)" json:"id"`
	TenantID  *int64    `gorm:"column:tenant_id;type:bigint;default:null" json:"tenant_id,omitempty"`
	PluginID  string    `gorm:"column:plugin_id;type:varchar(255);not null" json:"plugin_id"`
	Snapshot  JSONB     `gorm:"column:snapshot;type:jsonb;not null;default:'{}'" json:"snapshot"`
	SourceSha string    `gorm:"column:sourcesha;type:varchar(64);null" json:"source_sha"`
	CreateAt  time.Time `gorm:"column:created_at;type:timestamptz;not null;autoCreateTime" json:"created_at"`
	DeletedAt time.Time `gorm:"column:deleted_at;type:timestamptz;index" json:"deleted_at,omitempty"`
}

func (TenantPlugin) TableName() string {
	return "tenant_plugins"
}
