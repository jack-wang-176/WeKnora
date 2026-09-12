package extension

import (
	"context"
	"errors"
	"sort"
	"testing"
)

type fakeChannel struct {
	endpoint string
	closed   int
	health   error
}

func (c *fakeChannel) Conn() any                       { return nil }
func (c *fakeChannel) Endpoint() string                { return c.endpoint }
func (c *fakeChannel) Healthy(context.Context) error   { return c.health }
func (c *fakeChannel) Reconnect(context.Context) error { return nil }
func (c *fakeChannel) Close() error                    { c.closed++; return nil }

// inert builds a manifest whose transport is not served, so every host method
// under test reaches its own logic without opening a socket.
func inert(id string, kind Kind) *Manifest {
	m := &Manifest{Criticality: CriticalityOptional}
	m.Metadata.ID = id
	m.Extension.Kind = kind
	return m
}

func ids(ms []*Manifest) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Metadata.ID)
	}
	return out
}

func equalStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}

// One tenant must never see another tenant's plugin, and the host-wide ones
// belong to everybody.
func TestHostListForTenantSeparatesTenants(t *testing.T) {
	h := NewHost("1.0.0", []*Manifest{
		inert("shared", KindDatasource),
		inert("notion--1", KindDatasource),
		inert("notion--2", KindDatasource),
		inert("parser--1", KindDocParser),
	})
	equalStrings(t, `ListForTenant(datasource,"1")`,
		ids(h.ListForTenant(KindDatasource, "1")), []string{"notion--1", "shared"})
	equalStrings(t, `ListForTenant(datasource,"2")`,
		ids(h.ListForTenant(KindDatasource, "2")), []string{"notion--2", "shared"})
	equalStrings(t, `ListForTenant(datasource,"")`,
		ids(h.ListForTenant(KindDatasource, "")), []string{"shared"})
	equalStrings(t, `ListForTenant("","1")`,
		ids(h.ListForTenant("", "1")), []string{"notion--1", "parser--1", "shared"})
}

// List is the administrator view and deliberately crosses tenants; it must not
// be handed to a per-tenant endpoint.
func TestHostListIsTheAdministratorView(t *testing.T) {
	h := NewHost("1.0.0", []*Manifest{
		inert("shared", KindDatasource),
		inert("notion--1", KindDatasource),
		inert("notion--2", KindDatasource),
		inert("parser--1", KindDocParser),
	})
	got := ids(h.List(KindDatasource))
	sort.Strings(got)
	equalStrings(t, "List(datasource)", got, []string{"notion--1", "notion--2", "shared"})
}

func TestHostRegisterInstallsThenUpdates(t *testing.T) {
	ctx := context.Background()
	h := NewHost("1.0.0", nil)
	replaced, err := h.Register(ctx, newRemote("notion", KindDatasource))
	if err != nil || replaced {
		t.Fatalf("first Register() = (%v,%v), want (false,nil)", replaced, err)
	}
	replaced, err = h.Register(ctx, newRemote("notion", KindDatasource))
	if err != nil || !replaced {
		t.Fatalf("second Register() = (%v,%v), want (true,nil)", replaced, err)
	}
	if m, ok := h.Get("notion"); !ok || m.Hash == "" {
		t.Fatalf("Get() = (%v,%v), want a manifest carrying a hash", m, ok)
	}
}

func TestHostRegisterRejectsNilAndInvalid(t *testing.T) {
	ctx := context.Background()
	h := NewHost("1.0.0", nil)
	if _, err := h.Register(ctx, nil); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("Register(nil) = %v, want %v", err, ErrInvalidManifest)
	}
	bad := newRemote("Notion", KindDatasource)
	if _, err := h.Register(ctx, bad); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("Register(bad id) = %v, want %v", err, ErrInvalidManifest)
	}
	if _, ok := h.Get("Notion"); ok {
		t.Fatal("a rejected manifest reached the registry")
	}
}

