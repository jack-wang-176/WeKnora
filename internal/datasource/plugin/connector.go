// Package plugin adapts a datasource extension to the builtin connector
// interface, so a plugin-backed source travels the same sync pipeline as
// Feishu or Notion and nothing downstream has to know the difference.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	datasourcev1 "github.com/Tencent/WeKnora/docreader/proto/plugin/datasource"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"google.golang.org/grpc"
)

// callTimeout bounds the unary RPCs when the caller brought no deadline.
// FetchStream is left unbounded: a full wiki walk is legitimately long.
const callTimeout = 60 * time.Second

// Channel is the slice of extension.Channel this connector needs. Close() is
// left out so a borrower cannot close what the host owns, and the structural
// match keeps this package free of any import of internal/extension.
type Channel interface {
	Conn() any
	Reconnect(ctx context.Context) error
}

// Connector speaks the datasource plugin contract over gRPC.
//
// The stub is derived per call rather than cached, so a channel that dialled
// again is picked up immediately instead of being talked to on a dead
// connection.
type Connector struct {
	connectorType string
	ch            Channel
}

var (
	_ datasource.Connector          = (*Connector)(nil)
	_ datasource.StreamingConnector = (*Connector)(nil)
)

var errNoChannel = errors.New("datasource plugin has no live connection")

// New returns a connector whose Type() is the plugin id, which is what the
// data source row stores and what the resolver looks up.
func New(connectorType string, ch Channel) *Connector {
	return &Connector{connectorType: connectorType, ch: ch}
}

func (c *Connector) Type() string { return c.connectorType }

func (c *Connector) client() (datasourcev1.DataSourceClient, error) {
	if c.ch == nil {
		return nil, errNoChannel
	}
	// Asserted to the concrete type: a nil *grpc.ClientConn inside an interface
	// is not a nil interface, so a looser assertion would defer the nil to a
	// panic on the first RPC.
	conn, _ := c.ch.Conn().(*grpc.ClientConn)
	if conn == nil {
		return nil, errNoChannel
	}
	return datasourcev1.NewDataSourceClient(conn), nil
}

func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, callTimeout)
}

func (c *Connector) Validate(ctx context.Context, config *types.DataSourceConfig) error {
	client, err := c.client()
	if err != nil {
		return err
	}
	pbConfig, err := toPBConfig(config)
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	resp, err := client.Validate(ctx, &datasourcev1.ValidateRequest{Config: pbConfig})
	if err != nil {
		return fmt.Errorf("datasource plugin %s validate: %w", c.connectorType, err)
	}
	if !resp.GetOk() {
		return fmt.Errorf("datasource plugin %s rejected the configuration: %s", c.connectorType, resp.GetMessage())
	}
	return nil
}

func (c *Connector) ListResources(
	ctx context.Context, config *types.DataSourceConfig, parentID string,
) ([]types.Resource, error) {
	client, err := c.client()
	if err != nil {
		return nil, err
	}
	pbConfig, err := toPBConfig(config)
	if err != nil {
		return nil, err
	}
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	resp, err := client.ListResources(ctx, &datasourcev1.ListResourcesRequest{Config: pbConfig, ParentId: parentID})
	if err != nil {
		return nil, fmt.Errorf("datasource plugin %s list resources: %w", c.connectorType, err)
	}
	out := make([]types.Resource, 0, len(resp.GetResources()))
	for _, r := range resp.GetResources() {
		out = append(out, fromPBResource(r))
	}
	return out, nil
}

func (c *Connector) ResolveResourceAncestors(
	ctx context.Context, config *types.DataSourceConfig, resourceIDs []string,
) ([]string, error) {
	client, err := c.client()
	if err != nil {
		return nil, err
	}
	pbConfig, err := toPBConfig(config)
	if err != nil {
		return nil, err
	}
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	resp, err := client.ResolveResourceAncestors(ctx,
		&datasourcev1.ResolveAncestorsRequest{Config: pbConfig, ResourceIds: resourceIDs})
	if err != nil {
		return nil, fmt.Errorf("datasource plugin %s resolve ancestors: %w", c.connectorType, err)
	}
	return resp.GetAncestorIds(), nil
}

