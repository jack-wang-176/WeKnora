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

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,49}$`)

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

func builtins(hostVersion string) []*Manifest {
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
		Criticality: "required",
		FallbackFor: []string{"*"},
		Builtin:     true,
	}
	var reserved map[string]struct{}
	if err := m.Validate(hostVersion, reserved, m.Builtin); err != nil {
		return nil
	}
	return []*Manifest{m}
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
		manifest, err := parseManifestFile(string(content), manifestPath, hostVersion, reserved)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		manifest.Dir = manifestPath
		manifests = append(manifests, manifest)
	}
	return manifests, errors.Join(errs...)
}

func parseManifestFile(content string, dir string, hostVersion string, reserved map[string]struct{}) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal([]byte(content), &m); err != nil {
		return nil, fmt.Errorf("%s illegal yaml: %w", dir, err)
	}
	m.Dir = dir
	m.Runtime.Endpoint = os.ExpandEnv(m.Runtime.Endpoint)
	m.Runtime.Exec = os.ExpandEnv(m.Runtime.Exec)
	for i, arg := range m.Runtime.Args {
		m.Runtime.Args[i] = os.ExpandEnv(arg)
	}
	if err := m.Validate(hostVersion, reserved, m.Builtin); err != nil {
		return nil, err
	}
	return &m, nil
}

func (m *Manifest) Validate(hostVersion string, reserved map[string]struct{}, builtin bool) error {
	where := m.Dir
	if !idRe.MatchString(m.Metadata.ID) {
		return fmt.Errorf("%s: metadata.id %q illegal: %w", where, m.Metadata.ID, ErrInvalidManifest)
	}
	if _, taken := reserved[m.Metadata.ID]; taken {
		return fmt.Errorf("%s: metadata.id %q  name same with inner connectorer", where, m.Metadata.ID)
	}
	switch m.Extension.Kind {
	case KindDatasource, KindDocParser, KindWebSearch:
	default:
		return fmt.Errorf("%s extension,type %q is not in {datasource,docparser,websearch}", where, m.Extension.Kind)
	}
	if !hostCompatible(m.Compatibility.Host, hostVersion) {
		return fmt.Errorf("%s: need host %q, host is %s: %w", where, m.Compatibility.Host, hostVersion, ErrIncompatible)
	}

	switch m.Runtime.Transport {
	case TransportSubprocessGRPC:
		if m.Runtime.Exec == "" {
			return fmt.Errorf("%s: subprocess transport must provide runtime.exec", where)
		}
		if err := m.checkExecInsideDir(); err != nil {
			return err
		}
	case TransportRemoteGRPC, TransportRemoteHTTP:
		if m.Runtime.Endpoint == "" && !builtin {
			return fmt.Errorf("%s: remote transport must provide runtime.endpoint", where)
		}
	default:
		return fmt.Errorf("%s: runtime.transport %q 未知", where, m.Runtime.Transport)
	}
	return nil
}

func (m *Manifest) checkExecInsideDir() error {
	if m.Builtin {
		return nil
	}
	if filepath.IsAbs(m.Runtime.Exec) {
		return fmt.Errorf("%s: runtime.exec must be revalant path,now receive absolute path %q", m.Dir, m.Runtime.Exec)
	}
	full := filepath.Join(m.Dir, m.Runtime.Exec)
	rel, err := filepath.Rel(m.Dir, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%s: runtime.exec %q jump out of plugin file", m.Dir, m.Runtime.Exec)
	}
	return nil
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