func TestHostRegisterAndUnregisterRefuseBuiltins(t *testing.T) {
	ctx := context.Background()
	builtin := newRemote(DocreaderExtesnionID, KindDocParser)
	builtin.Builtin = true
	h := NewHost("1.0.0", []*Manifest{builtin})

	if _, err := h.Register(ctx, newRemote(DocreaderExtesnionID, KindDocParser)); !errors.Is(err, ErrBuiltinImmutable) {
		t.Fatalf("Register(builtin) = %v, want %v", err, ErrBuiltinImmutable)
	}
	if _, err := h.Unregister(ctx, DocreaderExtesnionID); !errors.Is(err, ErrBuiltinImmutable) {
		t.Fatalf("Unregister(builtin) = %v, want %v", err, ErrBuiltinImmutable)
	}
	if _, ok := h.Get(DocreaderExtesnionID); !ok {
		t.Fatal("the builtin manifest was removed")
	}
}

// Re-registering with a new address must drop the channel dialled from the old
// one, or the manifest and the wire disagree about where the plugin lives.
func TestHostRegisterEvictsTheChannelWhenTheEndpointChanges(t *testing.T) {
	ctx := context.Background()
	h := NewHost("1.0.0", nil).(*host)
	if _, err := h.Register(ctx, newRemote("notion", KindDatasource)); err != nil {
		t.Fatalf("Register() = %v", err)
	}

	kept := &fakeChannel{endpoint: "dns:///plugin:50051"}
	h.remote["notion"] = kept
	if _, err := h.Register(ctx, newRemote("notion", KindDatasource)); err != nil {
		t.Fatalf("Register(identical) = %v", err)
	}
	if kept.closed != 0 || h.remote["notion"] == nil {
		t.Fatalf("an unchanged runtime dropped the channel (closed=%d)", kept.closed)
	}

	moved := newRemote("notion", KindDatasource)
	moved.Runtime.Endpoint = "dns:///elsewhere:50052"
	if _, err := h.Register(ctx, moved); err != nil {
		t.Fatalf("Register(moved) = %v", err)
	}
	if kept.closed != 1 {
		t.Fatalf("stale channel Close() called %d times, want 1", kept.closed)
	}
	if _, ok := h.remote["notion"]; ok {
		t.Fatal("the stale channel is still cached")
	}
}

func TestHostUnregister(t *testing.T) {
	ctx := context.Background()
	h := NewHost("1.0.0", nil).(*host)
	if _, err := h.Unregister(ctx, "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Unregister(absent) = %v, want %v", err, ErrNotFound)
	}

	if _, err := h.Register(ctx, newRemote("quiet", KindDatasource)); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	closed, err := h.Unregister(ctx, "quiet")
	if err != nil || closed {
		t.Fatalf("Unregister(never connected) = (%v,%v), want (false,nil)", closed, err)
	}

	if _, err := h.Register(ctx, newRemote("live", KindDatasource)); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	ch := &fakeChannel{}
	h.remote["live"] = ch
	closed, err = h.Unregister(ctx, "live")
	if err != nil || !closed {
		t.Fatalf("Unregister(connected) = (%v,%v), want (true,nil)", closed, err)
	}
	if ch.closed != 1 {
		t.Fatalf("Close() called %d times, want 1", ch.closed)
	}
	if _, ok := h.Get("live"); ok {
		t.Fatal("the manifest survived Unregister")
	}
}

func TestHostReloadWithoutLoader(t *testing.T) {
	if _, err := NewHost("1.0.0", nil).Reload(context.Background()); !errors.Is(err, ErrNoLoader) {
		t.Fatalf("Reload() = %v, want %v", err, ErrNoLoader)
	}
}

