package extension

func LoadManifests(dirs []string, hostVersion string, reserved map[string]struct{}) ([]*Manifest, error) {
	all := builtins(hostVersion)
	found, err := discover(discoverRequest{
		dirs:        dirs,
		hostVersion: hostVersion,
		reserved:    reserved,
	})
	if err != nil {
		return all, err
	}
	return append(all, found...), err
}
