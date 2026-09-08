package extension

import (
	"context"
	"strings"
	"sync"

	"google.golang.org/grpc"
)

type remoteChannel struct {
	endpoint string
	mu       sync.RWMutex
	health   healthPlan
	conn     *grpc.ClientConn
	target   string
}

var _ Channel = (*remoteChannel)(nil)
var _ interface{ SetEndpoint(string) error } = (*remoteChannel)(nil)

func newRemoteChannel(endpoint string, health healthPlan) (*remoteChannel, error) {
	target, err := ValidateGRPCEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	c := &remoteChannel{
		endpoint: strings.TrimSpace(endpoint),
		health:   health,
		target:   target,
	}
	if err := c.connect(context.Background()); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *remoteChannel) Conn() any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn
}
func (c *remoteChannel) Healthy(ctx context.Context) error {
	conn, ok := c.Conn().(*grpc.ClientConn)
	if !ok || conn == nil {
		return ErrNotConnected
	}
	return checkHealthForGrpc(ctx, conn, c.health.service)
}
func (c *remoteChannel) Reconnect(ctx context.Context) error {
	_ = c.Close()
	return c.connect(ctx)
}
func (c *remoteChannel) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

func (c *remoteChannel) connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.target == "" {
		return ErrNotConfigured
	}
	opts, err := buildDialOptions()
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(c.target, opts...)
	if err != nil {
		return err
	}
	c.conn = conn
	return nil
}

func (c *remoteChannel) Endpoint() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.endpoint
}

func (c *remoteChannel) SetEndpoint(addr string) error {
	target, err := ValidateGRPCEndpoint(addr)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endpoint = strings.TrimSpace(addr)
	c.target = target
	return nil
}