func TestHostReloadClassifiesEveryManifest(t *testing.T) {
	ctx := context.Background()
	builtin := newRemote(DocreaderExtesnionID, KindDocParser)
	builtin.Builtin = true

	moved := newRemote("notion", KindDatasource)
	moved.Runtime.Endpoint = "dns:///elsewhere:50052"
	loaded := []*Manifest{moved, newRemote("keep", KindDatasource), newRemote("fresh", KindDatasource)}

	h := NewHost("1.0.0", []*Manifest{builtin}, WithManifestLoader(
		func(context.Context) ([]*Manifest, error) { return loaded, nil },
	)).(*host)
	for _, id := range []string{"notion", "keep", "gone"} {
		if _, err := h.Register(ctx, newRemote(id, KindDatasource)); err != nil {
			t.Fatalf("Register(%s) = %v", id, err)
		}
	}
	stale := &fakeChannel{}
	h.remote["gone"] = stale

	res, err := h.Reload(ctx)
	if err != nil {
		t.Fatalf("Reload() = %v", err)
	}
	equalStrings(t, "Added", res.Added, []string{"fresh"})
	equalStrings(t, "Updated", res.Updated, []string{"notion"})
	equalStrings(t, "Removed", res.Removed, []string{"gone"})
	equalStrings(t, "Unchanged", res.Unchanged, []string{DocreaderExtesnionID, "keep"})
	if stale.closed != 1 {
		t.Fatalf("removed plugin's channel Close() called %d times, want 1", stale.closed)
	}
}

// A loader is an outside source: a duplicate or an invalid entry is skipped
// with a reason instead of taking the whole reload down.
func TestHostReloadSkipsDuplicatesInvalidAndBuiltinShadows(t *testing.T) {
	ctx := context.Background()
	builtin := newRemote(DocreaderExtesnionID, KindDocParser)
	builtin.Builtin = true
	builtin.Metadata.Name = "compile-time docreader"

	bad := newRemote("Notion", KindDatasource)
	shadow := newRemote(DocreaderExtesnionID, KindDocParser)
	shadow.Metadata.Name = "impostor"
	loaded := []*Manifest{
		newRemote("dup", KindDatasource), newRemote("dup", KindDatasource), bad, shadow,
	}

	h := NewHost("1.0.0", []*Manifest{builtin}, WithManifestLoader(
		func(context.Context) ([]*Manifest, error) { return loaded, nil },
	))
	res, err := h.Reload(ctx)
	if err != nil {
		t.Fatalf("Reload() = %v", err)
	}
	equalStrings(t, "Skipped", res.Skipped, []string{"Notion", DocreaderExtesnionID, "dup"})
	if res.Errors["dup"] != ErrPluginRepeated.Error() {
		t.Fatalf("Errors[dup] = %q, want %q", res.Errors["dup"], ErrPluginRepeated.Error())
	}
	if res.Errors[DocreaderExtesnionID] != ErrBuiltinImmutable.Error() {
		t.Fatalf("Errors[docreader] = %q, want %q", res.Errors[DocreaderExtesnionID], ErrBuiltinImmutable.Error())
	}
	if m, _ := h.Get(DocreaderExtesnionID); m.Metadata.Name != "compile-time docreader" {
		t.Fatalf("the loader shadowed the builtin: name = %q", m.Metadata.Name)
	}
	if _, ok := h.Get("Notion"); ok {
		t.Fatal("an invalid manifest reached the registry")
	}
}

func TestHostReservedIDsBlockRegistration(t *testing.T) {
	h := NewHost("1.0.0", nil, WithReservedIDs(map[string]struct{}{"github": {}}))
	_, err := h.Register(context.Background(), newRemote("github--42", KindDatasource))
	if !errors.Is(err, ErrReservedID) {
		t.Fatalf("Register(reserved base) = %v, want %v", err, ErrReservedID)
	}
}

func TestHostHealthStates(t *testing.T) {
	ctx := context.Background()
	off := inert("off", KindDatasource)
	off.Disabled = true
	h := NewHost("1.0.0", []*Manifest{off, inert("unserved", KindDatasource)})

	if st := h.Health(ctx, "absent"); st.State != StateNotServing || st.Message != ErrNotFound.Error() {
		t.Fatalf("Health(absent) = %+v, want not_serving with the not-found message", st)
	}
	// Disabled is an operator decision: it must short-circuit before dialling.
	if st := h.Health(ctx, "off"); st.State != StateDisabled || !st.Checked {
		t.Fatalf("Health(disabled) = %+v, want disabled", st)
	}
	if st := h.Health(ctx, "unserved"); st.State != StateUnknown {
		t.Fatalf("Health(unserved transport) = %+v, want unknown", st)
	}
}

