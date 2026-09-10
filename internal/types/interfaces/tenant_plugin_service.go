package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// TenantPluginService is the write side of the plugin table: the operations
// that change what this process is willing to connect to, plus the reads.
//
// The tenant argument is not redundant with the id. The id already carries the
// tenant suffix, but the tenant is what the query filters on, so a caller
// holding tenant B's identity cannot act on tenant A's row by naming it. A nil
// tenant is the process-level scope, not a missing value.
type TenantPluginService interface {
	// Register installs one plugin through the `endpoint` channel and makes it
	// live in the same call. A failure after the row exists leaves the row
	// behind with status=failed and the reason rather than deleting it.
	Register(ctx context.Context, req *types.PluginRegisterRequest) (*types.TenantPlugin, error)

	// Uninstall removes the plugin from the host, then from the table.
	// Idempotent against the host, not against the table: a missing row is
	// ErrPluginNotFound, so a mistyped id is not reported as a success.
	Uninstall(ctx context.Context, tenantID *uint64, pluginID string) error

	// SetEnabled turns a plugin off or back on. Off means unregistered from the
	// host, not merely flagged; see plugin_service.go.
	SetEnabled(ctx context.Context, tenantID *uint64, pluginID string, enabled bool) (*types.TenantPlugin, error)

	// Repoint moves a live plugin to a new address. The store write is the
	// host's, not this layer's; see plugin_service.go.
	Repoint(ctx context.Context, tenantID *uint64, pluginID string, endpoint string) (*types.TenantPlugin, error)

	// SetEnvs replaces the stored credentials. Write-only: no read path returns
	// the values. Under the `endpoint` channel the plugin process is not ours,
	// so new values apply at the next load rather than immediately.
	SetEnvs(ctx context.Context, tenantID *uint64, pluginID string, envs map[string]string) (*types.TenantPlugin, error)

	// Get returns one plugin row within the given scope, or ErrPluginNotFound.
	Get(ctx context.Context, tenantID *uint64, pluginID string) (*types.TenantPlugin, error)

	// ListForTenant returns one tenant's plugins, process-level ones excluded:
	// mixing them in would invite a tenant-scoped handler to pass one back into
	// Uninstall.
	ListForTenant(ctx context.Context, tenantID uint64) ([]*types.TenantPlugin, error)

	// ListGlobal returns the process-level plugins.
	ListGlobal(ctx context.Context) ([]*types.TenantPlugin, error)
}
