package extension

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/blang/semver/v4"

	"gopkg.in/yaml.v3"
)

const PluginDirsEnv = "WEKNORA_PLUGIN_DIRS"
const DefaultPluginDir = "./plugin"
const ManifestFileName = "plugin.yaml"
const pluginEnvPrefix = "WEKNORA_PLUGIN_"
const idSep = "--"

var (
	baseIDRe = regexp.MustCompile(`^[a-z0-9](?:-?[a-z0-9]){0,48}$`)
	idRe     = regexp.MustCompile(`^[a-z0-9](?:-?[a-z0-9]){0,48}(?:--[a-z0-9]{1,36})?$`)
)

type discoverRequest struct {
	dirs        []string
	hostVersion string
	reserved    map[string]struct{}
}

func discover(input discoverRequest) ([]*Manifest, error) {
	var manifests []*Manifest
	var errorgroup []error
	for _, dir := range input.dirs {
		//todo fix below logic
		manifest, err := discoverInDirectory(dir, input.hostVersion, input.reserved)
		manifests = append(manifests, manifest...)
		if err != nil {
			errorgroup = append(errorgroup, err)
			continue
		}
	}
	return manifests, errors.Join(errorgroup...)
}

func builtins(hostVersion string) ([]*Manifest, error) {
	transport := strings.ToLower(strings.TrimSpace(os.Getenv("DOCREADER_TRANSPORT")))
	addr := strings.TrimSpace(os.Getenv("DOCREADER_ADDR"))

	tr := TransportRemoteGRPC
	if transport == "http" || transport == "https" {
		tr = TransportRemoteHTTP
		if addr != "" && !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
			addr = "http://" + addr
		}
	}
	m := &Manifest{
		Metadata: Metadata{
			ID:      "docreader",
			Name:    "inner doc parser",
			Version: hostVersion,
		},
		Extension: ExtensionSpec{
			Kind:     KindDocParser,
			Contract: "docparser.v1",
		},
		Compatibility: Compatibility{
			//todo fix this
		},
		Runtime:     Runtime{Transport: tr, Endpoint: addr},
		Criticality: CriticalityRequired,
		FallbackFor: []string{"*"},
		Builtin:     true,
	}
	var reserved map[string]struct{}
	if err := m.Validate(hostVersion, reserved, m.Builtin); err != nil {
		return nil, fmt.Errorf("builtin %s manifest is invalid: %w", m.Metadata.ID, err)
	}
	return []*Manifest{m}, nil
}

func discoverInDirectory(dir, hostVersion string, reserved map[string]struct{}) ([]*Manifest, error) {
	var manifests []*Manifest
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fail to access manifest directory: %s:%w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest directory %s: %w", dir, err)
	}
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(dir, entry.Name())
		manifestFile := filepath.Join(manifestPath, ManifestFileName)

		if _, err := os.Stat(manifestFile); os.IsNotExist(err) {
			continue
		}

		content, err := os.ReadFile(manifestFile)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		manifest, err := parseManifestFile(string(content), hostVersion, reserved)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		manifest.Dir = manifestPath
		manifests = append(manifests, manifest)
	}
	return manifests, errors.Join(errs...)
}

func restrictedExpand(s string, allow map[string]struct{}) (string, error) {
	if s == "" {
		return "", nil
	}
	var refused string
	expanded := os.Expand(s, func(name string) string {
		if refused != "" {
			return ""
		}
		if _, ok := allow[name]; ok {
			return os.Getenv(name)
		}
		if strings.HasPrefix(name, pluginEnvPrefix) {
			return os.Getenv(name)
		}
		refused = name
		return ""
	})
	if refused != "" {
		return "", fmt.Errorf("%w: $%s", ErrUnauthorizedEnv, refused)
	}
	return expanded, nil
}

// expandRuntime expands the runtime fields through restrictedExpand.
//
// It depends on nothing but the manifest itself — no m.Dir, no filesystem
// state — because the replay path runs it on manifests that came out of the
// database, where "we wrote this row ourselves" is not a reason to skip the
// allow-list.
//
// Each failure names the field it came from. The caller of the replay path
// writes this string into the plugin row's error column, and that is the only
// thing the operator gets to see: "TOKEN is not declared in permissions.secrets"
// without "runtime.endpoint:" in front of it does not say where to look.
func expandRuntime(m *Manifest) error {
	allow := make(map[string]struct{}, len(m.Permissions.Secrets))
	for _, name := range m.Permissions.Secrets {
		allow[name] = struct{}{}
	}
	endpoint, err := restrictedExpand(m.Runtime.Endpoint, allow)
	if err != nil {
		return fmt.Errorf("runtime.endpoint: %w", err)
	}
	exec, err := restrictedExpand(m.Runtime.Exec, allow)
	if err != nil {
		return fmt.Errorf("runtime.exec: %w", err)
	}
	args := make([]string, len(m.Runtime.Args))
	for i, arg := range m.Runtime.Args {
		expanded, err := restrictedExpand(arg, allow)
		if err != nil {
			return fmt.Errorf("runtime.args[%d]: %w", i, err)
		}
		args[i] = expanded
	}
	m.Runtime.Endpoint, m.Runtime.Exec, m.Runtime.Args = endpoint, exec, args
	return nil
}

func parseManifestFile(content string, hostVersion string, reserved map[string]struct{}) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal([]byte(content), &m); err != nil {
		return nil, err
	}

	if err := expandRuntime(&m); err != nil {
		return nil, err
	}
	if err := m.Validate(hostVersion, reserved, m.Builtin); err != nil {
		return nil, err
	}
	return &m, nil
}

func hostCompatible(constraint, hostVersion string) bool {
	if strings.TrimSpace(constraint) == "" {
		return true
	}
	rng, err := semver.ParseRange(constraint)
	if err != nil {
		return false
	}
	v, err := semver.Parse(strings.TrimPrefix(strings.TrimSpace(hostVersion), "v"))
	return err == nil && rng(v)
}

func ResolvePluginDirs() []string {
	raw := strings.TrimSpace(os.Getenv(PluginDirsEnv))
	if raw == "" {
		return []string{DefaultPluginDir}
	}
	var out []string
	for _, d := range strings.Split(raw, string(os.PathListSeparator)) {
		//todo add capability for windows
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return []string{DefaultPluginDir}
	}
	return out
}

func SplitID(id string) (base, tenant string) {
	if i := strings.Index(id, idSep); i >= 0 {
		return id[:i], id[i+len(idSep):]
	}
	return id, ""
}

func ScopedID(base, tenant string) string {
	if tenant == "" {
		return base
	}
	return base + idSep + tenant
}
