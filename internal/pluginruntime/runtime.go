package pluginruntime

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// State is the runtime view of one plugin container, collapsed to the three
// cases a caller can act on.
type State string

const (
	StateRunning State = "running"
	StateStopped State = "stopped"
	StateGone    State = "gone"
)

// Instance is what the caller needs to reach a container and, later, reclaim it.
type Instance struct {
	PluginID      string
	TenantID      uint64
	ContainerName string
	ContainerID   string
	Address       string
	Image         string
	Port          int
	State         State
	CreatedAt     time.Time
}

// Runtime is the seam the install flow depends on. It is intentionally narrow:
// no method here touches tenant_plugins, the SSRF whitelist, or the extension
// host, so nothing in this package can violate the "DB first, memory second"
// ordering those subsystems maintain.
type Runtime interface {
	Start(ctx context.Context, spec Spec) (Instance, error)
	Stop(ctx context.Context, containerName string) error
	Status(ctx context.Context, containerName string) (Instance, error)
	List(ctx context.Context) ([]Instance, error)
}

// Limits caps what one plugin container may consume.
type Limits struct {
	MemoryBytes int64
	CPULimit    float64
	PidsLimit   int64
}

// DefaultLimits are deliberately below the sandbox's: a plugin serves RPCs, it
// does not run user workloads.
var DefaultLimits = Limits{MemoryBytes: 1 << 30, CPULimit: 1.0, PidsLimit: 256}

// initProcess is addressable because HostConfig.Init is a *bool, where unset
// means "follow the daemon default".
var initProcess = true

const (
	logTailLimit  = 8 << 10
	cleanupBudget = 15 * time.Second
)

// DockerRuntime implements Runtime against a Docker daemon.
type DockerRuntime struct {
	api    dockerAPI
	limits Limits
}

var _ Runtime = (*DockerRuntime)(nil)

// NewDockerRuntime connects to the daemon named by DOCKER_HOST.
func NewDockerRuntime(limits Limits) (*DockerRuntime, error) {
	api, err := newDockerClient()
	if err != nil {
		return nil, err
	}
	return newDockerRuntime(api, limits), nil
}

func newDockerRuntime(api dockerAPI, limits Limits) *DockerRuntime {
	if limits.MemoryBytes <= 0 {
		limits.MemoryBytes = DefaultLimits.MemoryBytes
	}
	if limits.CPULimit <= 0 {
		limits.CPULimit = DefaultLimits.CPULimit
	}
	if limits.PidsLimit <= 0 {
		limits.PidsLimit = DefaultLimits.PidsLimit
	}
	return &DockerRuntime{api: api, limits: limits}
}

// Ping reports whether the daemon is reachable. Callers use it to decide
// whether bundle plugins are available at all, before showing the operator a
// per-plugin failure for something that is really one missing socket.
func (r *DockerRuntime) Ping(ctx context.Context) error {
	if _, err := r.api.Ping(ctx, client.PingOptions{}); err != nil {
		return wrapDockerError("Ping", err)
	}
	return nil
}

// Start brings the plugin's container up and returns its dial address.
//
// Idempotent by construction: the container name comes from the plugin id, so a
// re-entry finds the previous container instead of making a second one. One that
// is not running, or runs a different image than the spec, is rebuilt — which
// covers crash recovery and upgrade with one rule.
func (r *DockerRuntime) Start(ctx context.Context, spec Spec) (Instance, error) {
	if err := spec.Validate(); err != nil {
		return Instance{}, err
	}
	networkName, err := networkForPolicy(spec.Policy)
	if err != nil {
		return Instance{}, err
	}

	name := ContainerName(spec.ID)
	existing, err := r.inspect(ctx, name)
	if err != nil && !IsNotFound(err) {
		return Instance{}, err
	}
	if existing.State != StateGone {
		if existing.State == StateRunning && existing.Image == spec.ImageRef {
			return existing, nil
		}
		if err := r.Stop(ctx, name); err != nil {
			return Instance{}, err
		}
	}

	if err := r.ensureImage(ctx, spec.ImageRef); err != nil {
		return Instance{}, err
	}

	created, err := r.api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:       name,
		Config:     r.containerConfig(spec),
		HostConfig: r.hostConfig(networkName),
	})
	if err != nil {
		return Instance{}, wrapDockerError("ContainerCreate", err)
	}
	if _, err := r.api.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		r.removeQuietly(ctx, created.ID)
		return Instance{}, wrapDockerError("ContainerStart", err)
	}

	// Inspect back instead of trusting the start call: a container whose
	// entrypoint dies immediately still reports a successful start, and the
	// reason exists only in its logs.
	started, err := r.inspect(ctx, name)
	if err != nil {
		return Instance{}, err
	}
	if started.State != StateRunning {
		logs := r.tailLogs(ctx, name)
		r.removeQuietly(ctx, name)
		return Instance{}, fmt.Errorf("pluginruntime: container %s exited immediately: %s", name, logs)
	}
	return started, nil
}

