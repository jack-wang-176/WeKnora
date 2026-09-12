# Runtime Plugin Framework

This page describes the implementation in this branch, not a completed plugin marketplace.

## Architecture and Trade-offs

The extension host owns manifest validation, tenant visibility, channel creation, health and lifecycle. Business code resolves an extension and uses a typed adapter; it does not know how its process was deployed.

```text
Settings UI / REST API
        |
tenant-scoped authorization / system-admin authorization
        |
endpoint registration          managed image installation
        |                              |
        +------ tenant_plugins --------+---- Docker Runtime
                       |
              ManifestLoader / Host
                       |
       datasource | docparser | websearch adapters
                       |
        gRPC service / HTTP search service
```

The implementation uses process boundaries: gRPC for datasources and document parsing, and gRPC or HTTP for search. An external endpoint is operated outside WeKnora; a bundle is a prebuilt image started by WeKnora. These are distinct deployment channels, not distinct business extension contracts.

| Option | Benefits | Costs / decision |
| --- | --- | --- |
| Compile-time metadata only | Simple packaging, native calls | Cannot load a third-party implementation without rebuilding the host; insufficient for the independent datasource requirement |
| In-process Go plugins | Direct Go interfaces | Toolchain/ABI coupling, weak fault isolation, platform limitations; not selected |
| gRPC process boundary | Language independence, explicit contracts, independent deployment | Serialization, timeouts, health, lifecycle and infrastructure must be managed; selected |
| WASM | Potentially narrower capabilities | Requires a separate ABI, SDK and execution model; not implemented |

Native datasource/search registries still exist. Resolvers prefer builtins and then consult the tenant-visible extension host. The bundled docreader participates in the host. This is an incremental migration, **not** a claim that every builtin now uses one loader. Model-provider and retrieval-backend factories have not been migrated to runtime plugins.

## Manifest

```yaml
metadata:
  id: local-files
  name: Local Markdown Files
  version: 0.1.0
extension:
  type: datasource
  contract: datasource.v1
  authType: none
  capabilities: [incremental, deletions]
compatibility:
  host: ">=0.8.0 <0.9.0"
  contracts: [datasource.v1]
config: []
permissions:
  network:
    outbound: none
healthCheck:
  type: grpc
  timeout: 5s
runtime:
  transport: remote-grpc
```

`metadata.id` is the author's base ID. Tenant registrations become `<base>--<tenant ID>`; process registrations have no suffix. Route scope, not a request-body tenant ID, decides ownership. Lifecycle URLs use `plugin_id`, never the database row UUID.

The manifest declares metadata, extension kind/contract, configuration fields, permissions, compatibility and health settings. These declarations persist in the database and are restored on startup. Host-owned values (scoped ID, endpoint, enabled state and environment) override deployment details in the submitted manifest. Unknown fields are rejected. `config` is a stored descriptor; there is not yet a general schema-generated credential form or full configuration validator for every extension.

Version constraints are checked on registration/replay. In a managed install, the version check currently happens when registering the started service, so an incompatible image can be pulled before rejection. Do not treat this as a marketplace preflight validator. The runtime needs a valid application version, provided by build metadata or the `VERSION` file.

## Deployment Policies

| Channel / policy | Actual behavior |
| --- | --- |
| endpoint / open | Register an operator-owned service; WeKnora does not control its outbound traffic or environment |
| endpoint / offline or scoped | Rejected; a label must not imply isolation of another operator's process |
| bundle / offline | Default; verify the configured Docker network is actually `internal`, then start the container on that network |
| bundle / open | Start on the configured open network; cannot accompany `outbound: none` |
| bundle / scoped | Rejected; a domain-aware egress enforcement layer is not implemented |

The managed runtime drops Linux capabilities, enables `no-new-privileges` and sets CPU/memory/PID limits. It verifies the actual network property and compares the image, runtime specification and network before reusing a running container. These controls are not a complete sandbox for arbitrary untrusted code. Docker's internal network still permits internal peers. There is no general runtime-generated `network.denied` audit event yet; an external test's failed curl log must not be presented as one.

Filesystem access declarations for remote transports are rejected. Arbitrary host bind mounts are not exposed through the installer. Secret values are write-only through the API, but write-only output is not the same as encryption at rest or a secret manager.

The backend must be attached to both plugin networks and be able to resolve container names. Native macOS execution cannot reach an ordinary Docker container name directly. A bundle deployment also requires access to a Docker daemon; this is a highly privileged infrastructure boundary. Do not expose the Docker socket to plugin containers. Separate WeKnora installations should not share one plugin-runtime daemon without deployment-level ownership isolation.

## API Workflow

All routes below require authentication and workspace administration permission. Process-wide `/api/v1/admin/plugins` routes additionally require a real system administrator even when workspace RBAC is disabled. They are not opted into API-key access.

```text
POST   /api/v1/plugins                 register external endpoint
POST   /api/v1/plugins/install         install prebuilt image, HTTP 202
GET    /api/v1/plugins                 list persisted installation states
GET    /api/v1/plugins/:plugin_id      inspect installation and live health
POST   /api/v1/plugins/:plugin_id/enable
POST   /api/v1/plugins/:plugin_id/disable
POST   /api/v1/plugins/:plugin_id/reconnect
PUT    /api/v1/plugins/:plugin_id/envs
DELETE /api/v1/plugins/:plugin_id
GET    /api/v1/plugins/:plugin_id/events
```

