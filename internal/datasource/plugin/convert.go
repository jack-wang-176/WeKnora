package plugin

import (
	"fmt"

	datasourcev1 "github.com/Tencent/WeKnora/docreader/proto/plugin/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// toPBStruct converts a free-form config map. Values that are not JSON-shaped
// are refused here rather than silently dropped on the wire.
func toPBStruct(m map[string]interface{}, field string) (*structpb.Struct, error) {
	if len(m) == 0 {
		return nil, nil
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil, fmt.Errorf("datasource plugin %s is not encodable: %w", field, err)
	}
	return s, nil
}

func toPBConfig(config *types.DataSourceConfig) (*datasourcev1.Config, error) {
	if config == nil {
		return nil, nil
	}
	credentials, err := toPBStruct(config.Credentials, "credentials")
	if err != nil {
		return nil, err
	}
	settings, err := toPBStruct(config.Settings, "settings")
	if err != nil {
		return nil, err
	}
	return &datasourcev1.Config{
		Type:              config.Type,
		Credentials:       credentials,
		ResourceIds:       config.ResourceIDs,
		Settings:          settings,
		MultimodalEnabled: config.MultimodalEnabled,
	}, nil
}

func toPBCursor(cursor *types.SyncCursor) (*datasourcev1.Cursor, error) {
	if cursor == nil {
		return nil, nil
	}
	connectorCursor, err := toPBStruct(cursor.ConnectorCursor, "cursor")
	if err != nil {
		return nil, err
	}
	out := &datasourcev1.Cursor{
		ConnectorCursor: connectorCursor,
		LastSchemaHash:  cursor.LastSchemaHash,
	}
	if !cursor.LastSyncTime.IsZero() {
		out.LastSyncTime = timestamppb.New(cursor.LastSyncTime)
	}
	return out, nil
}

func fromPBCursor(cursor *datasourcev1.Cursor) *types.SyncCursor {
	if cursor == nil {
		return nil
	}
	return &types.SyncCursor{
		LastSyncTime:    cursor.GetLastSyncTime().AsTime(),
		ConnectorCursor: cursor.GetConnectorCursor().AsMap(),
		LastSchemaHash:  cursor.GetLastSchemaHash(),
	}
}

func fromPBResource(r *datasourcev1.Resource) types.Resource {
	out := types.Resource{
		ExternalID:  r.GetExternalId(),
		Name:        r.GetName(),
		Type:        r.GetType(),
		Description: r.GetDescription(),
		URL:         r.GetUrl(),
		ModifiedAt:  r.GetModifiedAt().AsTime(),
		ParentID:    r.GetParentId(),
		HasChildren: r.GetHasChildren(),
	}
	if len(r.GetMetadata()) > 0 {
		out.Metadata = make(map[string]interface{}, len(r.GetMetadata()))
		for k, v := range r.GetMetadata() {
			out.Metadata[k] = v
		}
	}
	return out
}

func fromPBItem(item *datasourcev1.FetchedItem) types.FetchedItem {
	return types.FetchedItem{
		ExternalID:       item.GetExternalId(),
		Title:            item.GetTitle(),
		Content:          item.GetContent(),
		ContentType:      item.GetContentType(),
		FileName:         item.GetFileName(),
		URL:              item.GetUrl(),
		UpdatedAt:        item.GetUpdatedAt().AsTime(),
		Metadata:         item.GetMetadata(),
		IsDeleted:        item.GetIsDeleted(),
		SourceResourceID: item.GetSourceResourceId(),
		ReplacesSubtree:  item.GetReplacesSubtree(),
		SubtreeKeep:      item.GetSubtreeKeep(),
	}
}
