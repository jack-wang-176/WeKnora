package datasource

import (
	"context"
	"sync"

	"github.com/Tencent/WeKnora/internal/types"
)

// Connector is the interface that all external data source connectors must implement.
// Each connector (Feishu, Notion, Confluence, etc.) provides an implementation of this interface.
type Connector interface {
	// Type returns the connector type identifier (e.g., "feishu", "notion")
	Type() string

	// Validate verifies that the provided configuration is valid by testing connectivity
	// and checking credentials. Returns error if validation fails.
	Validate(ctx context.Context, config *types.DataSourceConfig) error

	// ListResources lists available resources that can be synced (documents, spaces, folders, etc.)
	// Returns a list of Resource objects that the user can select for syncing.
	//
	// parentID controls lazy (on-demand) loading of hierarchical resources:
	//   - parentID == "" → return the top-level resources (e.g. Feishu wiki spaces).
	//   - parentID != "" → return only the direct children of that resource.
	// Connectors whose listing is already flat or returns the full tree in a single
	// call may ignore parentID for the root call and return an empty slice for any
	// non-empty parentID.
	ListResources(ctx context.Context, config *types.DataSourceConfig, parentID string) ([]types.Resource, error)

	// ResolveResourceAncestors resolves, for each of the given resource IDs, the
	// ExternalIDs of every ancestor whose direct children must be loaded so a
	// lazily-loaded picker can reveal a pre-existing (possibly deeply nested)
	// selection. The returned set is deduplicated and unordered.
	//
	// It exists so connectors that load their tree one level at a time (e.g. the
	// Feishu wiki) can expose, in O(depth) per selection, the path back to the
	// root without re-traversing the whole tree. Connectors that already return
	// the full tree (Notion) or a flat list (Yuque) have nothing to reveal and
	// return an empty slice.
	ResolveResourceAncestors(
		ctx context.Context, config *types.DataSourceConfig, resourceIDs []string,
	) ([]string, error)

	// FetchAll performs a full sync of the specified resources.
	// Returns all items from the given resource IDs.
	FetchAll(ctx context.Context, config *types.DataSourceConfig, resourceIDs []string) ([]types.FetchedItem, error)

	// FetchIncremental performs an incremental sync based on the provided cursor.
	// Returns items that have changed since the last sync, a new cursor for the next sync,
	// and an error if the operation fails.
	FetchIncremental(ctx context.Context, config *types.DataSourceConfig, cursor *types.SyncCursor) ([]types.FetchedItem, *types.SyncCursor, error)
}

// StreamHandler receives items and progress checkpoints emitted during a
// streaming fetch. The service implements it to ingest each item as it arrives
// (bounding memory to one item instead of the whole wiki) and to persist the
// connector cursor at page boundaries, so a sync that times out mid-traversal
// resumes from the last checkpoint instead of restarting from scratch
// (Tencent/WeKnora#2136).
type StreamHandler interface {
	// Emit ingests a single fetched item. Returning an error aborts the
	// stream: the connector stops fetching and propagates the error, since a
	// failed ingest means the sync is failing and further API calls are wasted.
	Emit(ctx context.Context, item types.FetchedItem) error

	// Checkpoint persists the cursor reached so far. The cursor is only valid
	// for the duration of the call (the connector may keep mutating its backing
	// maps afterwards), so implementations must serialize it synchronously.
	//
	// The cursor MUST be a complete resumable snapshot, not a delta: resuming
	// from it must reproduce all progress so far. This is what lets the service
	// treat a checkpoint as a safe restart point and, for a full sync, drop the
	// prior baseline without losing already-synced state.
	Checkpoint(ctx context.Context, cursor *types.SyncCursor) error
}

// StreamingConnector is an optional interface. Connectors that implement it let
// the service interleave fetch→ingest→checkpoint so a large sync persists
// incrementally and resumes after a timeout, rather than holding every item in
// memory and losing all progress on retry. Connectors that do not implement it
// fall back to FetchAll / FetchIncremental unchanged.
type StreamingConnector interface {
	Connector

	// FetchStream walks the configured resources starting from cursor (nil =
	// from the beginning / full sync), calling h.Emit for each changed item and
	// h.Checkpoint at page boundaries. It returns the final cursor for the next
	// sync. Nodes already recorded in cursor at their current edit time are
	// skipped, which is what makes a resumed sync converge.
	FetchStream(
		ctx context.Context, config *types.DataSourceConfig,
		cursor *types.SyncCursor, h StreamHandler,
	) (*types.SyncCursor, error)
}

// ConnectorRegistry manages the registration and lookup of available connectors
type ConnectorRegistry struct {
	mu         sync.RWMutex
	connectors map[string]Connector
	metadatas  map[string]ConnectorMetadata
}

