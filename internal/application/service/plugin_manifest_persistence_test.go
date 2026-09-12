package service

import (
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/extension"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const persistedPluginManifest = `metadata:
  id: local-files
  name: Local Files
  version: 1.2.3
  description: Controlled directory source
extension:
  type: datasource
  authType: none
  capabilities: [incremental]
config:
  - key: root
    type: string
    required: true
compatibility:
  host: ">=0.8.0 <0.9.0"
healthCheck:
  service: local-files
permissions:
  network:
    outbound: none
runtime:
  transport: remote-grpc
`

func TestPluginManifestRoundTripPreservesDeclaration(t *testing.T) {
	manifest, err := parsePluginManifest(persistedPluginManifest)
	require.NoError(t, err)
	tenantID := uint64(7)
	request := &types.PluginInstallRequest{TenantID: &tenantID, PluginID: "local-files", Manifest: persistedPluginManifest}
	row, err := newBundlePluginRow(request, "local-files", pluginSource{URL: "local-files:1.2.3"}, manifest, time.Now())
	require.NoError(t, err)
	require.Equal(t, types.PluginPolicyOffline, row.PolicyClass)
	endpoint := "local-files:50051"
	row.Endpoint = &endpoint
	restored, err := manifestFromRow(row)
	require.NoError(t, err)
	require.Equal(t, "local-files--7", restored.Metadata.ID)
	require.Equal(t, manifest.Metadata.Version, restored.Metadata.Version)
	require.Equal(t, manifest.Metadata.Name, restored.Metadata.Name)
	require.Equal(t, manifest.Compatibility, restored.Compatibility)
	require.Equal(t, manifest.Config, restored.Config)
	require.Equal(t, manifest.Extension.Capabilities, restored.Extension.Capabilities)
	require.Equal(t, manifest.HealthCheck, restored.HealthCheck)
	require.True(t, restored.Runtime.ManagedOffline)
	require.NoError(t, restored.Validate("0.8.0", nil, false))
	require.ErrorIs(t, restored.Validate("0.9.0", nil, false), extension.ErrIncompatible)
}

func TestPluginBundleRejectsUnenforceablePoliciesBeforeInstallation(t *testing.T) {
	for _, policy := range []string{types.PluginPolicyOpen, types.PluginPolicyScoped} {
		t.Run(policy, func(t *testing.T) {
			manifest, err := parsePluginManifest(persistedPluginManifest)
			require.NoError(t, err)
			_, err = newBundlePluginRow(&types.PluginInstallRequest{PolicyClass: policy}, "local-files", pluginSource{}, manifest, time.Now())
			require.ErrorIs(t, err, ErrPluginInvalid)
		})
	}
}

func TestPluginEndpointCannotClaimManagedIsolation(t *testing.T) {
	for _, policy := range []string{types.PluginPolicyOffline, types.PluginPolicyScoped} {
		_, err := newEndpointPluginRow(&types.PluginRegisterRequest{PluginID: "remote", Kind: "datasource", Transport: "remote-grpc", Endpoint: "remote:50051", PolicyClass: policy}, time.Now())
		require.ErrorIs(t, err, ErrPluginInvalid)
	}
	_, err := parsePluginManifest(persistedPluginManifest + "  ManagedOffline: true\n")
	require.Error(t, err)
}

func TestPluginPermissionDecoderRejectsUnknownFields(t *testing.T) {
	_, err := declaredPermissions(types.JSONMap{"network": map[string]any{"outbound": "none", "allow_all": true}})
	require.Error(t, err)
}
