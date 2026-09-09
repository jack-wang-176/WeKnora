package service

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/types"
)

// This file is the `endpoint` channel's half of registration: it turns a
// request into the row that the rest of the system reads back.
//
// It stops at the row on purpose. The manifest is built from the row by the
// single manifestFromRow in plugin_loader.go, shared with the start-up replay,
// so the two paths cannot produce manifests that differ in a field. When the
// `bundle` channel arrives it will add its own builder beside this one — and it
// will also stop at a row, for the same reason: under `bundle` the address is
// not the user's to give, it is whatever the container came up on, so a builder
// that went straight to a manifest would have nothing to build from.

// endpointTransports are the transports this channel can install. The table's
// CHECK constraint also permits subprocess-grpc, and that is not an oversight
// in either place: a subprocess plugin needs an executable on the host's disk,
// which is exactly the thing an API request cannot deliver. The row shape can
// hold one (a future channel may put one there); this request cannot create one.
var endpointTransports = map[string]struct{}{
	string(extension.TransportRemoteGRPC): {},
	string(extension.TransportRemoteHTTP): {},
}

var pluginKinds = map[string]struct{}{
	string(extension.KindDocParser):  {},
	string(extension.KindWebSearch):  {},
	string(extension.KindDatasource): {},
}

var pluginPolicyClasses = map[string]struct{}{
	types.PluginPolicyOffline: {},
	types.PluginPolicyScoped:  {},
	types.PluginPolicyOpen:    {},
}

// newEndpointPluginRow validates a registration request and builds the row for
// it. What it checks here is only what the database and the id grammar have to
// agree on before an INSERT can succeed — cheap, local, and worth refusing
// before a row exists.
//
// It deliberately does NOT check the endpoint's reachability or its address
// class. Those are the host's checks (NormalizeEndpoint, then Register), and
// they run after the row is written so that a rejected plugin leaves a row
// carrying the reason. A caller who typed an unreachable address wants to read
// why; a caller who typed a malformed kind has nothing to read yet.
func newEndpointPluginRow(req *types.PluginRegisterRequest, now time.Time) (*types.TenantPlugin, error) {
	base := strings.TrimSpace(req.PluginID)
	if base == "" {
		return nil, fmt.Errorf("%w: plugin_id is required", ErrPluginInvalid)
	}
	if strings.Contains(base, "--") {
		// The tenant suffix is composed below, never accepted. Taking one from
		// the request would let a tenant write into another tenant's namespace.
		return nil, fmt.Errorf("%w: plugin_id %q must not carry a tenant suffix", ErrPluginInvalid, base)
	}
	kind := strings.TrimSpace(req.Kind)
	if _, ok := pluginKinds[kind]; !ok {
		return nil, fmt.Errorf("%w: kind %q is not one of docparser/websearch/datasource", ErrPluginInvalid, req.Kind)
	}
	transport := strings.TrimSpace(req.Transport)
	if _, ok := endpointTransports[transport]; !ok {
		return nil, fmt.Errorf("%w: transport %q is not one of remote-grpc/remote-http", ErrPluginInvalid, req.Transport)
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("%w: endpoint is required for the endpoint channel", ErrPluginInvalid)
	}
	policy := strings.TrimSpace(req.PolicyClass)
	if policy == "" {
		// scoped is the default because it is the middle setting: an operator
		// who has not thought about the network yet gets neither "this plugin
		// may reach anything" nor a class the runtime cannot honour.
		policy = types.PluginPolicyScoped
	}
	if _, ok := pluginPolicyClasses[policy]; !ok {
		return nil, fmt.Errorf("%w: policy_class %q is not one of offline/scoped/open", ErrPluginInvalid, req.PolicyClass)
	}

	row := &types.TenantPlugin{
		ID:          uuid.New().String(),
		TenantID:    req.TenantID,
		PluginID:    scopedPluginID(base, req.TenantID),
		Kind:        kind,
		Channel:     types.PluginChannelEndpoint,
		Transport:   transport,
		Endpoint:    &endpoint,
		PolicyClass: policy,
		// installing, not ready: the row exists before the host has agreed to
		// the plugin, and ListReadyPlugins keys off exactly this so a crash
		// between the INSERT and the Register cannot leave a half-installed
		// plugin loading itself on the next boot.
		Status:          types.PluginStatusInstalling,
		InstallingSince: &now,
		Enabled:         true,
		Envs:            widenStringMap(req.Envs),
		Permissions:     types.JSONMap(req.Permissions),
	}
	if row.Permissions == nil {
		row.Permissions = types.JSONMap{}
	}
	return row, nil
}

// scopedPluginID composes the stored id. The tenant suffix is the decimal
// tenant id, which the id grammar accepts ([a-z0-9]{1,36} after the separator).
func scopedPluginID(base string, tenantID *uint64) string {
	if tenantID == nil {
		return base
	}
	return extension.ScopedID(base, strconv.FormatUint(*tenantID, 10))
}

// widenStringMap turns the request's string envs into the jsonb map the column
// holds. envMap in plugin_loader.go is the inverse and refuses non-strings on
// the way back, so this is the only place a value's type is decided.
func widenStringMap(in map[string]string) types.JSONMap {
	out := make(types.JSONMap, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
