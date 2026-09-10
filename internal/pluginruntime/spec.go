package pluginruntime

import (
	"errors"
	"fmt"
	"strings"
)

// DefaultPort is the gRPC port a bundle plugin image is expected to listen on.
// It is a convention rather than a per-plugin field because the container is
// addressed by name on a dedicated network, where nothing else competes for it.
const DefaultPort = 50051

// Spec is everything the runtime needs to start one plugin container.
//
// It deliberately mentions neither types.TenantPlugin nor extension.Manifest:
// this package is a stateless Docker adapter, and keeping those types out of
// the signature is what stops it from growing a dependency on the store or the
// extension host.
type Spec struct {
	// ID is the scoped plugin id (`base--tenant`); it seeds the container name.
	ID string
	// TenantID is 0 for a process-level plugin.
	TenantID uint64
	// ImageRef must be a fully qualified tag chosen by the caller.
	ImageRef string
	// Policy is one of types.PluginPolicy*; it selects the container network.
	Policy string
	// Envs are injected verbatim. Secrets appear here and nowhere else.
	Envs map[string]string
	// Port overrides DefaultPort for images that cannot listen there.
	Port int
}

// ErrInvalidSpec marks the argument errors the Docker calls cannot recover from.
var ErrInvalidSpec = errors.New("pluginruntime: invalid spec")

// Validate rejects a spec that would produce an unusable container.
func (s Spec) Validate() error {
	if strings.TrimSpace(s.ImageRef) == "" {
		return fmt.Errorf("%w: empty image ref", ErrInvalidSpec)
	}
	if err := validatePluginID(s.ID); err != nil {
		return err
	}
	if s.Port < 0 || s.Port > 65535 {
		return fmt.Errorf("%w: port %d out of range", ErrInvalidSpec, s.Port)
	}
	return nil
}

func (s Spec) effectivePort() int {
	if s.Port > 0 {
		return s.Port
	}
	return DefaultPort
}
