package extension

import (
	"errors"
	"strings"
	"testing"
)

// newRemote builds the smallest manifest Validate accepts, so each case below
// mutates exactly the one field it is about.
func newRemote(id string, kind Kind) *Manifest {
	m := &Manifest{}
	m.Metadata.ID = id
	m.Extension.Kind = kind
	m.Runtime.Transport = TransportRemoteGRPC
	m.Runtime.Endpoint = "dns:///plugin:50051"
	m.Criticality = CriticalityOptional
	return m
}

func TestManifestValidateAcceptsMinimalRemotePlugin(t *testing.T) {
	m := newRemote("notion", KindDatasource)
	if err := m.Validate("1.0.0", nil, false); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if len(m.Downgraded) != 0 {
		t.Fatalf("Downgraded = %v, want empty", m.Downgraded)
	}
}

func TestManifestValidateRejects(t *testing.T) {
	reserved := map[string]struct{}{"github": {}}
	cases := []struct {
		name string
		mut  func(*Manifest)
		want error
		text string
	}{
		{name: "empty id", mut: func(m *Manifest) { m.Metadata.ID = "" }, want: ErrInvalidManifest},
		{name: "uppercase id", mut: func(m *Manifest) { m.Metadata.ID = "Notion" }, want: ErrInvalidManifest},
		{name: "underscore id", mut: func(m *Manifest) { m.Metadata.ID = "my_plugin" }, want: ErrInvalidManifest},
		{name: "two separators", mut: func(m *Manifest) { m.Metadata.ID = "a--b--c" }, want: ErrInvalidManifest},
		{name: "reserved id", mut: func(m *Manifest) { m.Metadata.ID = "github" }, want: ErrReservedID},
		{
			name: "reserved base of a scoped id",
			mut:  func(m *Manifest) { m.Metadata.ID = "github--42" },
			want: ErrReservedID,
		},
		{
			name: "unknown kind",
			mut:  func(m *Manifest) { m.Extension.Kind = "vector" },
			text: "is not in {datasource,docparser,websearch}",
		},
		{
			name: "empty kind",
			mut:  func(m *Manifest) { m.Extension.Kind = "" },
			text: "is not in {datasource,docparser,websearch}",
		},
		{name: "incompatible host", mut: func(m *Manifest) { m.Compatibility.Host = ">=2.0.0" }, want: ErrIncompatible},
		{
			name: "unparsable host range",
			mut:  func(m *Manifest) { m.Compatibility.Host = "not-a-range" },
			want: ErrIncompatible,
		},
		{name: "unknown criticality", mut: func(m *Manifest) { m.Criticality = "urgent" }, want: ErrInvalidManifest},
		{
			name: "subprocess transport",
			mut:  func(m *Manifest) { m.Runtime.Transport = TransportSubprocessGRPC },
			want: ErrInvalidManifest,
		},
		{name: "unknown transport", mut: func(m *Manifest) { m.Runtime.Transport = "carrier-pigeon" }, text: "未知"},
		{
			name: "remote without endpoint",
			mut:  func(m *Manifest) { m.Runtime.Endpoint = "" },
			text: "must provide runtime.endpoint",
		},
		{
			name: "allow list without scoped outbound",
			mut:  func(m *Manifest) { m.Permissions.Network.Allow = []string{"api.example.com"} },
			want: ErrInvalidManifest,
		},
		{
			name: "outbound none contradicted by an allow list",
			mut: func(m *Manifest) {
				m.Permissions.Network.Outbound = NetworkNone
				m.Permissions.Network.Allow = []string{"api.example.com"}
			},
			want: ErrInvalidManifest,
		},
		{
			name: "scoped outbound with an empty allow list",
			mut:  func(m *Manifest) { m.Permissions.Network.Outbound = NetworkScoped },
			want: ErrInvalidManifest,
		},
		{
			name: "scoped outbound is declared but unenforceable",
			mut: func(m *Manifest) {
				m.Permissions.Network.Outbound = NetworkScoped
				m.Permissions.Network.Allow = []string{"api.example.com"}
			},
			want: ErrUnenforceable,
		},
		{
			name: "unknown outbound class",
			mut:  func(m *Manifest) { m.Permissions.Network.Outbound = "vpn-only" },
			want: ErrInvalidManifest,
		},
		{
			name: "filesystem permissions on a remote transport",
			mut:  func(m *Manifest) { m.Permissions.Filesystem.Read = []string{"/data"} },
			want: ErrUnenforceable,
		},
		{
			name: "write permissions on a remote transport",
			mut:  func(m *Manifest) { m.Permissions.Filesystem.Write = []string{"/tmp"} },
			want: ErrUnenforceable,
		},
		{
			name: "outbound none on a remote transport",
			mut:  func(m *Manifest) { m.Permissions.Network.Outbound = NetworkNone },
			want: ErrUnenforceable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newRemote("notion", KindDatasource)
			tc.mut(m)
			err := m.Validate("1.0.0", reserved, false)
			if err == nil {
				t.Fatalf("Validate() = nil, want an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("Validate() = %v, want %v", err, tc.want)
			}
			if tc.text != "" && !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("Validate() = %v, want it to mention %q", err, tc.text)
			}
		})
	}
}

