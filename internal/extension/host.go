package extension

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type Host interface {
	List(kind Kind) []*Manifest
	Get(id string) (*Manifest, bool)
	Open(ctx context.Context, id string) (Channel, error)
	Health(ctx context.Context, id string) Status
	Close(ctx context.Context) error
}

type Status struct {
	State   State
	Message string
	Checked bool
}

type State string

const (
	StateServing       State = "serving"
	StateNotServing    State = "not_serving"
	StateNotConfigured State = "not_configured"
	StateUnknown       State = "unknown"
)

type host struct {
	hostVersion string
	mu          sync.RWMutex
	manifests   map[string]*Manifest
	remote      map[string]Channel
}

var _ Host = (*host)(nil)

func NewHost(hostVersion string, manifests []*Manifest) Host {
	idx := make(map[string]*Manifest, len(manifests))
	for _, m := range manifests {
		idx[m.Metadata.ID] = m
	}
	return &host{
		hostVersion: hostVersion,
		manifests:   idx,
		remote:      map[string]Channel{},
	}
}

func (h *host) Get(id string) (*Manifest, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	m, ok := h.manifests[id]
	return m, ok
}

func (h *host) List(kind Kind) []*Manifest {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []*Manifest
	for _, m := range h.manifests {
		if m.Extension.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

func (h *host) openRemote(m *Manifest) (Channel, error) {
	h.mu.RLock()
	ch, ok := h.remote[m.Metadata.ID]
	h.mu.RUnlock()
	if ok {
		return ch, nil
	}
	ch, err := newRemoteChannel(m.Runtime.Endpoint)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.remote[m.Metadata.ID]; ok {
		_ = ch.Close()
		return existing, nil
	}
	h.remote[m.Metadata.ID] = ch
	return ch, nil
}

func (h *host) Open(ctx context.Context, id string) (Channel, error) {
	m, ok := h.Get(id)
	if !ok {
		return nil, ErrNotFound
	}
	switch m.Runtime.Transport {
	case TransportRemoteGRPC:
		return h.openRemote(m)
	case TransportRemoteHTTP:
		return nil, fmt.Errorf("extension %s: remote-http is not carried by the gRPC channel in v1", id)
	case TransportSubprocessGRPC:
		return nil, fmt.Errorf("extension %s subprocess-grpc not implemented in v1", id)
	default:
		return nil, fmt.Errorf("extension %s: transport not supported %q", id, m.Runtime.Transport)
	}
}

func (h *host) Health(ctx context.Context, id string) Status {
	ch, err := h.Open(ctx, id)
	if err != nil {
		return statusFromErr(err)
	}
	return statusFromErr(ch.Healthy(ctx))
}

func (h *host) Close(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	var errs []error
	for id, ch := range h.remote {
		if err := ch.Close(); err != nil {
			errs = append(errs, err)
		}
		delete(h.remote, id)
	}
	return errors.Join(errs...)
}
