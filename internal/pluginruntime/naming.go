package pluginruntime

import (
	"fmt"
	"regexp"
	"strings"
)

// containerPrefix namespaces plugin containers on the daemon. The whole name is
// derived from the plugin id, which is what makes Start idempotent: a host that
// restarts mid-install recomputes the same name and finds its own container
// instead of creating a second one.
const containerPrefix = "weknora-plugin-"

// pluginIDRe mirrors the scoped id grammar in internal/extension. That charset
// is already a subset of Docker's container-name charset, so the id is appended
// verbatim — no escaping, no hashing, and the name stays greppable in `docker ps`.
var pluginIDRe = regexp.MustCompile(`^[a-z0-9](?:-?[a-z0-9]){0,48}(?:--[a-z0-9]{1,36})?$`)

func validatePluginID(id string) error {
	if !pluginIDRe.MatchString(id) {
		return fmt.Errorf("%w: %q is not a valid scoped plugin id", ErrInvalidSpec, id)
	}
	return nil
}

// ContainerName is the deterministic container name for a plugin id.
func ContainerName(pluginID string) string {
	return containerPrefix + pluginID
}

// PluginIDFromContainerName reverses ContainerName. The second result is false
// for any container this package did not create.
func PluginIDFromContainerName(name string) (string, bool) {
	// Docker reports container names with a leading slash.
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	if !strings.HasPrefix(name, containerPrefix) {
		return "", false
	}
	id := strings.TrimPrefix(name, containerPrefix)
	if id == "" {
		return "", false
	}
	return id, true
}

// Address is the dial target for a started plugin. The app process reaches the
// container by name because both sit on the same Docker network; no host port is
// published, which is what keeps an offline plugin unreachable from outside.
func Address(pluginID string, port int) string {
	if port <= 0 {
		port = DefaultPort
	}
	return fmt.Sprintf("%s:%d", ContainerName(pluginID), port)
}
