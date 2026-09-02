package extension

import (
	"errors"
)

func LoadManifests(dirs []string, hostVersion string, reserved map[string]struct{}) ([]*Manifest, error) {
	all, builtinErr := builtins(hostVersion)
	found, discoverErr := discover(discoverRequest{
		dirs:        dirs,
		hostVersion: hostVersion,
		reserved:    reserved,
	})
	return append(all, found...), errors.Join(builtinErr, discoverErr)
}
