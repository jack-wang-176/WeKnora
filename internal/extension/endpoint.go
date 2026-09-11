package extension

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/utils"
)

const grpcDialPrefix = "dns:///"

func ValidateGRPCEndpoint(raw string) (string, error) {
	addr := strings.TrimSpace(raw)
	if addr == "" {
		return "", ErrNotConfigured
	}
	if strings.ContainsAny(addr, " \t\r\n") {
		return "", fmt.Errorf("%w: grpc endpoint %q contains whitespace", ErrInvalidManifest, raw)
	}
	if strings.Contains(addr, ",") {
		return "", fmt.Errorf("%w: grpc endpoint %q lists several addresses", ErrInvalidManifest, raw)
	}

	hostport := addr
	if strings.Contains(addr, "://") {
		if !strings.HasPrefix(strings.ToLower(addr), grpcDialPrefix) {
			return "", fmt.Errorf(
				"%w: grpc endpoint %q must be a bare host:port or %shost:port",
				ErrInvalidManifest, raw, grpcDialPrefix,
			)
		}
		hostport = addr[len(grpcDialPrefix):]
	}
	if strings.Contains(hostport, "/") {
		return "", fmt.Errorf("%w: grpc endpoint %q has a path", ErrInvalidManifest, raw)
	}

	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return "", fmt.Errorf("%w: grpc endpoint %q needs host:port (%v)", ErrInvalidManifest, raw, err)
	}
	if host == "" {
		return "", fmt.Errorf("%w: grpc endpoint %q has no host", ErrInvalidManifest, raw)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("%w: grpc endpoint %q has an invalid port %q", ErrInvalidManifest, raw, port)
	}

	if err := utils.ValidateURLForSSRF(hostport); err != nil {
		return "", fmt.Errorf("extension grpc endpoint failed SSRF validation: %w", err)
	}
	return grpcDialPrefix + hostport, nil
}

func DefaultCriticality(kind Kind) string {
	switch kind {
	case KindDocParser:
		return CriticalityRequired
	default:
		return CriticalityOptional
	}
}

func (m *Manifest) Validate(hostVersion string, reserved map[string]struct{}, builtin bool) error {
	where := m.Dir
	if where == "" {
		where = m.Metadata.ID
	}
	if !idRe.MatchString(m.Metadata.ID) {
		return fmt.Errorf("%s: metadata.id %q illegal: %w", where, m.Metadata.ID, ErrInvalidManifest)
	}
	// The reserved check looks at the base name, not the whole id: otherwise
	// "github--t42" walks straight past a reserved "github".
	base, owner := SplitID(m.Metadata.ID)
	if !baseIDRe.MatchString(base) {
		return fmt.Errorf("%s: metadata.id base %q illegal: %w", where, base, ErrInvalidManifest)
	}
	if _, taken := reserved[base]; taken {
		return fmt.Errorf("%s: metadata.id %q collides with builtin %q: %w",
			where, m.Metadata.ID, base, ErrReservedID)
	}

	switch m.Extension.Kind {
	case KindDatasource, KindDocParser, KindWebSearch:
	default:
		return fmt.Errorf("%s extension,type %q is not in {datasource,docparser,websearch}", where, m.Extension.Kind)
	}
	if !hostCompatible(m.Compatibility.Host, hostVersion) {
		return fmt.Errorf("%s: need host %q, host is %s: %w", where, m.Compatibility.Host, hostVersion, ErrIncompatible)
	}

	// Criticality is a host judgement ("I cannot work without this"), not a
	// manifest claim. A tenant-scoped plugin must never be able to turn
	// /readyz red for every tenant, so required is downgraded here — visibly.
	switch c := strings.TrimSpace(m.Criticality); c {
	case "":
		m.Criticality = DefaultCriticality(m.Extension.Kind)
	case CriticalityRequired, CriticalityOptional:
		m.Criticality = c
	default:
		return fmt.Errorf("%s: criticality %q is not one of {%q, %q}: %w", where, m.Criticality, CriticalityRequired, CriticalityOptional, ErrInvalidManifest)
	}

	if owner != "" && m.Criticality == CriticalityRequired {
		m.Criticality = CriticalityOptional
		m.Downgraded = append(m.Downgraded,
			"criticality: required -> optional (tenant-scoped plugin)")
	}

	switch m.Runtime.Transport {
	case TransportSubprocessGRPC:
		// Refused at the manifest rather than at Open: this host never spawns
		// processes, so such a plugin would load and then fail every call.
		// Run the process yourself and publish its address as remote-grpc.
		return fmt.Errorf("%s: runtime.transport %q is not served; run the plugin and declare %q instead: %w",
			where, TransportSubprocessGRPC, TransportRemoteGRPC, ErrInvalidManifest)
	case TransportRemoteGRPC, TransportRemoteHTTP:
		if m.Runtime.Endpoint == "" && !builtin {
			return fmt.Errorf("%s: remote transport must provide runtime.endpoint", where)
		}
	default:
		return fmt.Errorf("%s: runtime.transport %q 未知", where, m.Runtime.Transport)
	}

	return m.Permissions.validate(where, m.Runtime.Transport)
}
