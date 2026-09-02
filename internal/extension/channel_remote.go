package extension

import (
	"context"
	"strings"
	"sync"

	"google.golang.org/grpc"
)

type remoteChannel struct {
	endpoint  string
	mu        sync.Mutex
	healthSvc string
	conn      *grpc.ClientConn
}

var _ Channel = (*remoteChannel)(nil)

func newRemoteChannel(endpoint string) (*remoteChannel, error) {
	c := &remoteChannel{
		endpoint: endpoint,
	}
	if err := c.connect(context.Background()); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *remoteChannel) Conn() any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}
func (c *remoteChannel) Healthy(ctx context.Context) error {
	conn, ok := c.Conn().(grpc.ClientConnInterface)
	if !ok || conn == nil {
		return ErrNotConnected
	}
	return checkHealth(ctx, conn, c.healthSvc)
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
	if c.endpoint == "" {
		return ErrNotConfigured
	}
	opts, err := buildDialOptions()
	if err != nil {
		return err
	}
	target := c.endpoint
	if !strings.Contains(target, "://") {
		target = "dns:///" + target
	}
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return err
	}
	c.conn = conn
	return nil
}

func (c *remoteChannel) setEndpoint(addr string) {
	if strings.TrimSpace(addr) == "" {
		return
	}
	c.endpoint = addr
}