Registration/installation and enable/disable responses use `{success: true, data: ...}`. Managed enable returns 202, not completion; follow the SSE stream or poll the detail endpoint. `status: ready` means registered, while `state: serving` is a health observation. A list request does not dial every plugin; the UI retrieves live health separately with bounded concurrency.

Example external registration:

```json
{
  "plugin_id": "local-files",
  "kind": "datasource",
  "transport": "remote-grpc",
  "endpoint": "operator-managed-host:50051",
  "policy_class": "open"
}
```

An optional `manifest` string can preserve richer metadata. For external endpoints it must not claim `outbound: none`. When a manifest is supplied, permissions must be declared there rather than in two conflicting places.

Example managed installation body:

```json
{
  "plugin_id": "local-files",
  "source_type": "image",
  "source_url": "registry.example.org/local-files@sha256:<verified digest>",
  "policy_class": "offline",
  "manifest": "<the YAML manifest as a string>"
}
```

VCS/archive builds are deliberately not enabled. Building a caller-supplied Dockerfile requires an isolated build system. Image tags are treated as immutable; use a digest for reproducible delivery. Environment edits are persisted for the next container start: disable/enable a managed plugin to apply them. Reconnecting an external endpoint does not remotely change that service's process environment. There is no atomic hot configuration rollout.

## Implementing a Datasource

The wire contract is in `docreader/proto/plugin/datasource/datasource.proto`. A plugin must implement these RPCs plus the standard gRPC Health service:

| RPC | Responsibility |
| --- | --- |
| Describe | Report the plugin version and supported contracts/capabilities |
| Validate | Check datasource credentials/settings without ingesting data |
| ListResources | Return stable external IDs and resource metadata |
| ResolveResourceAncestors | Return the ancestors of selected resources; a flat source can return an empty list |
| FetchStream | Emit changed content, deletion markers, checkpoints and a final cursor |

To start an independent Go module, copy the protocol into its own `proto/datasource` directory, set `go_package` to that module's import path and generate its bindings. Do not import `internal` packages or replace the main repository's connector factory.

```sh
go mod init example.org/my-datasource-plugin
go get google.golang.org/grpc@v1.81.0 google.golang.org/protobuf@v1.36.11
protoc --go_out=. --go_opt=paths=source_relative \
  --go-grpc_out=. --go-grpc_opt=paths=source_relative \
  proto/datasource/datasource.proto
```

Use a bounded source snapshot or a resumable page iterator. Hash the exact emitted bytes. Keep external IDs stable, emit only changed items and send the new cursor only after the corresponding items. A deletion is an explicit `is_deleted` item, not an empty successful fetch. Honor cancellation and cap item sizes. Errors must not produce a success cursor; avoid logging credentials or document contents.

The standalone `weknora-plugin-local-files` example has its own module, generated bindings, Dockerfile, manifest, read-only source implementation and real gRPC incremental tests. It supports Markdown/text files, hash cursors, deletions, cancellation and bounded reads. Its README documents external-directory operation and managed-image limitations. The example's tests do not substitute for an independent contributor following the documentation without help.

## End-to-End Verification

Use an isolated workspace and synthetic source files. Configure a real storage backend and embedding model. A local Ollama embedding model is sufficient; a mocked successful datasource response is not an ingestion test.

1. Register the plugin without modifying WeKnora code. Confirm it appears in `/datasource/types` and the UI picker.
2. Validate credentials and create a datasource with two selected files.
3. Run initial synchronization, then wait for document parsing to reach `completed`. Verify chunks and embeddings exist, not merely `SyncLog.status == success`.
4. Change one file. Verify the sync processes one item and the unchanged document retains its ID, hash, chunk IDs and processing timestamp.
5. Run again unchanged and expect zero items. Exercise deletion with `sync_deletions` enabled.
6. Restart the host and compare metadata, configuration descriptors and health settings.
7. Verify ordinary tenant Owners cannot manage process-level plugins or another tenant's plugin. Test actual API responses as well as hidden UI controls.
8. For offline bundles, inspect the actual network and test a direct-IP control from open and managed network namespaces. Record the scope of the result; IPv4 TCP failure alone does not verify DNS, UDP, IPv6, internal-peer isolation or a host-generated denial audit.

## Verification Commands

```sh
go test ./internal/extension/... ./internal/pluginruntime/... ./internal/datasource/plugin/...
go test ./internal/application/service ./internal/router ./internal/handler \
  -run 'Test(Plugin|ProcessSync|ResolveConnector|Datasource|DataSource|Docreader|MergePlugin)' -count=1
go test -race ./internal/extension ./internal/pluginruntime ./internal/datasource/plugin
```

From `frontend/` run `npm run type-check`, `npm test` and `npm run build`.

Migration `000093_plugin_manifest` stores the original manifest. Existing rows without a manifest use the legacy metadata fallback. Test database migration and rollback on a disposable database before deployment; do not roll back by deleting production plugin rows.

Known completion gaps: unified model/retrieval runtime contracts, complete migration of native builtins, strong per-plugin egress enforcement plus denial audit, secret storage/rotation, package signing and provenance, rollback, resource quotas per tenant, multi-replica lifecycle fault testing, and an independent documentation-only implementation trial. Marketplace design must treat these as release gates, not as already implemented features.
