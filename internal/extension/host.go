package extension

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Tencent/WeKnora/internal/utils"
)

type Host interface {
	List(kind Kind) []*Manifest
	Get(id string) (*Manifest, bool)
	Open(ctx context.Context, id string) (Channel, error)
	Health(ctx context.Context, id string) Status
	HealthAll(ctx context.Context, kind Kind) map[string]Status
	Ready(ctx context.Context) Readiness
	Close(ctx context.Context) error
	//for total manifest
	Register(ctx context.Context, manifest *Manifest) (replaced bool, err error)
	Unregister(ctx context.Context, id string) (closed bool, err error)
	Reload(ctx context.Context) (ReloadResult, error)
	CloseSingle(ctx context.Context, id string) error
	Reconnect(ctx context.Context, id string, addr string) (Status, error)
}

type Status struct {
	ID        string
	State     State
	Message   string
	Checked   bool
	Endpoint  string
	Transport Transport
}

type State string

const (
	StateServing       State = "serving"
	StateNotServing    State = "not_serving"
	StateNotConfigured State = "not_configured"
	StateUnknown       State = "unknown"
	StateUnavailable   State = "unavailable"
	StateNotFound      State = "notfound"
	StateDisabled      State = "disabled"
)

type ManifestLoader func(context.Context) ([]*Manifest, error)
type HostOption func(*host)

// EndpointPersistFunc is called by Reconnect once — and only once — it knows
// both facts nobody outside the host knows: the normalized form of the address
// and that the reconnect actually worked. It runs before the in-memory write,
// so a failure to persist leaves the stored endpoint untouched rather than
// producing a host that works until the next restart.
//
// It receives the full manifest id, tenant suffix included; the implementation
// splits it with SplitID if it needs the tenant. It must not be given a tenant
// parameter — the tenant dimension lives in the id.
type EndpointPersistFunc func(ctx context.Context, id, normalizedEndpoint string) error

// Locking discipline:
//  1. A method holding h.mu must not call any other method on h.
//  2. Helpers that require the caller to hold h.mu are named *Locked.
//  3. Channel.Close()/Reconnect() are I/O: never call them under h.mu.
//
// Rule 1 is not a style preference: sync.RWMutex is not reentrant, so an
// RLock taken while this goroutine holds Lock blocks forever — and it blocks
// while holding the write lock, which freezes every other host method with no
// error in the log.
type host struct {
	reloadmu    sync.Mutex
	hostVersion string
	mu          sync.RWMutex
	manifests   map[string]*Manifest
	remote      map[string]Channel
	loader      ManifestLoader
	reserved    map[string]struct{}
	persist     EndpointPersistFunc
}

type Readiness struct {
	Ready    bool
	Degraded []string
	Failed   []string
	Disabled []string
	Statuses map[string]Status
}

type ReloadResult struct {
	Added     []string
	Removed   []string
	Updated   []string
	Unchanged []string
	Skipped   []string
	Errors    map[string]string
}

var _ Host = (*host)(nil)

func WithManifestLoader(l ManifestLoader) HostOption {
	return func(h *host) {
		h.loader = l
	}
}

func WithReservedIDs(ids map[string]struct{}) HostOption {
	return func(h *host) {
		h.reserved = make(map[string]struct{}, len(ids))
		for id := range ids {
			h.reserved[id] = struct{}{}
		}
	}
}

// WithPersistFunc installs the endpoint-persistence callback. Leaving it out
// must stay equivalent to today's behaviour: docreader is builtin and takes its
// endpoint from configuration, so persisting a reconnect for it would make the
// next restart disagree with the deployment's own config.
func WithPersistFunc(fn EndpointPersistFunc) HostOption {
	return func(h *host) {
		h.persist = fn
	}
}