// Stop removes the container rather than merely stopping it: every caller wants
// the name free for the next Start, and a stopped-but-present container would
// make that Start fail on a name conflict. A container already gone is success.
func (r *DockerRuntime) Stop(ctx context.Context, containerName string) error {
	if strings.TrimSpace(containerName) == "" {
		return fmt.Errorf("%w: empty container name", ErrInvalidSpec)
	}
	_, err := r.api.ContainerRemove(ctx, containerName, client.ContainerRemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
	if wrapped := wrapDockerError("ContainerRemove", err); wrapped != nil && !IsNotFound(wrapped) {
		return wrapped
	}
	return nil
}

// Status reports one container's state. A missing container is not an error: it
// is the StateGone answer, which is exactly what reconciliation asks about.
func (r *DockerRuntime) Status(ctx context.Context, containerName string) (Instance, error) {
	inst, err := r.inspect(ctx, containerName)
	if err != nil && !IsNotFound(err) {
		return Instance{}, err
	}
	return inst, nil
}

// List returns every container this package manages on the daemon, running or
// not. The managed label is the only filter: reconciliation has to see the
// containers whose DB row was already deleted, which is what makes them orphans.
func (r *DockerRuntime) List(ctx context.Context) ([]Instance, error) {
	listed, err := r.api.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", labelManaged+"=true"),
	})
	if err != nil {
		return nil, wrapDockerError("ContainerList", err)
	}
	out := make([]Instance, 0, len(listed.Items))
	for _, item := range listed.Items {
		inst := Instance{
			ContainerID: item.ID,
			PluginID:    item.Labels[labelPluginID],
			TenantID:    parseUint(item.Labels[labelTenantID]),
			Port:        parsePort(item.Labels[labelPort]),
			Image:       item.Image,
			State:       stateOf(string(item.State)),
			CreatedAt:   time.Unix(item.Created, 0),
		}
		if len(item.Names) > 0 {
			inst.ContainerName = strings.TrimPrefix(item.Names[0], "/")
		}
		if inst.PluginID == "" {
			if id, ok := PluginIDFromContainerName(inst.ContainerName); ok {
				inst.PluginID = id
			}
		}
		if inst.PluginID != "" {
			inst.Address = Address(inst.PluginID, inst.Port)
		}
		out = append(out, inst)
	}
	return out, nil
}

func (r *DockerRuntime) inspect(ctx context.Context, containerName string) (Instance, error) {
	got, err := r.api.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{})
	if err != nil {
		return Instance{ContainerName: containerName, State: StateGone}, wrapDockerError("ContainerInspect", err)
	}
	c := got.Container
	inst := Instance{
		ContainerName: strings.TrimPrefix(c.Name, "/"),
		ContainerID:   c.ID,
		State:         StateStopped,
	}
	if c.Config != nil {
		// Config.Image is the ref the container was created with. The top-level
		// Image field is the resolved digest, which never equals a spec's tag.
		inst.Image = c.Config.Image
		inst.PluginID = c.Config.Labels[labelPluginID]
		inst.TenantID = parseUint(c.Config.Labels[labelTenantID])
		inst.Port = parsePort(c.Config.Labels[labelPort])
	}
	if c.State != nil {
		inst.State = stateOf(string(c.State.Status))
	}
	if ts, err := time.Parse(time.RFC3339Nano, c.Created); err == nil {
		inst.CreatedAt = ts
	}
	if inst.PluginID == "" {
		if id, ok := PluginIDFromContainerName(inst.ContainerName); ok {
			inst.PluginID = id
		}
	}
	if inst.PluginID != "" {
		inst.Address = Address(inst.PluginID, inst.Port)
	}
	return inst, nil
}

