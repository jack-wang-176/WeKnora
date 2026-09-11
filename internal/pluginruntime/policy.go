package pluginruntime

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// Docker networks backing the two enforceable policy classes. Both are declared
// in docker-compose.yml; the offline one carries `internal: true`, which removes
// the gateway while leaving the app — attached to both networks — able to dial
// the container.
const (
	DefaultOfflineNetwork = "WeKnora-plugin-net"
	DefaultOpenNetwork    = "WeKnora-network"

	offlineNetworkEnv = "WEKNORA_PLUGIN_NETWORK_OFFLINE"
	openNetworkEnv    = "WEKNORA_PLUGIN_NETWORK_OPEN"
)

// ErrPolicyUnsupported reports a policy class this runtime cannot enforce.
var ErrPolicyUnsupported = errors.New("pluginruntime: policy class not enforceable by the docker runtime")

// networkForPolicy maps a policy class onto a Docker network name; an empty
// policy is offline, the safe end. "scoped" has no Docker equivalent — the
// daemon filters at L3/L4 only — so it fails loudly rather than degrade to open.
func networkForPolicy(policy string) (string, error) {
	switch strings.TrimSpace(policy) {
	case types.PluginPolicyOffline, "":
		return envOr(offlineNetworkEnv, DefaultOfflineNetwork), nil
	case types.PluginPolicyOpen:
		return envOr(openNetworkEnv, DefaultOpenNetwork), nil
	case types.PluginPolicyScoped:
		return "", fmt.Errorf("%w: %q requires an L7 egress proxy", ErrPolicyUnsupported, policy)
	default:
		return "", fmt.Errorf("%w: unknown policy class %q", ErrPolicyUnsupported, policy)
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