func TestHostReadyBucketsDisabledAwayFromDegraded(t *testing.T) {
	ctx := context.Background()
	required := inert("req", KindDocParser)
	required.Criticality = CriticalityRequired
	off := inert("off", KindDatasource)
	off.Disabled = true
	h := NewHost("1.0.0", []*Manifest{required, inert("opt", KindDatasource), off})

	r := h.Ready(ctx)
	if r.Ready {
		t.Fatal("Ready = true while a required extension is down")
	}
	equalStrings(t, "Failed", r.Failed, []string{"req"})
	equalStrings(t, "Degraded", r.Degraded, []string{"opt"})
	equalStrings(t, "Disabled", r.Disabled, []string{"off"})

	h2 := NewHost("1.0.0", []*Manifest{inert("opt", KindDatasource), off})
	if r2 := h2.Ready(ctx); !r2.Ready {
		t.Fatalf("Ready = false with only optional and disabled extensions: %+v", r2)
	}
}

func TestHostCloseSingleIsIdempotent(t *testing.T) {
	ctx := context.Background()
	h := NewHost("1.0.0", []*Manifest{inert("a", KindDatasource)}).(*host)
	if err := h.CloseSingle(ctx, "a"); err != nil {
		t.Fatalf("CloseSingle(never connected) = %v", err)
	}
	ch := &fakeChannel{}
	h.remote["a"] = ch
	if err := h.CloseSingle(ctx, "a"); err != nil {
		t.Fatalf("CloseSingle() = %v", err)
	}
	if err := h.CloseSingle(ctx, "a"); err != nil {
		t.Fatalf("second CloseSingle() = %v", err)
	}
	if ch.closed != 1 {
		t.Fatalf("Close() called %d times, want 1", ch.closed)
	}
}

func TestHostCloseDropsEveryChannel(t *testing.T) {
	h := NewHost("1.0.0", nil).(*host)
	a, b := &fakeChannel{}, &fakeChannel{}
	h.remote["a"], h.remote["b"] = a, b
	if err := h.Close(context.Background()); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if a.closed != 1 || b.closed != 1 || len(h.remote) != 0 {
		t.Fatalf("Close() left %d channels (a=%d b=%d)", len(h.remote), a.closed, b.closed)
	}
}

func TestHostOpenAndReconnectRefuseWhatIsNotServed(t *testing.T) {
	ctx := context.Background()
	h := NewHost("1.0.0", []*Manifest{inert("unserved", KindDatasource)})
	if _, err := h.Open(ctx, "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open(absent) = %v, want %v", err, ErrNotFound)
	}
	if _, err := h.Open(ctx, "unserved"); !errors.Is(err, ErrNotServed) {
		t.Fatalf("Open(unserved) = %v, want %v", err, ErrNotServed)
	}
	st, err := h.Reconnect(ctx, "absent", "192.0.2.10:50051")
	if !errors.Is(err, ErrNotFound) || st.State != StateNotFound {
		t.Fatalf("Reconnect(absent) = (%+v,%v), want not-found", st, err)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	withSSRFWhitelist(t)
	if _, err := NormalizeEndpoint(TransportRemoteGRPC, "  "); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("NormalizeEndpoint(blank) = %v, want %v", err, ErrNotConfigured)
	}
	// subprocess-grpc is refused before dialling rather than given a channel
	// that could never work.
	if _, err := NormalizeEndpoint(TransportSubprocessGRPC, testEndpointHost); !errors.Is(err, ErrNotServed) {
		t.Fatalf("NormalizeEndpoint(subprocess) = %v, want %v", err, ErrNotServed)
	}
	got, err := NormalizeEndpoint(TransportRemoteGRPC, testEndpointHost)
	if err != nil || got != testEndpointHost {
		t.Fatalf("NormalizeEndpoint(grpc) = (%q,%v), want the address unchanged", got, err)
	}
}
