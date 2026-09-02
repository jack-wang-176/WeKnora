package extension

import (
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
	Transport Transport         `yaml:"transport"`
	Exec      string            `yaml:"exec"`
	Args      []string          `yaml:"args"`
	Endpoint  string            `yaml:"endpoint"`
	Env       map[string]string `yaml:"env"`
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
