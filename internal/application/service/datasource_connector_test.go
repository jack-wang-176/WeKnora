package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/types"
)

// stubConnector is a builtin connector; only Type() is ever reached here.
type stubConnector struct{ connectorType string }

func (c stubConnector) Type() string { return c.connectorType }

func (c stubConnector) Validate(context.Context, *types.DataSourceConfig) error { return nil }

func (c stubConnector) ListResources(
	context.Context, *types.DataSourceConfig, string,
) ([]types.Resource, error) {
	return nil, nil
}

func (c stubConnector) ResolveResourceAncestors(
	context.Context, *types.DataSourceConfig, []string,
) ([]string, error) {
	return nil, nil
}

func (c stubConnector) FetchAll(
	context.Context, *types.DataSourceConfig, []string,
) ([]types.FetchedItem, error) {
	return nil, nil
}

func (c stubConnector) FetchIncremental(
	context.Context, *types.DataSourceConfig, *types.SyncCursor,
) ([]types.FetchedItem, *types.SyncCursor, error) {
	return nil, nil, nil
}

func serviceWith(t *testing.T, host extension.Host, builtins ...string) *DataSourceService {
	t.Helper()
	registry := datasource.NewConnectorRegistry()
	for _, b := range builtins {
		if err := registry.Register(stubConnector{connectorType: b}); err != nil {
			t.Fatalf("Register(%q) = %v", b, err)
		}
	}
	return &DataSourceService{connectorRegistry: registry, host: host}
}

// The builtin registry answers first, so no plugin can reroute a builtin type.
// This also pins the lookup against self-recursion: the call would overflow the
// stack instead of returning.
func TestResolveConnectorPrefersTheBuiltin(t *testing.T) {
	host := newStubHost(manifestOf("feishu", extension.KindDatasource))
	got, err := serviceWith(t, host, "feishu").resolveConnector(tenantCtx(1), "feishu")
	if err != nil {
		t.Fatalf("resolveConnector() = %v", err)
	}
	if _, isStub := got.(stubConnector); !isStub {
		t.Fatalf("resolveConnector() = %T, want the builtin connector", got)
	}
	if len(host.opened) != 0 {
		t.Fatalf("the host was dialled although a builtin claimed the type: %v", host.opened)
	}
}

func TestResolveConnectorFallsBackToThePlugin(t *testing.T) {
	host := newStubHost(manifestOf("notion--1", extension.KindDatasource))
	got, err := serviceWith(t, host, "feishu").resolveConnector(tenantCtx(1), "notion--1")
	if err != nil {
		t.Fatalf("resolveConnector() = %v", err)
	}
	if got.Type() != "notion--1" {
		t.Fatalf("connector Type() = %q, want the plugin id", got.Type())
	}
}

// A type no builtin and no visible plugin claims keeps the registry's own
// not-found error, which is what the data source endpoints already report.
func TestResolveConnectorKeepsTheRegistryError(t *testing.T) {
	for _, tc := range []struct {
		name string
		host extension.Host
		typ  string
	}{
		{"no host", nil, "notion--1"},
		{"unknown type", newStubHost(), "notion--1"},
		{"another tenant", newStubHost(manifestOf("notion--2", extension.KindDatasource)), "notion--2"},
		{"wrong kind", newStubHost(manifestOf("serp--1", extension.KindWebSearch)), "serp--1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := serviceWith(t, tc.host, "feishu").resolveConnector(tenantCtx(1), tc.typ)
			if !errors.Is(err, datasource.ErrConnectorNotFound) {
				t.Fatalf("resolveConnector() = %v, want %v", err, datasource.ErrConnectorNotFound)
			}
		})
	}
}

func TestResolveConnectorReportsAnOpenFailure(t *testing.T) {
	host := newStubHost(manifestOf("notion--1", extension.KindDatasource))
	host.openErr = errors.New("dial refused")
	_, err := serviceWith(t, host, "feishu").resolveConnector(tenantCtx(1), "notion--1")
	if err == nil || !strings.Contains(err.Error(), "dial refused") {
		t.Fatalf("resolveConnector() = %v, want the open failure", err)
	}
}

func TestResolveConnectorSkipsADisabledPlugin(t *testing.T) {
	off := manifestOf("notion--1", extension.KindDatasource)
	off.Disabled = true
	_, err := serviceWith(t, newStubHost(off), "feishu").resolveConnector(tenantCtx(1), "notion--1")
	if !errors.Is(err, datasource.ErrConnectorNotFound) {
		t.Fatalf("resolveConnector() = %v, want %v", err, datasource.ErrConnectorNotFound)
	}
}
