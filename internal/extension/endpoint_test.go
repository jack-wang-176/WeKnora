package extension

import (
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/utils"
)

// TEST-NET-1 stands in for a public address: it is never routed, so the grammar
// is exercised without a DNS lookup. The whitelist is what lets a literal IP
// through ValidateURLForSSRF at all.
const testEndpointHost = "192.0.2.10:50051"

func withSSRFWhitelist(t *testing.T) {
	t.Helper()
	utils.SetSSRFWhitelistFromRaw("192.0.2.0/24")
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
}

func TestValidateGRPCEndpointNormalizes(t *testing.T) {
	withSSRFWhitelist(t)
	want := "dns:///" + testEndpointHost
	for _, raw := range []string{
		testEndpointHost,
		"dns:///" + testEndpointHost,
		"  " + testEndpointHost + "  ",
	} {
		got, err := ValidateGRPCEndpoint(raw)
		if err != nil {
			t.Fatalf("ValidateGRPCEndpoint(%q) = %v", raw, err)
		}
		if got != want {
			t.Fatalf("ValidateGRPCEndpoint(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestValidateGRPCEndpointRejects(t *testing.T) {
	withSSRFWhitelist(t)
	cases := []struct {
		name string
		raw  string
		want error
	}{
		{"empty", "", ErrNotConfigured},
		{"blank", "   ", ErrNotConfigured},
		{"internal whitespace", "192.0.2.10 :50051", ErrInvalidManifest},
		{"several addresses", "192.0.2.10:50051,192.0.2.11:50051", ErrInvalidManifest},
		{"http scheme", "http://192.0.2.10:50051", ErrInvalidManifest},
		{"grpc scheme", "grpc://192.0.2.10:50051", ErrInvalidManifest},
		{"path", "192.0.2.10:50051/search", ErrInvalidManifest},
		{"no port", "192.0.2.10", ErrInvalidManifest},
		{"no host", ":50051", ErrInvalidManifest},
		{"port zero", "192.0.2.10:0", ErrInvalidManifest},
		{"port too large", "192.0.2.10:65536", ErrInvalidManifest},
		{"port not a number", "192.0.2.10:grpc", ErrInvalidManifest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateGRPCEndpoint(tc.raw); !errors.Is(err, tc.want) {
				t.Fatalf("ValidateGRPCEndpoint(%q) = %v, want %v", tc.raw, err, tc.want)
			}
		})
	}
}

// The endpoint grammar is not the security boundary: an address that parses
// still has to clear the same SSRF whitelist as ordinary outbound traffic.
func TestValidateGRPCEndpointRunsSSRFValidation(t *testing.T) {
	withSSRFWhitelist(t)
	_, err := ValidateGRPCEndpoint("10.0.0.1:50051")
	if err == nil {
		t.Fatal("ValidateGRPCEndpoint(private address) = nil, want an error")
	}
	if !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("ValidateGRPCEndpoint() = %v, want an SSRF failure", err)
	}
}

func TestDefaultCriticality(t *testing.T) {
	if got := DefaultCriticality(KindDocParser); got != CriticalityRequired {
		t.Fatalf("DefaultCriticality(docparser) = %q", got)
	}
	for _, k := range []Kind{KindDatasource, KindWebSearch, Kind("")} {
		if got := DefaultCriticality(k); got != CriticalityOptional {
			t.Fatalf("DefaultCriticality(%q) = %q", k, got)
		}
	}
}