// NewConnectorRegistry creates a new connector registry
func NewConnectorRegistry() *ConnectorRegistry {
	registry := &ConnectorRegistry{
		connectors: make(map[string]Connector),
		metadatas:  make(map[string]ConnectorMetadata),
	}
	for k, v := range connectorMetadataRegistry {
		registry.metadatas[k] = v
	}
	return registry
}

// Register registers a connector with the registry.
//
// A duplicate type is refused rather than replaced: this registry is built once
// at start-up from a fixed list, so a second registration can only be a wiring
// mistake, and keeping the last one would reroute every sync of that type.
func (r *ConnectorRegistry) Register(connector Connector) error {
	if connector == nil {
		return ErrConnectorNil
	}
	// Type() is third-party code: an implementation is free to take its own
	// lock, or to be slow. Calling it once out here keeps it off r.mu, so a
	// misbehaving connector cannot stall every other registry reader.
	connectorType := connector.Type()
	if connectorType == "" {
		return ErrConnectorTypeEmpty
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exist := r.connectors[connectorType]; exist {
		return ErrConnectorRegistered
	}
	r.connectors[connectorType] = connector
	return nil
}

// Get retrieves a connector by type
func (r *ConnectorRegistry) Get(connectorType string) (Connector, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	connector, exists := r.connectors[connectorType]
	if !exists {
		return nil, ErrConnectorNotFound
	}
	return connector, nil
}

// List returns all registered connector types
func (r *ConnectorRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]string, 0, len(r.connectors))
	for t := range r.connectors {
		types = append(types, t)
	}
	return types
}

