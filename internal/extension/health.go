package extension

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

type healthPlan struct {
	service string
	timeout time.Duration
}

const defaultHealthTimeout = 3 * time.Second

func healthPlanFrom(m *Manifest) healthPlan {
	p := healthPlan{
		service: strings.TrimSpace(m.HealthCheck.Service),
		timeout: m.HealthCheck.Timeout,
	}
	if p.timeout <= 0 {
		p.timeout = defaultHealthTimeout
	}
	return p
}

func (p healthPlan) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, p.timeout)
}

func checkHealthForGrpc(ctx context.Context, conn grpc.ClientConnInterface, svc string) error {
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: svc})
	if status.Code(err) == codes.Unimplemented {
		return nil
	}
	if err != nil {
		return err
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("extension unhealthy: %s", resp.GetStatus())
	}
	return nil
}

func checkHealthForHttp(ctx context.Context, client *http.Client, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("health check failed, status: %d", resp.StatusCode)
	}
	return nil
}

func statusFromErr(err error) Status {
	switch {
	case err == nil:
		return Status{State: StateServing, Checked: true}
	case errors.Is(err, ErrNotConfigured):
		return Status{State: StateNotConfigured, Checked: true}
	case errors.Is(err, ErrNotServed):
		return Status{State: StateUnknown, Checked: true}
	default:
		return Status{State: StateNotServing, Message: err.Error(), Checked: true}
	}
}
