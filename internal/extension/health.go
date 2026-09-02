package extension

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func checkHealth(ctx context.Context, conn grpc.ClientConnInterface, svc string) error {
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

func statusFromErr(err error) Status {
	switch {
	case err == nil:
		return Status{State: StateServing, Checked: true}
	case errors.Is(err, ErrNotConfigured):
		return Status{State: StateNotConfigured, Checked: true}
	default:
		return Status{State: StateNotServing, Message: err.Error(), Checked: true}
	}
}
