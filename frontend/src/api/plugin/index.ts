import { del, get, post, put } from '@/utils/request'

// Scope decides the route prefix, not a request field: the two prefixes carry
// different RBAC, and a body-level scope would let one key reach the other.
export type PluginScope = 'tenant' | 'process'

export const PLUGIN_KINDS = ['websearch', 'docparser', 'datasource', 'docreader'] as const
export type PluginKind = (typeof PLUGIN_KINDS)[number] | (string & {})

// subprocess-grpc is refused by the manifest validator, so it is not offered.
export const PLUGIN_TRANSPORTS = ['remote-grpc', 'remote-http'] as const
export type PluginTransport = (typeof PLUGIN_TRANSPORTS)[number]

export const PLUGIN_POLICIES = ['offline', 'scoped', 'open'] as const
export type PluginPolicy = (typeof PLUGIN_POLICIES)[number]

// Envs are reported as key names only — the values are credentials.
export interface PluginEntity {
  id: string
  tenant_id?: number
  plugin_id: string
  kind: PluginKind
  channel: 'builtin' | 'bundle' | 'endpoint'
  transport: string
  endpoint?: string
  policy_class: PluginPolicy
  status: 'installing' | 'ready' | 'failed' | 'removing'
  enabled: boolean
  error?: string
  env_keys: string[]
  permissions?: Record<string, any>
  state?: string
  created_at: string
  updated_at: string
}

// plugin_id is the BASE id; the server composes the scoped id from it.
export interface PluginRegisterRequest {
  plugin_id: string
  kind: PluginKind
  transport: PluginTransport
  endpoint: string
  policy_class?: PluginPolicy
  envs?: Record<string, string>
  permissions?: Record<string, any>
}

export interface PluginInstallRequest {
  plugin_id: string
  source_type: 'image' | 'vcs' | 'archive'
  source_url: string
  source_ref?: string
  policy_class?: PluginPolicy
  manifest: string
  envs?: Record<string, string>
}

export interface PluginInstallEvent {
  percent: number
  stage: string
  log?: string
  status?: string
  done: boolean
}

interface Envelope<T> {
  success: boolean
  data: T
}

function base(scope: PluginScope): string {
  return scope === 'process' ? '/api/v1/admin/plugins' : '/api/v1/plugins'
}

export function listPlugins(scope: PluginScope) {
  return get<Envelope<PluginEntity[]>>(base(scope))
}

export function getPlugin(scope: PluginScope, id: string) {
  return get<Envelope<PluginEntity>>(`${base(scope)}/${encodeURIComponent(id)}`)
}

export function registerPlugin(scope: PluginScope, data: PluginRegisterRequest) {
  return post<Envelope<PluginEntity>>(base(scope), data)
}

// An install pulls or builds an image, so it holds the request open far longer
// than a register; progress arrives on the event stream, not here.
export function installPlugin(scope: PluginScope, data: PluginInstallRequest) {
  return post<Envelope<PluginEntity>>(`${base(scope)}/install`, data, {
    timeout: 5 * 60 * 1000,
  })
}

export function uninstallPlugin(scope: PluginScope, id: string) {
  return del(`${base(scope)}/${encodeURIComponent(id)}`)
}

export function setPluginEnabled(scope: PluginScope, id: string, enabled: boolean) {
  const action = enabled ? 'enable' : 'disable'
  return post<Envelope<PluginEntity>>(`${base(scope)}/${encodeURIComponent(id)}/${action}`, {})
}

export function reconnectPlugin(scope: PluginScope, id: string, endpoint: string) {
  return post<Envelope<PluginEntity>>(`${base(scope)}/${encodeURIComponent(id)}/reconnect`, { endpoint })
}

export function setPluginEnvs(scope: PluginScope, id: string, envs: Record<string, string>) {
  return put<Envelope<PluginEntity>>(`${base(scope)}/${encodeURIComponent(id)}/envs`, { envs })
}

// Path only: the stream is opened by fetchEventSource, which needs the auth
// headers the request helpers add.
export function pluginInstallEventsUrl(scope: PluginScope, id: string): string {
  return `${base(scope)}/${encodeURIComponent(id)}/events`
}