// ConnectorMetadata provides metadata about available connectors
type ConnectorMetadata struct {
	Type         string   `json:"type"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Icon         string   `json:"icon,omitempty"`
	Priority     int      `json:"priority"`     // Priority order for UI display (lower = higher priority)
	AuthType     string   `json:"auth_type"`    // "oauth2", "api_key", "token", etc.
	Capabilities []string `json:"capabilities"` // "incremental", "webhook", "deletion_sync", etc.
}

// GetConnectorMetadata returns metadata for all available connectors
// This is used by the frontend to display connector options
var connectorMetadataRegistry = map[string]ConnectorMetadata{
	types.ConnectorTypeFeishu: {
		Type:         types.ConnectorTypeFeishu,
		Name:         "Feishu (飞书)",
		Description:  "Sync documents, wikis, and content from Feishu",
		Priority:     0,
		AuthType:     "oauth2",
		Capabilities: []string{"incremental", "deletion_sync"},
	},
	types.ConnectorTypeLark: {
		Type:         types.ConnectorTypeLark,
		Name:         "Lark",
		Description:  "Sync documents, wikis, and content from Lark (Feishu international)",
		Priority:     0,
		AuthType:     "oauth2",
		Capabilities: []string{"incremental", "deletion_sync"},
	},
	types.ConnectorTypeFeishuDrive: {
		Type:         types.ConnectorTypeFeishuDrive,
		Name:         "Feishu Drive (飞书云盘)",
		Description:  "Sync documents and files from a Feishu Drive folder",
		Priority:     0,
		AuthType:     "oauth2",
		Capabilities: []string{"incremental", "deletion_sync"},
	},
	types.ConnectorTypeLarkDrive: {
		Type:         types.ConnectorTypeLarkDrive,
		Name:         "Lark Drive",
		Description:  "Sync documents and files from a Lark Drive folder",
		Priority:     0,
		AuthType:     "oauth2",
		Capabilities: []string{"incremental", "deletion_sync"},
	},
	types.ConnectorTypeNotion: {
		Type:         types.ConnectorTypeNotion,
		Name:         "Notion",
		Description:  "Sync pages and databases from Notion",
		Priority:     1,
		AuthType:     "api_key",
		Capabilities: []string{"incremental"},
	},
	types.ConnectorTypeConfluence: {
		Type:         types.ConnectorTypeConfluence,
		Name:         "Confluence",
		Description:  "Sync spaces and pages from Atlassian Confluence",
		Priority:     2,
		AuthType:     "api_key",
		Capabilities: []string{"incremental"},
	},
	types.ConnectorTypeYuque: {
		Type:         types.ConnectorTypeYuque,
		Name:         "Yuque (语雀)",
		Description:  "Sync knowledge bases and documents from Yuque",
		Priority:     3,
		AuthType:     "api_key",
		Capabilities: []string{"incremental"},
	},
	types.ConnectorTypeIMA: {
		Type:         types.ConnectorTypeIMA,
		Name:         "Tencent IMA (ima.qq.com)",
		Description:  "Sync knowledge bases and documents from Tencent IMA",
		Priority:     3,
		AuthType:     "api_key",
		Capabilities: []string{"incremental", "deletion_sync"},
	},
	types.ConnectorTypeGitHub: {
		Type:         types.ConnectorTypeGitHub,
		Name:         "GitHub",
		Description:  "Sync repositories, wikis, and issues from GitHub",
		Priority:     4,
		AuthType:     "oauth2",
		Capabilities: []string{"incremental"},
	},
	types.ConnectorTypeGoogleDrive: {
		Type:         types.ConnectorTypeGoogleDrive,
		Name:         "Google Drive",
		Description:  "Sync documents and files from Google Drive",
		Priority:     5,
		AuthType:     "oauth2",
		Capabilities: []string{"incremental"},
	},
	types.ConnectorTypeOneDrive: {
		Type:         types.ConnectorTypeOneDrive,
		Name:         "OneDrive / SharePoint",
		Description:  "Sync documents and files from Microsoft OneDrive",
		Priority:     6,
		AuthType:     "oauth2",
		Capabilities: []string{"incremental"},
	},
	types.ConnectorTypeDingTalk: {
		Type:         types.ConnectorTypeDingTalk,
		Name:         "DingTalk (钉钉)",
		Description:  "Sync documents and content from DingTalk",
		Priority:     7,
		AuthType:     "api_key",
		Capabilities: []string{"incremental"},
	},
	types.ConnectorTypeWebCrawler: {
		Type:         types.ConnectorTypeWebCrawler,
		Name:         "Web Crawler (Sitemap)",
		Description:  "Crawl websites via Sitemap.xml",
		Priority:     9,
		AuthType:     "none",
		Capabilities: []string{},
	},
	types.ConnectorTypeSlack: {
		Type:         types.ConnectorTypeSlack,
		Name:         "Slack",
		Description:  "Sync channel messages and files from Slack",
		Priority:     10,
		AuthType:     "oauth2",
		Capabilities: []string{"incremental"},
	},
	types.ConnectorTypeIMAP: {
		Type:         types.ConnectorTypeIMAP,
		Name:         "Email (IMAP)",
		Description:  "Sync email content from IMAP servers",
		Priority:     11,
		AuthType:     "password",
		Capabilities: []string{},
	},
	types.ConnectorTypeRSS: {
		Type:         types.ConnectorTypeRSS,
		Name:         "RSS / Atom Feed",
		Description:  "Sync articles from RSS/Atom feeds",
		Priority:     12,
		AuthType:     "custom",
		Capabilities: []string{"incremental"},
	},
	types.ConnectorTypeGitLab: {
		Type:         types.ConnectorTypeGitLab,
		Name:         "GitLab",
		Description:  "Sync files from GitLab projects",
		Priority:     8,
		AuthType:     "token",
		Capabilities: []string{"incremental", "hierarchical"},
	},
}

// BuiltinConnectorMetadata returns the compile-time connector metadata, keyed by
// type, as copies — exporting the map itself would invite a concurrent map
// write, which is a crash. It answers "what does this build ship with", which is
// what extension.NewHost needs for its reserved ids; ListAvailableConnectors
// answers what a running registry holds.
func BuiltinConnectorMetadata() map[string]ConnectorMetadata {
	out := make(map[string]ConnectorMetadata, len(connectorMetadataRegistry))
	for k, meta := range connectorMetadataRegistry {
		cloned := meta
		if meta.Capabilities != nil {
			cloned.Capabilities = make([]string, len(meta.Capabilities))
			copy(cloned.Capabilities, meta.Capabilities)
		}
		out[k] = cloned
	}
	return out
}

// BuiltinConnectorTypes returns the connector types this build ships with.
//
// These names are reserved for the extension host: a plugin that called itself
// "feishu" would shadow a built-in in every list the UI renders, and the
// operator would have no way to tell which one a sync actually used.
func BuiltinConnectorTypes() map[string]struct{} {
	out := make(map[string]struct{}, len(connectorMetadataRegistry))
	for k := range connectorMetadataRegistry {
		out[k] = struct{}{}
	}
	return out
}

// ListAvailableConnectors returns all available connector metadata
// sorted by priority.
//
// Capabilities is copied per entry: handing back the registry's own slice would
// be a window into state r.mu guards once the call returns.
func (r *ConnectorRegistry) ListAvailableConnectors() []ConnectorMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()

	metadata := make([]ConnectorMetadata, 0, len(r.metadatas))

	for _, meta := range r.metadatas {
		clonedMeta := meta

		if meta.Capabilities != nil {
			clonedMeta.Capabilities = make([]string, len(meta.Capabilities))
			copy(clonedMeta.Capabilities, meta.Capabilities)
		}

		metadata = append(metadata, clonedMeta)
	}

	// Sort by priority (insertion sort for simplicity)
	for i := 1; i < len(metadata); i++ {
		key := metadata[i]
		j := i - 1
		for j >= 0 && metadata[j].Priority > key.Priority {
			metadata[j+1] = metadata[j]
			j--
		}
		metadata[j+1] = key
	}

	return metadata
}
