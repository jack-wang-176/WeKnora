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

// The `endpoint` channel's half of registration: request in, row out. It stops
// at the row on purpose — the manifest is built from the row by the single
// manifestFromRow in plugin_loader.go, shared with the start-up replay, so the
// two paths cannot produce manifests that differ in a field.

// endpointTransports are the transports this channel can install. The table's
// CHECK constraint also permits subprocess-grpc: that needs an executable on
// the host's disk, which an API request cannot deliver.
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

// newEndpointPluginRow validates a registration request and builds its row. It
// checks only what the database and the id grammar must agree on before an
// INSERT can succeed. Reachability and address class are the host's checks,
// run after the row is written so a rejected plugin leaves a readable reason.
func newEndpointPluginRow(req *types.PluginRegisterRequest, now time.Time) (*types.TenantPlugin, error) {
	base := strings.TrimSpace(req.PluginID)
	if base == "" {
		return nil, fmt.Errorf("%w: plugin_id is required", ErrPluginInvalid)
	}
	if strings.Contains(base, "--") {
		// The tenant suffix is composed below, never accepted: one taken from
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
		// scoped is the middle setting: neither "may reach anything" nor a
		// class the runtime cannot honour.
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
		// installing, not ready: ListReadyPlugins keys off this, so a crash
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

// scopedPluginID composes the stored id; the suffix is the decimal tenant id,
// which the id grammar accepts ([a-z0-9]{1,36} after the separator).
func scopedPluginID(base string, tenantID *uint64) string {
	if tenantID == nil {
		return base
	}
	return extension.ScopedID(base, strconv.FormatUint(*tenantID, 10))
}

// widenStringMap turns the request's string envs into the jsonb map the column
// holds. envMap in plugin_loader.go is the inverse.
func widenStringMap(in map[string]string) types.JSONMap {
	out := make(types.JSONMap, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
