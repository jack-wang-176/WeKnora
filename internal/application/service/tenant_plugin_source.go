package service

import (
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// pluginSource is the parsed, normalized form of what the caller asked to
// install. Parsing does no network I/O: a bad string must turn into a 400
// while the request is still in flight.
type pluginSource struct {
	Type string
	URL  string
	Ref  string
}

// knownVCSHosts is only used to guess when the caller left source_type empty.
// A self-hosted GitLab is not on it, which is exactly why the guess is a
// fallback and not the rule.
var knownVCSHosts = []string{"github.com", "gitlab.com", "gitee.com", "bitbucket.org"}

var archiveSuffixes = []string{".tar.gz", ".tgz", ".tar", ".zip"}

func parsePluginSource(req *types.PluginInstallRequest) (pluginSource, error) {
	raw := strings.TrimSpace(req.SourceURL)
	if raw == "" {
		return pluginSource{}, fmt.Errorf("%w: source_url is empty", ErrPluginInvalid)
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return pluginSource{}, fmt.Errorf("%w: source_url %q contains whitespace", ErrPluginInvalid, raw)
	}

	kind := strings.TrimSpace(req.SourceType)
	if kind == "" {
		kind = inferPluginSourceType(raw)
	}

	switch kind {
	case types.PluginSourceImage:
		ref, err := normalizeImageRef(raw, strings.TrimSpace(req.SourceRef))
		if err != nil {
			return pluginSource{}, err
		}
		return pluginSource{Type: types.PluginSourceImage, URL: ref}, nil
	case types.PluginSourceVCS, types.PluginSourceArchive:
		// The seam is here on purpose: building a caller-supplied Dockerfile
		// runs their code on this host, which needs a build sandbox and an
		// image-size cap that this step does not ship. Until then only
		// pre-built images can be installed.
		return pluginSource{}, fmt.Errorf(
			"%w: source_type %q is not supported yet, push an image and install it with source_type=image",
			ErrPluginInvalid, kind)
	default:
		return pluginSource{}, fmt.Errorf("%w: source_type %q is not one of {image,vcs,archive}",
			ErrPluginInvalid, kind)
	}
}

func inferPluginSourceType(raw string) string {
	lower := strings.ToLower(raw)
	for _, suffix := range archiveSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return types.PluginSourceArchive
		}
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(lower, "https://"), "http://")
	for _, host := range knownVCSHosts {
		if trimmed == host || strings.HasPrefix(trimmed, host+"/") {
			return types.PluginSourceVCS
		}
	}
	return types.PluginSourceImage
}

// normalizeImageRef folds source_ref into the ref so the rest of the flow has
// one string to carry. A ref that already names a tag or digest wins: two ways
// of saying the version that disagree is a caller bug worth reporting.
func normalizeImageRef(raw, ref string) (string, error) {
	if strings.Contains(raw, "://") {
		return "", fmt.Errorf("%w: image ref %q must not carry a scheme", ErrPluginInvalid, raw)
	}
	tagged := strings.Contains(raw, "@") || strings.Contains(lastPathSegment(raw), ":")
	switch {
	case ref == "":
	case tagged:
		return "", fmt.Errorf("%w: image ref %q already names a version, drop source_ref", ErrPluginInvalid, raw)
	case strings.HasPrefix(ref, "sha256:"):
		raw += "@" + ref
	default:
		raw += ":" + ref
	}
	return raw, nil
}

func lastPathSegment(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// imageRefIsFloating reports a ref whose contents can change under a fixed
// name. pluginruntime.Start decides "rebuild the container" by comparing image
// refs, so a floating ref means a re-install silently keeps the old container.
func imageRefIsFloating(ref string) bool {
	if strings.Contains(ref, "@sha256:") {
		return false
	}
	seg := lastPathSegment(ref)
	i := strings.LastIndex(seg, ":")
	return i < 0 || seg[i+1:] == "latest"
}
