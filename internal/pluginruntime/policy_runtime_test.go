package pluginruntime

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

type networkInspectStub struct {
	dockerAPI
	internal bool
}

func (stub networkInspectStub) NetworkInspect(context.Context, string, client.NetworkInspectOptions) (client.NetworkInspectResult, error) {
	result := client.NetworkInspectResult{}
	result.Network.Internal = stub.internal
	return result, nil
}

func TestOfflinePolicyChecksNetworkInsteadOfTrustingName(t *testing.T) {
	runtime := newDockerRuntime(networkInspectStub{internal: false}, DefaultLimits)
	require.ErrorIs(t, runtime.validateNetwork(context.Background(), types.PluginPolicyOffline, "offline-in-name-only"), ErrPolicyUnsupported)
	runtime = newDockerRuntime(networkInspectStub{internal: true}, DefaultLimits)
	require.NoError(t, runtime.validateNetwork(context.Background(), types.PluginPolicyOffline, "isolated"))
}

func TestRuntimeSpecHashChangesWithPermissionsAndEnvironment(t *testing.T) {
	initial := Spec{ID: "files--1", ImageRef: "files:1", Policy: types.PluginPolicyOpen, Envs: map[string]string{"KEY": "before"}}
	changed := initial
	changed.Policy = types.PluginPolicyOffline
	require.NotEqual(t, runtimeSpecHash(initial), runtimeSpecHash(changed))
	changed = initial
	changed.Envs = map[string]string{"KEY": "after"}
	require.NotEqual(t, runtimeSpecHash(initial), runtimeSpecHash(changed))
	changed = initial
	changed.Port = 50100
	require.NotEqual(t, runtimeSpecHash(initial), runtimeSpecHash(changed))
}
