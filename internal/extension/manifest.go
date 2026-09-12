package extension

import (
	"fmt"
	"strings"
	"time"
)

type Kind string

const (
	KindDatasource Kind = "datasource"
	KindDocParser  Kind = "docparser"
	KindWebSearch  Kind = "websearch"
)

type Transport string

const (
	TransportSubprocessGRPC Transport = "subprocess-grpc"
	TransportRemoteGRPC     Transport = "remote-grpc"
	TransportRemoteHTTP     Transport = "remote-http"
)

const (
	CriticalityRequired = "required"
	CriticalityOptional = "optional"
)

const DocreaderExtesnionID = "docreader"

const (
	NetworkAny    = "any"
	NetworkNone   = "none"
	NetworkScoped = "scoped"
)

type Manifest struct {
	Metadata      Metadata      `yaml:"metadata"`
	Extension     ExtensionSpec `yaml:"extension"`
	Config        []ConfigField `yaml:"config"`
	Permissions   Permissions   `yaml:"permissions"`
	HealthCheck   HealthCheck   `yaml:"healthCheck"`
	Compatibility Compatibility `yaml:"compatibility"`
	Runtime       Runtime       `yaml:"runtime"`
	Dir           string        `yaml:"-"`
	Builtin       bool          `yaml:"-"`
	Criticality   string        `yaml:"criticality"`
	FallbackFor   []string      `yaml:"fallbackFor"`
	Scaling       string        `yaml:"scaling"`
	IdleTimeout   string        `yaml:"idleTimeout"`
	Hash          string        `yaml:"-"`
	// Disabled marks an extension the operator turned off on purpose. It is
	// not a health signal: Health short-circuits before dialling and Ready
	// buckets it separately, so a deliberately disabled plugin never shows up
	// as a permanent yellow light (see Readiness.Disabled).
	Disabled bool `yaml:"-"`
	// Downgraded records every decision this host made against what the
	// manifest asked for. The host may override a declaration, but it must say
	// which one it overrode: a silent rewrite leaves the author and the UI
	// looking at different facts.
	Downgraded []string `yaml:"-"`
}

type Metadata struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Author      string `yaml:"author"`
	Icon        string `yaml:"icon"`
	Version     string `yaml:"version"`
	Homepage    string `yaml:"homepage"`
}

type ExtensionSpec struct {
	Contract     string   `yaml:"contract"`
	Kind         Kind     `yaml:"type"`
	Capabilities []string `yaml:"capabilities"`
	AuthType     string   `yaml:"authType"`
	Priority     int      `yaml:"priority"`
}

type ConfigField struct {
	Key         string            `yaml:"key"`
	Type        string            `yaml:"type"`
	Label       map[string]string `yaml:"label"`
	Required    bool              `yaml:"required"`
	Default     any               `yaml:"default"`
	Placeholder string            `yaml:"placeholder"`
	Help        string            `yaml:"help"`
}

type Runtime struct {
	Transport      Transport         `yaml:"transport"`
	Exec           string            `yaml:"exec"`
	Args           []string          `yaml:"args"`
	Endpoint       string            `yaml:"endpoint"`
	Env            map[string]string `yaml:"env"`
	ManagedOffline bool              `yaml:"-"`
}

type Compatibility struct {
	Host      string   `yaml:"host"`
	Contracts []string `yaml:"contracts"`
}

type HealthCheck struct {
	Type     string        `yaml:"type"`
	Service  string        `yaml:"service"`
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

type Permissions struct {
	Network struct {
		Outbound string   `yaml:"outbound"`
		Allow    []string `yaml:"allow"`
	} `yaml:"network"`
	Filesystem struct {
		Read  []string `yaml:"read"`
		Write []string `yaml:"write"`
	} `yaml:"filesystem"`
	Secrets []string `yaml:"secrets"`
}

func (m *Manifest) IsRequired() bool {
	return m.Criticality != CriticalityOptional
}

func (p *Permissions) validate(where string, t Transport, managedOffline bool) error {
	switch outbound := strings.TrimSpace(p.Network.Outbound); outbound {
	case "", NetworkAny:
		if len(p.Network.Allow) > 0 {
			return fmt.Errorf("%s: network.allow requires outbound=%q: %w", where, NetworkScoped, ErrInvalidManifest)
		}
	case NetworkNone:
		if len(p.Network.Allow) > 0 {
			return fmt.Errorf("%s: outbound=none contradicts %d allow entries: %w", where, len(p.Network.Allow), ErrInvalidManifest)
		}
	case NetworkScoped:
		if len(p.Network.Allow) == 0 {
			return fmt.Errorf("%s: outbound=scoped needs a non-empty network.allow: %w", where, ErrInvalidManifest)
		}
		return fmt.Errorf("%s: outbound=scoped is not enforceable yet: %w", where, ErrUnenforceable)
	default:
		return fmt.Errorf("%s: network.outbound %q unknown: %w", where, outbound, ErrInvalidManifest)
	}

	if t == TransportRemoteGRPC || t == TransportRemoteHTTP {
		if len(p.Filesystem.Read) > 0 || len(p.Filesystem.Write) > 0 {
			return fmt.Errorf("%s: filesystem permissions cannot apply to %s: %w", where, t, ErrUnenforceable)
		}
		if p.Network.Outbound == NetworkNone && !managedOffline {
			return fmt.Errorf("%s: outbound=none cannot apply to %s: %w", where, t, ErrUnenforceable)
		}
	}
	return nil
}
