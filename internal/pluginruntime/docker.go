package pluginruntime

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// Container labels. The managed key must differ from the sandbox's
// com.weknora.sandbox.managed: each subsystem lists by its own label and removes
// what it does not recognise, so a shared key would make the two reap each
// other's containers.
const (
	labelManaged  = "com.weknora.plugin.managed"
	labelPluginID = "com.weknora.plugin.id"
	labelTenantID = "com.weknora.plugin.tenant"
	labelPolicy   = "com.weknora.plugin.policy"
	labelSpecHash = "com.weknora.plugin.spec-hash"
	// labelPort lets List rebuild a dial address without the originating spec.
	labelPort = "com.weknora.plugin.port"
)

const defaultDockerHost = "unix:///var/run/docker.sock"

// dockerAPI is the slice of the Engine API this package uses. It is declared
// here rather than imported because the sandbox's equivalent is package-private;
// *client.Client satisfies both structurally.
type dockerAPI interface {
	Ping(ctx context.Context, options client.PingOptions) (client.PingResult, error)
	ContainerCreate(
		ctx context.Context, options client.ContainerCreateOptions,
	) (client.ContainerCreateResult, error)
	ContainerStart(
		ctx context.Context, containerID string, options client.ContainerStartOptions,
	) (client.ContainerStartResult, error)
	ContainerInspect(
		ctx context.Context, containerID string, options client.ContainerInspectOptions,
	) (client.ContainerInspectResult, error)
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerRemove(
		ctx context.Context, containerID string, options client.ContainerRemoveOptions,
	) (client.ContainerRemoveResult, error)
	ContainerLogs(
		ctx context.Context, containerID string, options client.ContainerLogsOptions,
	) (client.ContainerLogsResult, error)
	NetworkInspect(ctx context.Context, networkID string, options client.NetworkInspectOptions) (client.NetworkInspectResult, error)
	ImageInspect(ctx context.Context, imageID string, opts ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ImagePull(ctx context.Context, refStr string, options client.ImagePullOptions) (client.ImagePullResponse, error)
}

var _ dockerAPI = (*client.Client)(nil)

// newDockerClient dials the daemon named by DOCKER_HOST. No HTTP client timeout
// is set: it would cover the whole response body and kill a cold image pull
// mid-stream. Call deadlines come from the caller's context instead.
func newDockerClient() (*client.Client, error) {
	host := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if host == "" {
		host = defaultDockerHost
	}
	built, err := client.New(client.WithHost(host))
	if err != nil {
		return nil, fmt.Errorf("pluginruntime: build docker client for %s: %w", host, err)
	}
	return built, nil
}

// ErrorKind classifies an Engine API failure by what the caller can do about it.
type ErrorKind string

const (
	ErrorKindNotFound    ErrorKind = "not_found"
	ErrorKindConflict    ErrorKind = "conflict"
	ErrorKindInvalid     ErrorKind = "invalid_request"
	ErrorKindUnavailable ErrorKind = "unavailable"
	ErrorKindTimeout     ErrorKind = "timeout"
	ErrorKindInternal    ErrorKind = "internal"
)

// Error is a classified Engine API failure. The kind is what the install flow
// branches on; the wrapped error keeps the daemon's own message for the log.
type Error struct {
	Op   string
	Kind ErrorKind
	Err  error
}

func (e *Error) Error() string {
	return fmt.Sprintf("pluginruntime: %s: %s: %v", e.Op, e.Kind, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func wrapDockerError(op string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Op: op, Kind: classifyDockerError(err), Err: err}
}

func classifyDockerError(err error) ErrorKind {
	switch {
	case errors.Is(err, context.DeadlineExceeded), cerrdefs.IsDeadlineExceeded(err):
		return ErrorKindTimeout
	case cerrdefs.IsNotFound(err):
		return ErrorKindNotFound
	case cerrdefs.IsInvalidArgument(err), cerrdefs.IsNotImplemented(err):
		return ErrorKindInvalid
	case cerrdefs.IsConflict(err), cerrdefs.IsAlreadyExists(err):
		return ErrorKindConflict
	case cerrdefs.IsUnavailable(err), cerrdefs.IsUnauthorized(err),
		cerrdefs.IsPermissionDenied(err), client.IsErrConnectionFailed(err):
		return ErrorKindUnavailable
	default:
		return ErrorKindInternal
	}
}

// IsNotFound reports whether err means "the container is gone", which every
// idempotent path treats as the postcondition already holding.
func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == ErrorKindNotFound
}

// stripLogFrames removes the 8-byte stream headers Docker prefixes to each log
// frame when the container has no TTY; without it the message an operator reads
// is interleaved with control bytes. Unframed output is returned untouched.
func stripLogFrames(data []byte) string {
	if len(data) < 8 || data[0] > 2 || data[1] != 0 || data[2] != 0 || data[3] != 0 {
		return strings.TrimSpace(string(data))
	}
	var b strings.Builder
	for len(data) >= 8 {
		size := int(binary.BigEndian.Uint32(data[4:8]))
		data = data[8:]
		if size > len(data) {
			size = len(data)
		}
		b.Write(data[:size])
		data = data[size:]
	}
	return strings.TrimSpace(b.String())
}
