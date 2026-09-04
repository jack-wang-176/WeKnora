package extension

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/utils"
)

type httpChannel struct {
	mu       sync.RWMutex
	endpoint string
	conn     *httpConn
	health   healthPlan
}

type httpConn struct {
	client  *http.Client
	baseURL string
}

var _ Channel = (*httpChannel)(nil)

func (c *httpConn) Do(req *http.Request) (*http.Response, error) {
	return c.client.Do(req)
}

func (c *httpConn) BaseURL() string {
	return c.baseURL
}

func newHttpChannel(endpoint string, health healthPlan) (*httpChannel, error) {
	h := &httpChannel{
		endpoint: normalizeHTTPEndpoint(endpoint),
		health:   health,
	}
	if err := h.connect(context.Background()); err != nil {
		return nil, err
	}
	return h, nil
}

func normalizeHTTPEndpoint(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	return strings.TrimSuffix(addr, "/")
}

func (h *httpChannel) Conn() any {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.conn == nil {
		return nil
	}
	return h.conn
}

func (h *httpChannel) connect(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.endpoint == "" {
		return ErrNotConfigured
	}
	target := normalizeHTTPEndpoint(h.endpoint)
	if _, err := url.ParseRequestURI(target); err != nil {
		return err
	}
	if err := utils.ValidateURLForSSRF(target); err != nil {
		return fmt.Errorf("extension http endpoint failed SSRF validation: %w", err)
	}
	cfg := utils.DefaultSSRFSafeHTTPClientConfig()
	cfg.Timeout = 5 * time.Minute
	h.endpoint = target
	h.conn = &httpConn{
		client:  utils.NewSSRFSafeHTTPClient(cfg),
		baseURL: target,
	}
	return nil
}

func (h *httpChannel) Healthy(ctx context.Context) error {
	h.mu.RLock()
	c := h.conn
	h.mu.RUnlock()
	if c == nil {
		return ErrNotConfigured
	}

	ctx, cancel := h.health.withTimeout(ctx)
	defer cancel()
	return checkHealthForHttp(ctx, c.client, healthTarget(c.baseURL, h.health.service))
}

func healthTarget(base, svc string) string {
	svc = strings.TrimSpace(svc)
	if svc == "" {
		svc = "/health"
	}
	if !strings.HasPrefix(svc, "/") {
		svc = "/" + svc
	}
	return base + svc
}

func (h *httpChannel) Reconnect(ctx context.Context) error {
	_ = h.Close()
	return h.connect(ctx)
}

func (h *httpChannel) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn != nil {
		h.conn.client.CloseIdleConnections()
		h.conn = nil
	}
	return nil
}

func (h *httpChannel) SetEndpoint(addr string) {
	addr = normalizeHTTPEndpoint(addr)
	if addr == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.endpoint = addr
}
