package pluginruntime

import (
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestValidatePluginID(t *testing.T) {
	for _, id := range []string{"notion", "notion--7", "a", "a-b-c", "notion--tenant1"} {
		if err := validatePluginID(id); err != nil {
			t.Fatalf("validatePluginID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range []string{"", "Notion", "no_tion", "-notion", "notion-", "notion--", "a--b--c"} {
		if err := validatePluginID(id); !errors.Is(err, ErrInvalidSpec) {
			t.Fatalf("validatePluginID(%q) = %v, want %v", id, err, ErrInvalidSpec)
		}
	}
}

// The name is derived from the id and nothing else: that is what makes Start
// idempotent across a host restart.
func TestContainerNameRoundTrip(t *testing.T) {
	for _, id := range []string{"notion", "notion--7"} {
		name := ContainerName(id)
		got, ok := PluginIDFromContainerName(name)
		if !ok || got != id {
			t.Fatalf("PluginIDFromContainerName(%q) = (%q,%v), want (%q,true)", name, got, ok, id)
		}
		// Docker reports names with a leading slash.
		if got, ok = PluginIDFromContainerName("/" + name); !ok || got != id {
			t.Fatalf("PluginIDFromContainerName(%q) = (%q,%v), want (%q,true)", "/"+name, got, ok, id)
		}
	}
}

func TestPluginIDFromContainerNameIgnoresForeignContainers(t *testing.T) {
	for _, name := range []string{"", "/", "postgres", "weknora-app", containerPrefix, "/" + containerPrefix} {
		if id, ok := PluginIDFromContainerName(name); ok {
			t.Fatalf("PluginIDFromContainerName(%q) = (%q,true), want false", name, id)
		}
	}
}

func TestAddressDefaultsThePort(t *testing.T) {
	if got := Address("notion--7", 50055); got != "weknora-plugin-notion--7:50055" {
		t.Fatalf("Address() = %q", got)
	}
	for _, port := range []int{0, -1} {
		if got := Address("notion", port); got != "weknora-plugin-notion:50051" {
			t.Fatalf("Address(_, %d) = %q, want the default port", port, got)
		}
	}
}

func TestNetworkForPolicy(t *testing.T) {
	// An empty policy is offline, the safe end.
	for _, policy := range []string{"", types.PluginPolicyOffline, "  "} {
		got, err := networkForPolicy(policy)
		if err != nil || got != DefaultOfflineNetwork {
			t.Fatalf("networkForPolicy(%q) = (%q,%v), want the offline network", policy, got, err)
		}
	}
	if got, err := networkForPolicy(types.PluginPolicyOpen); err != nil || got != DefaultOpenNetwork {
		t.Fatalf("networkForPolicy(open) = (%q,%v), want the open network", got, err)
	}
	// "scoped" has no Docker equivalent; degrading it to open would hand the
	// plugin the whole internet.
	for _, policy := range []string{types.PluginPolicyScoped, "whatever"} {
		if _, err := networkForPolicy(policy); !errors.Is(err, ErrPolicyUnsupported) {
			t.Fatalf("networkForPolicy(%q) = %v, want %v", policy, err, ErrPolicyUnsupported)
		}
	}
}

func TestNetworkForPolicyHonoursTheEnvOverrides(t *testing.T) {
	t.Setenv(offlineNetworkEnv, "custom-offline")
	t.Setenv(openNetworkEnv, "custom-open")
	if got, _ := networkForPolicy(types.PluginPolicyOffline); got != "custom-offline" {
		t.Fatalf("networkForPolicy(offline) = %q, want custom-offline", got)
	}
	if got, _ := networkForPolicy(types.PluginPolicyOpen); got != "custom-open" {
		t.Fatalf("networkForPolicy(open) = %q, want custom-open", got)
	}
}