// ensureImage makes the image present locally. An image already on the daemon is
// not re-pulled: a bundle plugin's tag is immutable by contract, and pulling on
// every start would make plugin startup depend on registry availability.
func (r *DockerRuntime) ensureImage(ctx context.Context, ref string) error {
	if _, err := r.api.ImageInspect(ctx, ref); err == nil {
		return nil
	}
	body, err := r.api.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return wrapDockerError("ImagePull", err)
	}
	// The pull only completes once its stream is drained; closing early leaves a
	// partial image that the next inspect would happily accept.
	if body != nil {
		defer func() { _ = body.Close() }()
		if err := body.Wait(ctx); err != nil {
			return wrapDockerError("ImagePull", err)
		}
	}
	return nil
}

func (r *DockerRuntime) containerConfig(spec Spec) *container.Config {
	return &container.Config{
		Image:  spec.ImageRef,
		Env:    envSlice(spec.Envs),
		Labels: labelsFor(spec),
	}
}

func (r *DockerRuntime) hostConfig(networkName string) *container.HostConfig {
	pids := r.limits.PidsLimit
	return &container.HostConfig{
		Resources: container.Resources{
			Memory: r.limits.MemoryBytes,
			// Equal memory and memory+swap disables swap, so a runaway
			// allocation is killed instead of thrashing the host's disk.
			MemorySwap: r.limits.MemoryBytes,
			NanoCPUs:   int64(r.limits.CPULimit * 1e9),
			PidsLimit:  &pids,
		},
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
		NetworkMode: container.NetworkMode(networkName),
		Init:        &initProcess,
		// No restart policy on purpose: the daemon restarting a container behind
		// the host's back would resurrect what Stop just took down, and the DB
		// row that owns the plugin's lifecycle would never learn about it.
	}
}

// tailLogs collects the tail of a container's output, only ever to put the
// reason it died into the error an operator reads.
func (r *DockerRuntime) tailLogs(ctx context.Context, containerName string) string {
	body, err := r.api.ContainerLogs(ctx, containerName, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       "20",
	})
	if err != nil {
		return "(logs unavailable: " + err.Error() + ")"
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, logTailLimit))
	if err != nil || len(data) == 0 {
		return "(no output)"
	}
	return stripLogFrames(data)
}

// removeQuietly reclaims a container on a failure path. It detaches from the
// caller's context: the usual reason we are here is that ctx was cancelled, and
// a cleanup that inherits the cancellation leaves the container behind.
func (r *DockerRuntime) removeQuietly(ctx context.Context, ref string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupBudget)
	defer cancel()
	_, _ = r.api.ContainerRemove(cleanupCtx, ref, client.ContainerRemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
}

// stateOf collapses Docker's container states. Anything neither running nor
// removed is "stopped", because the remedy is the same: remove it and start again.
func stateOf(status string) State {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running":
		return StateRunning
	case "", "removing", "dead":
		return StateGone
	default:
		return StateStopped
	}
}

func labelsFor(spec Spec) map[string]string {
	return map[string]string{
		labelManaged:  "true",
		labelPluginID: spec.ID,
		labelTenantID: strconv.FormatUint(spec.TenantID, 10),
		labelPolicy:   strings.TrimSpace(spec.Policy),
		labelPort:     strconv.Itoa(spec.effectivePort()),
	}
}

func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	pairs := make([]string, 0, len(env))
	for key, value := range env {
		pairs = append(pairs, key+"="+value)
	}
	return pairs
}

func parseUint(raw string) uint64 {
	v, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func parsePort(raw string) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v <= 0 || v > 65535 {
		return DefaultPort
	}
	return v
}