func TestManifestValidateFillsCriticalityFromKind(t *testing.T) {
	for kind, want := range map[Kind]string{
		KindDocParser:  CriticalityRequired,
		KindDatasource: CriticalityOptional,
		KindWebSearch:  CriticalityOptional,
	} {
		m := newRemote("plug", kind)
		m.Criticality = ""
		if err := m.Validate("1.0.0", nil, false); err != nil {
			t.Fatalf("%s: Validate() = %v", kind, err)
		}
		if m.Criticality != want {
			t.Fatalf("%s: Criticality = %q, want %q", kind, m.Criticality, want)
		}
		if got := m.IsRequired(); got != (want == CriticalityRequired) {
			t.Fatalf("%s: IsRequired() = %v", kind, got)
		}
	}
}

// A tenant-scoped plugin must never be able to turn /readyz red for everyone,
// and the downgrade has to be visible rather than silent.
func TestManifestValidateDowngradesScopedRequiredPlugin(t *testing.T) {
	m := newRemote("parser--42", KindDocParser)
	m.Criticality = CriticalityRequired
	if err := m.Validate("1.0.0", nil, false); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if m.Criticality != CriticalityOptional {
		t.Fatalf("Criticality = %q, want %q", m.Criticality, CriticalityOptional)
	}
	if len(m.Downgraded) != 1 || !strings.Contains(m.Downgraded[0], "criticality") {
		t.Fatalf("Downgraded = %v, want one criticality note", m.Downgraded)
	}
}

func TestManifestValidateKeepsHostWideRequiredPlugin(t *testing.T) {
	m := newRemote("parser", KindDocParser)
	m.Criticality = CriticalityRequired
	if err := m.Validate("1.0.0", nil, false); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if m.Criticality != CriticalityRequired || len(m.Downgraded) != 0 {
		t.Fatalf("Criticality = %q, Downgraded = %v", m.Criticality, m.Downgraded)
	}
}

// Only compile-time injection sets builtin, and only a builtin may take its
// endpoint from deployment configuration instead of the manifest.
func TestManifestValidateLetsBuiltinsOmitTheEndpoint(t *testing.T) {
	m := newRemote(DocreaderExtesnionID, KindDocParser)
	m.Runtime.Endpoint = ""
	if err := m.Validate("1.0.0", nil, true); err != nil {
		t.Fatalf("Validate(builtin) = %v, want nil", err)
	}
	m2 := newRemote(DocreaderExtesnionID, KindDocParser)
	m2.Runtime.Endpoint = ""
	if err := m2.Validate("1.0.0", nil, false); err == nil {
		t.Fatal("Validate(non-builtin) = nil, want an error")
	}
}

func TestManifestValidateAcceptsCompatibleHostRange(t *testing.T) {
	m := newRemote("notion", KindDatasource)
	m.Compatibility.Host = ">=1.0.0 <2.0.0"
	if err := m.Validate("v1.4.2", nil, false); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestScopedIDRoundTrip(t *testing.T) {
	cases := []struct{ id, base, tenant string }{
		{"notion", "notion", ""},
		{"notion--42", "notion", "42"},
		{"a-b-c--7", "a-b-c", "7"},
	}
	for _, tc := range cases {
		base, tenant := SplitID(tc.id)
		if base != tc.base || tenant != tc.tenant {
			t.Fatalf("SplitID(%q) = (%q,%q), want (%q,%q)", tc.id, base, tenant, tc.base, tc.tenant)
		}
		if got := ScopedID(tc.base, tc.tenant); got != tc.id {
			t.Fatalf("ScopedID(%q,%q) = %q, want %q", tc.base, tc.tenant, got, tc.id)
		}
	}
}