func NewHost(hostVersion string, manifests []*Manifest, opts ...HostOption) Host {
	h := &host{
		hostVersion: hostVersion,
		manifests:   make(map[string]*Manifest, len(manifests)),
		remote:      map[string]Channel{},
		reserved:    map[string]struct{}{},
	}
	for _, m := range manifests {
		if m == nil || m.Metadata.ID == "" {
			continue
		}
		h.manifests[m.Metadata.ID] = m
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *host) Get(id string) (*Manifest, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.getLocked(id)
}

// List is the administrator view: it returns every manifest, including other
// tenants'. It must not be handed to a per-tenant endpoint — use ListForTenant
// there.
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
	m, ok := h.Get(id)
	if !ok {
		return statusFromErr(ErrNotFound)
	}
	// Turned off on purpose: dialling it would spend a timeout and write a
	// "connection failed" line about something that is supposed to be quiet.
	if m.Disabled {
		return Status{
			ID: id, State: StateDisabled, Checked: true,
			Endpoint: m.Runtime.Endpoint, Transport: m.Runtime.Transport,
		}
	}
	ch, err := h.Open(ctx, id)
	if err != nil {
		st := statusFromErr(err)
		st.Endpoint = m.Runtime.Endpoint
		st.Transport = m.Runtime.Transport
		return st
	}
	st := statusFromErr(ch.Healthy(ctx))
	st.Endpoint = ch.Endpoint()
	st.Transport = m.Runtime.Transport
	return st
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

// CloseSingle drops the cached channel for id and closes it. It is the single
// owner of removals from h.remote, and it is idempotent: closing an extension
// that never connected is not an error.
func (h *host) CloseSingle(ctx context.Context, id string) error {
	h.mu.Lock()
	ch, ok := h.remote[id]
	if ok {
		delete(h.remote, id)
	}
	h.mu.Unlock()

	if !ok || ch == nil {
		return nil
	}
	if err := ch.Close(); err != nil {
		return fmt.Errorf("close channel %s: %w", id, err)
	}
	return nil
}

func (h *host) Ready(ctx context.Context) Readiness {
	// One snapshot instead of N calls to Get: N RLocks would also mean the
	// manifests could change between them, so the answer would not describe
	// any single moment.
	h.mu.RLock()
	kinds := make(map[string]bool, len(h.manifests))
	for id, m := range h.manifests {
		kinds[id] = m.IsRequired()
	}
	h.mu.RUnlock()
	statuses := h.HealthAll(ctx, "")
	r := Readiness{
		Ready:    true,
		Statuses: statuses,
	}
	for id, st := range statuses {
		// StateDisabled is an operator decision, not a health signal: mixing it
		// into Degraded turns /readyz into a yellow light that never clears,
		// and an alert nobody clears is an alert nobody reads.
		if st.State == StateDisabled {
			r.Disabled = append(r.Disabled, id)
			continue
		}
		if st.State == StateNotConfigured || st.State == StateServing {
			continue
		}
		required, known := kinds[id]
		if !known {
			continue
		}
		if required {
			r.Failed = append(r.Failed, id)
			r.Ready = false
		} else {
			r.Degraded = append(r.Degraded, id)
		}
	}
	sort.Strings(r.Failed)
	sort.Strings(r.Degraded)
	sort.Strings(r.Disabled)
	return r
}

// Register adds or replaces a dynamically registered manifest. replaced
// reports whether an entry with the same id already existed, which is what
// lets a caller tell "installed" from "updated" in the UI and in the audit log.
//
// Replacing is allowed here and refused in datasource.ConnectorRegistry on
// purpose: this registry is mutable at runtime (updating a plugin is a
// re-register), that one is built once during process start, where a duplicate
// can only be a error.
//
// Validation runs outside the lock: it only reads m plus two fields written
// once in NewHost, and a rejected manifest must not make other registrations
// wait.
func (h *host) Register(ctx context.Context, m *Manifest) (replaced bool, err error) {
	if m == nil {
		return false, ErrInvalidManifest
	}
	if err = m.Validate(h.hostVersion, h.reserved, false); err != nil {
		return false, err
	}
	m.Hash = calculateHash(m)
	id := m.Metadata.ID

	var stale Channel
	h.mu.Lock()
	cur, exists := h.manifests[id]
	if exists && cur.Builtin {
		h.mu.Unlock()
		return false, fmt.Errorf("%s: %w", id, ErrBuiltinImmutable)
	}
	if exists && isRuntimeChanged(cur, m) {
		// The cached channel was dialled from the old runtime. Leaving it in
		// place would make "the plugin address was updated" true in the
		// manifest and false on the wire.
		if ch, ok := h.remote[id]; ok {
			delete(h.remote, id)
			stale = ch
		}
	}
	h.manifests[id] = m
	h.mu.Unlock()

	if stale != nil {
		if cerr := stale.Close(); cerr != nil {
			return exists, fmt.Errorf("register %s: stale channel: %w", id, cerr)
		}
	}
	return exists, nil
}

// Unregister removes a dynamically registered manifest and closes its channel.
//
// Built-in manifests are immutable, and that is a state-machine property rather
// than a permission one: builtins are injected at compile time and never live
// in the loader's source, so Reload cannot bring one back. Removing docreader
// would leave Get returning not-found, Ready treating it as unknown-and-
// therefore-not-required — a green /readyz — and uploads failing until the
// process is restarted, which is exactly what this subsystem exists to avoid.
func (h *host) Unregister(ctx context.Context, id string) (closed bool, err error) {
	h.mu.Lock()
	m, exist := h.manifests[id]
	if !exist {
		h.mu.Unlock()
		return false, ErrNotFound
	}
	if m.Builtin {
		h.mu.Unlock()
		return false, fmt.Errorf("%s: %w", id, ErrBuiltinImmutable)
	}
	delete(h.manifests, id)
	// closed means "this call really did close a connection", so it is read in
	// the same critical section that removed the manifest rather than inferred
	// from CloseSingle's return value — that one is idempotent, so success
	// there does not imply there was anything to close.
	_, hadConn := h.remote[id]
	h.mu.Unlock()

	if cerr := h.CloseSingle(ctx, id); cerr != nil {
		return hadConn, cerr
	}
	return hadConn, nil
}

func (h *host) Reload(ctx context.Context) (ReloadResult, error) {
	if h.loader == nil {
		return ReloadResult{}, ErrNoLoader
	}
	h.reloadmu.Lock()
	defer h.reloadmu.Unlock()
	result := ReloadResult{Errors: make(map[string]string)}
	loaded, err := h.loader(ctx)
	if err != nil {
		return result, fmt.Errorf("reload: load manifests: %w", err)
	}
	// Everything up to the next h.mu.Lock() runs outside the lock: it touches
	// only the loaded manifests plus h.hostVersion / h.reserved, which are
	// written once in NewHost and read-only afterwards.
	next := make(map[string]*Manifest, len(loaded))

	for _, m := range loaded {
		if m == nil || m.Metadata.ID == "" {
			continue
		}
		id := m.Metadata.ID
		if _, dup := next[id]; dup {
			result.Skipped = append(result.Skipped, id)
			result.Errors[id] = ErrPluginRepeated.Error()
			continue
		}
		// Third argument is false, never m.Builtin: a manifest that came from
		// outside must not get to decide whether it is exempt from validation.
		// builtin is only ever set by compile-time injection.
		if err := m.Validate(h.hostVersion, h.reserved, false); err != nil {
			result.Skipped = append(result.Skipped, m.Metadata.ID)
			result.Errors[m.Metadata.ID] = err.Error()
			continue
		}
		m.Hash = calculateHash(m)
		next[id] = m
	}

	var stale []Channel

	h.mu.Lock()
	// Built-ins land in next first and cannot be shadowed by the loader. They
	// are not in the loader's source at all, so a missing id here means
	// "compile-time only", not "deleted".
	for id, cur := range h.manifests {
		if !cur.Builtin {
			continue
		}
		if _, shadowed := next[id]; shadowed {
			result.Skipped = append(result.Skipped, id)
			result.Errors[id] = ErrBuiltinImmutable.Error()
		}
		next[id] = cur
		result.Unchanged = append(result.Unchanged, id)
	}

	for id, nm := range next {
		cur, exist := h.manifests[id]
		switch {
		case cur != nil && cur.Builtin:
			// already counted as Unchanged above
		case !exist:
			result.Added = append(result.Added, id)
		case cur.Hash == nm.Hash:
			result.Unchanged = append(result.Unchanged, id)
		default:
			result.Updated = append(result.Updated, id)
			if isRuntimeChanged(cur, nm) {
				if ch, ok := h.remote[id]; ok {
					delete(h.remote, id)
					stale = append(stale, ch)
				}
			}
		}
	}

	for id := range h.manifests {
		if _, keep := next[id]; keep {
			continue
		}
		result.Removed = append(result.Removed, id)
		if ch, ok := h.remote[id]; ok {
			delete(h.remote, id)
			stale = append(stale, ch)
		}
	}

	h.manifests = next
	h.mu.Unlock()

	// Closing is I/O, so it happens outside the lock — and synchronously:
	// `go ch.Close()` races with process shutdown, and drops the error.
	for _, ch := range stale {
		if cerr := ch.Close(); cerr != nil {
			result.Errors["close:"+ch.Endpoint()] = cerr.Error()
		}
	}

	sort.Strings(result.Added)
	sort.Strings(result.Updated)
	sort.Strings(result.Removed)
	sort.Strings(result.Unchanged)
	sort.Strings(result.Skipped)
	return result, nil
}

// getLocked: it supposed to be invoke after lock suse
func (h *host) getLocked(id string) (*Manifest, bool) {
	m, ok := h.manifests[id]
	return m, ok
}

func calculateHash(m *Manifest) string {
	marshal, _ := json.Marshal(m)
	sum := md5.Sum(marshal)
	return hex.EncodeToString(sum[:])
}

func isRuntimeChanged(oldM, newM *Manifest) bool {
	if calculateRuntimeHash(oldM.Runtime) != calculateRuntimeHash(newM.Runtime) {
		return true
	}
	if calculateHealthHash(oldM.HealthCheck) != calculateHealthHash(newM.HealthCheck) {
		return true
	}
	return false
}

func calculateRuntimeHash(r Runtime) string {
	b, _ := json.Marshal(r)
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func calculateHealthHash(h HealthCheck) string {
	b, _ := json.Marshal(h)
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// Reconnect points one extension at a new endpoint and re-dials it.
//
// It mutates the channel in place rather than replacing it: callers such as
// docparser.GRPCDocumentReader hold the channel itself, so swapping the
// instance in h.remote would leave them talking on the old address while this
// call reported success.
//
// The endpoint written back here lives only in memory. A caller that needs the
// new address to survive a restart must write its own store first — see 24
// §12.6.
func (h *host) Reconnect(ctx context.Context, id string, addr string) (Status, error) {
	// getLocked, not Get: Get takes RLock itself, and recursive read locking
	// deadlocks as soon as a writer queues up between the two acquisitions.
	h.mu.RLock()
	m, ok := h.getLocked(id)
	ch := h.remote[id]
	h.mu.RUnlock()
	if !ok {
		return Status{ID: id, State: StateNotFound}, ErrNotFound
	}
	normalized, err := normalizeEndpointFor(m.Runtime.Transport, addr)
	if err != nil {
		return Status{ID: id, Transport: m.Runtime.Transport, State: StateUnavailable}, err
	}

	// Never connected: store the endpoint, then take exactly the path Open
	// takes, so there is one way to build a channel and not two.
	if ch == nil {
		// Nothing is connected yet, so the endpoint has to reach the manifest
		// before Open reads it. Persist first all the same: store, then memory.
		if err := h.persistEndpoint(ctx, id, normalized); err != nil {
			return Status{
				ID: id, Transport: m.Runtime.Transport,
				Endpoint: m.Runtime.Endpoint, State: StateUnavailable,
			}, err
		}
		h.mu.Lock()
		cur, still := h.manifests[id]
		if !still {
			h.mu.Unlock()
			return Status{ID: id, State: StateNotFound}, ErrNotFound
		}
		cur.Runtime.Endpoint = normalized
		h.mu.Unlock()

		if _, err := h.Open(ctx, id); err != nil {
			return Status{
				ID: id, Transport: m.Runtime.Transport,
				Endpoint: normalized, State: StateUnavailable,
			}, err
		}
		return h.Health(ctx, id), nil
	}

	// Structural assertion: each implementor pairs it with a compile-time
	// `var _ interface{ SetEndpoint(string) error }` so a missing method is a
	// build failure instead of a channel that silently refuses to be repointed.
	setter, canSet := ch.(interface{ SetEndpoint(string) error })
	if !canSet {
		return Status{
			ID: id, Transport: m.Runtime.Transport,
			Endpoint: ch.Endpoint(), State: StateServing,
		}, fmt.Errorf("%s: %w", id, ErrEndpointImmutable)
	}
	if err := setter.SetEndpoint(normalized); err != nil {
		return Status{ID: id, Transport: m.Runtime.Transport, State: StateUnavailable}, err
	}
	if err := ch.Reconnect(ctx); err != nil { // I/O, outside the lock
		return Status{
			ID: id, Transport: m.Runtime.Transport,
			Endpoint: normalized, State: StateUnavailable,
		}, err
	}

	// The channel is already on the new address at this point. If the store
	// refuses the write, put the channel back rather than reporting an old
	// endpoint the process is no longer talking to. One attempt, no retry loop:
	// a loop here would hold the caller while the same store keeps failing.
	if err := h.persistEndpoint(ctx, id, normalized); err != nil {
		restored := m.Runtime.Endpoint
		if rerr := setter.SetEndpoint(restored); rerr == nil {
			if rerr = ch.Reconnect(ctx); rerr != nil {
				return Status{
					ID: id, Transport: m.Runtime.Transport,
					Endpoint: restored, State: StateUnavailable,
				}, fmt.Errorf("persist endpoint: %w (restore failed: %v)", err, rerr)
			}
		}
		return Status{
			ID: id, Transport: m.Runtime.Transport,
			Endpoint: restored, State: StateUnavailable,
		}, fmt.Errorf("persist endpoint: %w", err)
	}

	h.mu.Lock()
	if cur, still := h.manifests[id]; still {
		cur.Runtime.Endpoint = normalized
	}
	h.mu.Unlock()

	return h.Health(ctx, id), nil
}

// NormalizeEndpoint normalizes an address for callers outside this package.
// Same function Reconnect uses, deliberately: a stored value the host would not
// have produced makes the manifest rebuilt after a restart differ from the live
// one, and the first Reload then drops a healthy connection.
func NormalizeEndpoint(t Transport, addr string) (string, error) {
	return normalizeEndpointFor(t, addr)
}

// normalizeEndpointFor validates addr against the transport's own grammar and
// returns the form to store in the manifest. It is the single authority for
// endpoint validation, SSRF included: a handler does not know whether an
// extension speaks gRPC or HTTP, so it cannot do this job without either
// guessing or duplicating it.
//
// What it returns is the user's endpoint, not a dial target: remoteChannel
// derives its own target in SetEndpoint, and echoing "dns:///host:port" back to
// the UI would show the operator an address they never typed.
func normalizeEndpointFor(t Transport, addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", ErrNotConfigured
	}
	switch t {
	case TransportRemoteGRPC:
		if _, err := ValidateGRPCEndpoint(addr); err != nil {
			return "", err
		}
		return addr, nil
	case TransportRemoteHTTP:
		normalized := normalizeHTTPEndpoint(addr)
		if normalized == "" {
			return "", ErrInvalidAddr
		}
		if err := utils.ValidateURLForSSRF(normalized); err != nil {
			return "", err
		}
		return normalized, nil
	default:
		// subprocess-grpc lands here on purpose: refuse before dialling
		// rather than build a channel that cannot work.
		return "", fmt.Errorf("%s: %w", t, ErrNotServed)
	}
}

// ListForTenant returns the manifests one tenant may see: host-wide ones plus
// its own. The tenant dimension is read out of the id because that is the only
// place it can live — h.remote is keyed by id, so a tenant passed as an
// argument would have two tenants sharing one connection, and with it one
// endpoint and one set of credentials.
//
// The filter has to exist before the first tenant plugin is stored, not after:
// today it is a no-op, later it is a leak that no log can reconstruct.
func (h *host) ListForTenant(kind Kind, tenantID string) []*Manifest {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*Manifest, 0, len(h.manifests))
	for id, m := range h.manifests {
		if kind != "" && m.Extension.Kind != kind {
			continue
		}
		if _, owner := SplitID(id); owner != "" && owner != tenantID {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Metadata.ID < out[j].Metadata.ID
	})
	return out
}

// persistEndpoint runs the persistence callback if one was installed. It is a
// method only so the two Reconnect branches cannot drift apart; it must never
// be called while h.mu is held, since the callback writes to a store and the
// locking discipline at the top of this file keeps I/O out from under the lock.
func (h *host) persistEndpoint(ctx context.Context, id, normalized string) error {
	if h.persist == nil {
		return nil
	}
	return h.persist(ctx, id, normalized)
}

// ReplayManifest prepares a manifest that did not come from a plugin directory
// — today, one rebuilt from a database row — so Reload can accept it. It runs
// the same two steps LoadManifests runs on disk: restricted env expansion, then
// validation.
//
// There is deliberately no builtin parameter. Validate's third argument is
// false here as it is on every other path that accepts outside data: a manifest
// assembled from stored data must not get to decide that it is exempt from the
// checks. builtin is set by compile-time injection and nowhere else.
func ReplayManifest(m *Manifest, hostVersion string, reserved map[string]struct{}) error {
	if err := expandRuntime(m); err != nil {
		return err
	}
	return m.Validate(hostVersion, reserved, false)
}
