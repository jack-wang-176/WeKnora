package extension

import (
	"context"
	"errors"
	"sort"
	"sync"
)

type Host interface {
	List(kind Kind) []*Manifest
	Get(id string) (*Manifest, bool)
	Open(ctx context.Context, id string) (Channel, error)
	Health(ctx context.Context, id string) Status
	HealthAll(ctx context.Context, kind Kind) map[string]Status
	Ready(ctx context.Context) Readiness
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

type Readiness struct {
	Ready    bool
	Degraded []string
	Failed   []string
	Statuses map[string]Status
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

func (h *host) openCached(m *Manifest, build func() (Channel, error)) (Channel, error) {
	h.mu.RLock()
	ch, ok := h.remote[m.Metadata.ID]
	h.mu.RUnlock()
	if ok {
		return ch, nil
	}
	ch, err := build()
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
	plan := healthPlanFrom(m)
	switch m.Runtime.Transport {
	case TransportRemoteGRPC:
		return h.openCached(m, func() (Channel, error) {
			return newRemoteChannel(m.Runtime.Endpoint, plan)
		})
	case TransportRemoteHTTP:
		return h.openCached(m, func() (Channel, error) {
			return newHttpChannel(m.Runtime.Endpoint, plan)
		})
	case TransportSubprocessGRPC:
		return nil, ErrNotServed
	default:
		return nil, ErrNotServed
	}
}

func (h *host) Health(ctx context.Context, id string) Status {
	ch, err := h.Open(ctx, id)
	if err != nil {
		return statusFromErr(err)
	}
	return statusFromErr(ch.Healthy(ctx))
}

func (h *host) HealthAll(ctx context.Context, kind Kind) map[string]Status {
	h.mu.RLock()
	ids := make([]string, 0, len(h.manifests))
	for id, m := range h.manifests {
		if kind == "" || m.Extension.Kind == kind {
			ids = append(ids, id)
		}
	}
	h.mu.RUnlock()
	out := make(map[string]Status, len(ids))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			st := h.Health(ctx, id)
			mu.Lock()
			out[id] = st
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	return out
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

func (h *host) Ready(ctx context.Context) Readiness {
	statuses := h.HealthAll(ctx, "")
	r := Readiness{
		Ready:    true,
		Statuses: statuses,
	}
	for id, st := range statuses {
		if st.State == StateNotConfigured || st.State == StateServing {
			continue
		}
		m, ok := h.Get(id)
		if !ok {
			continue
		}
		if m.IsRequired() {
			r.Failed = append(r.Failed, id)
			r.Ready = false
		} else {
			r.Degraded = append(r.Degraded, id)
		}
	}
	sort.Strings(r.Failed)
	sort.Strings(r.Degraded)
	return r
}