// FetchAll and FetchIncremental are the batch face of the one streaming RPC the
// contract has. They buffer, which is what the batch interface promises; the
// service prefers FetchStream whenever it can.
func (c *Connector) FetchAll(
	ctx context.Context, config *types.DataSourceConfig, resourceIDs []string,
) ([]types.FetchedItem, error) {
	collector := &itemCollector{}
	if _, err := c.fetchStream(ctx, config, resourceIDs, nil, collector); err != nil {
		return nil, err
	}
	return collector.items, nil
}

func (c *Connector) FetchIncremental(
	ctx context.Context, config *types.DataSourceConfig, cursor *types.SyncCursor,
) ([]types.FetchedItem, *types.SyncCursor, error) {
	collector := &itemCollector{}
	next, err := c.fetchStream(ctx, config, config.ResourceIDs, cursor, collector)
	if err != nil {
		return nil, nil, err
	}
	return collector.items, next, nil
}

func (c *Connector) FetchStream(
	ctx context.Context, config *types.DataSourceConfig,
	cursor *types.SyncCursor, h datasource.StreamHandler,
) (*types.SyncCursor, error) {
	return c.fetchStream(ctx, config, config.ResourceIDs, cursor, h)
}

// fetchStream walks the plugin's stream, handing items and checkpoints to h.
// A handler error aborts the walk: ingestion is failing, so further fetching is
// wasted work.
func (c *Connector) fetchStream(
	ctx context.Context, config *types.DataSourceConfig, resourceIDs []string,
	cursor *types.SyncCursor, h datasource.StreamHandler,
) (*types.SyncCursor, error) {
	client, err := c.client()
	if err != nil {
		return nil, err
	}
	pbConfig, err := toPBConfig(config)
	if err != nil {
		return nil, err
	}
	pbCursor, err := toPBCursor(cursor)
	if err != nil {
		return nil, err
	}
	stream, err := client.FetchStream(ctx, &datasourcev1.FetchStreamRequest{
		Config: pbConfig, ResourceIds: resourceIDs, Cursor: pbCursor,
	})
	if err != nil {
		return nil, fmt.Errorf("datasource plugin %s fetch: %w", c.connectorType, err)
	}

	// Kept until the stream ends: the last cursor the plugin sent is the one
	// the next sync resumes from, whether it arrived as a final cursor or as
	// the last checkpoint before the stream closed.
	var final *types.SyncCursor
	for {
		frame, recvErr := stream.Recv()
		if recvErr == io.EOF {
			return final, nil
		}
		if recvErr != nil {
			return nil, fmt.Errorf("datasource plugin %s fetch stream: %w", c.connectorType, recvErr)
		}
		switch payload := frame.GetPayload().(type) {
		case *datasourcev1.FetchStreamResponse_Item:
			if err := h.Emit(ctx, fromPBItem(payload.Item)); err != nil {
				return nil, err
			}
		case *datasourcev1.FetchStreamResponse_Checkpoint:
			snapshot := fromPBCursor(payload.Checkpoint)
			if err := h.Checkpoint(ctx, snapshot); err != nil {
				return nil, err
			}
			final = snapshot
		case *datasourcev1.FetchStreamResponse_FinalCursor:
			final = fromPBCursor(payload.FinalCursor)
		}
	}
}

// itemCollector turns the streaming RPC back into a slice for the batch calls.
type itemCollector struct {
	items []types.FetchedItem
}

func (c *itemCollector) Emit(_ context.Context, item types.FetchedItem) error {
	c.items = append(c.items, item)
	return nil
}

func (c *itemCollector) Checkpoint(_ context.Context, _ *types.SyncCursor) error { return nil }
