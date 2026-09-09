package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// TenantPluginService is the write side of the extension plugin table: the four
// operations that change what this process is willing to connect to, plus the
// reads a caller needs to render them.
//
// Every method that addresses an existing plugin takes both a tenant and the
// stored plugin id. The pair is not redundant: the id already carries the
// tenant suffix, but the tenant argument is what the query filters on, so a
// caller holding tenant B's identity cannot act on tenant A's row by naming it.
// A nil tenant means the process-level (global) scope, which is an
// administrator's scope, not a missing value.
type TenantPluginService interface {
	// Register installs one plugin through the `endpoint` channel and makes it
	// live in the same call. A failure after the row exists leaves the row
	// behind with status=failed and a readable error rather than deleting it:
	// a deleted row has nowhere to show the reason it failed.
	Register(ctx context.Context, req *types.PluginRegisterRequest) (*types.TenantPlugin, error)

	// Uninstall removes the plugin from the host and then from the table.
	// It is idempotent against the host — an id the host does not know is the
	// state the caller asked for — but not against the table: removing a row
	// that is not there reports ErrPluginNotFound so a mistyped id is not
	// silently reported as a successful uninstall.
	Uninstall(ctx context.Context, tenantID *uint64, pluginID string) error

	// SetEnabled turns a plugin off or back on. Off means unregistered from
	// the host, not merely flagged; see the note in plugin_service.go.
	SetEnabled(ctx context.Context, tenantID *uint64, pluginID string, enabled bool) (*types.TenantPlugin, error)

	// Repoint moves a live plugin to a new address. The store write is done by
	// the host's persistence callback rather than here, because only the host
	// knows both the normalized form of the address and whether the reconnect
	// actually succeeded.
	Repoint(ctx context.Context, tenantID *uint64, pluginID string, endpoint string) (*types.TenantPlugin, error)

	// Get returns one plugin row within the given scope, or ErrPluginNotFound.
	Get(ctx context.Context, tenantID *uint64, pluginID string) (*types.TenantPlugin, error)

	// ListForTenant returns the plugins installed by one tenant. It does not
	// include process-level plugins: those are an administrator's objects, and
	// mixing them into a tenant's list would invite a tenant-scoped handler to
	// pass one of them back into Uninstall.
	ListForTenant(ctx context.Context, tenantID uint64) ([]*types.TenantPlugin, error)

	// ListGlobal returns the process-level plugins.
	ListGlobal(ctx context.Context) ([]*types.TenantPlugin, error)
}
