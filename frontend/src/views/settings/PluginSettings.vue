<template>
  <div class="plugin-settings">
    <div class="section-header">
      <div class="section-header__title-row">
        <h2>{{ t('settings.plugins.title') }}</h2>
      </div>
      <p class="section-description">{{ t('settings.plugins.description') }}</p>
    </div>

    <div class="plugin-settings__bar">
      <t-radio-group v-if="canUseProcessScope" v-model="scope" variant="default-filled" size="small">
        <t-radio-button value="tenant">{{ t('settings.plugins.scopeTenant') }}</t-radio-button>
        <t-radio-button value="process">{{ t('settings.plugins.scopeProcess') }}</t-radio-button>
      </t-radio-group>
      <span v-else />
      <div class="plugin-settings__bar-actions">
        <t-button theme="default" variant="outline" size="small" :loading="loading" @click="load">
          {{ t('settings.plugins.refresh') }}
        </t-button>
        <t-button theme="default" variant="outline" size="small" @click="openInstall">
          {{ t('settings.plugins.install') }}
        </t-button>
        <t-button theme="primary" size="small" @click="openRegister">
          {{ t('settings.plugins.register') }}
        </t-button>
      </div>
    </div>

    <div v-if="loading" class="loading-container">
      <t-loading :text="t('common.loading')" />
    </div>

    <template v-else>
      <div v-if="plugins.length === 0" class="empty-state">
        <t-empty :description="t('settings.plugins.emptyDesc')" />
      </div>

      <div v-else class="plugin-list">
        <article v-for="item in plugins" :key="item.id" class="plugin-card"
          :class="{ 'plugin-card--ready': item.status === 'ready' && item.enabled }">
          <div class="plugin-card__main">
            <div class="plugin-card__badge" aria-hidden="true">
              <t-icon :name="kindIcon(item.kind)" size="16px" />
            </div>
            <div class="plugin-card__body">
              <div class="plugin-card__header">
                <div class="plugin-card__heading">
                  <h3 class="plugin-card__title" :title="item.plugin_id">{{ item.plugin_id }}</h3>
                  <span class="plugin-card__type">{{ kindLabel(item.kind) }}</span>
                </div>
                <div class="plugin-card__actions">
                  <t-switch :value="item.enabled" size="small" :disabled="busyId === item.id"
                    :label="[t('settings.plugins.enabled'), t('settings.plugins.disabled')]"
                    @change="(v: boolean) => toggle(item, v)" />
                  <button v-if="item.channel === 'endpoint'" type="button" class="plugin-card__icon-btn"
                    :title="t('settings.plugins.reconnect')" @click="openReconnect(item)">
                    <t-icon name="link" size="14px" />
                  </button>
                  <button type="button" class="plugin-card__icon-btn" :title="t('settings.plugins.envsTitle')"
                    @click="openEnvs(item)">
                    <t-icon name="key" size="14px" />
                  </button>
                  <button type="button" class="plugin-card__icon-btn plugin-card__icon-btn--danger"
                    :disabled="busyId === item.id" :title="t('settings.plugins.uninstall')" @click="askUninstall(item)">
                    <t-icon name="delete" size="14px" />
                  </button>
                </div>
              </div>
              <p class="plugin-card__desc" :title="item.endpoint || item.transport">
                {{ item.transport }}<template v-if="item.endpoint"> · {{ item.endpoint }}</template>
              </p>
              <p v-if="item.error" class="plugin-card__error" :title="item.error">{{ item.error }}</p>
              <div class="plugin-card__footer">
                <span class="plugin-card__chip" :class="`plugin-card__chip--${item.status}`">
                  {{ statusLabel(item) }}
                </span>
                <span class="plugin-card__meta">{{ item.channel }} · {{ item.policy_class }}</span>
                <span v-if="item.env_keys?.length" class="plugin-card__meta">
                  {{ t('settings.plugins.envKeys', { n: item.env_keys.length }) }}
                </span>
              </div>
            </div>
          </div>
        </article>
      </div>
    </template>

    <t-dialog v-model:visible="registerVisible" :header="t('settings.plugins.registerTitle')" :width="520"
      :confirm-btn="{ content: t('settings.plugins.submit'), loading: submitting }"
      :cancel-btn="t('common.cancel')" @confirm="submitRegister">
      <t-form label-align="top">
        <t-form-item :label="t('settings.plugins.pluginId')">
          <t-input v-model="registerForm.plugin_id" :placeholder="t('settings.plugins.pluginIdTip')" />
        </t-form-item>
        <t-form-item :label="t('settings.plugins.kind')">
          <t-select v-model="registerForm.kind">
            <t-option v-for="k in PLUGIN_KINDS" :key="k" :value="k" :label="kindLabel(k)" />
          </t-select>
        </t-form-item>
        <t-form-item :label="t('settings.plugins.transport')">
          <t-select v-model="registerForm.transport">
            <t-option v-for="tr in PLUGIN_TRANSPORTS" :key="tr" :value="tr" :label="tr" />
          </t-select>
        </t-form-item>
        <t-form-item :label="t('settings.plugins.endpoint')">
          <t-input v-model="registerForm.endpoint" placeholder="host:port" />
        </t-form-item>
        <t-form-item :label="t('settings.plugins.policy')">
          <t-select v-model="registerForm.policy_class">
            <t-option v-for="p in PLUGIN_POLICIES" :key="p" :value="p" :label="p" />
          </t-select>
        </t-form-item>
      </t-form>
    </t-dialog>

    <t-dialog v-model:visible="installVisible" :header="t('settings.plugins.installTitle')" :width="560"
      :confirm-btn="{ content: t('settings.plugins.submit'), loading: submitting }"
      :cancel-btn="t('common.cancel')" @confirm="submitInstall">
      <t-form label-align="top">
        <t-form-item :label="t('settings.plugins.pluginId')">
          <t-input v-model="installForm.plugin_id" :placeholder="t('settings.plugins.pluginIdTip')" />
        </t-form-item>
        <t-form-item :label="t('settings.plugins.sourceType')">
          <t-select v-model="installForm.source_type">
            <t-option v-for="s in SOURCE_TYPES" :key="s" :value="s" :label="s" />
          </t-select>
        </t-form-item>
        <t-form-item :label="t('settings.plugins.sourceUrl')">
          <t-input v-model="installForm.source_url" />
        </t-form-item>
        <t-form-item :label="t('settings.plugins.sourceRef')">
          <t-input v-model="installForm.source_ref" />
        </t-form-item>
        <t-form-item :label="t('settings.plugins.manifest')">
          <t-textarea v-model="installForm.manifest" :autosize="{ minRows: 6, maxRows: 14 }"
            :placeholder="t('settings.plugins.manifestTip')" />
        </t-form-item>
      </t-form>
    </t-dialog>

    <t-dialog v-model:visible="reconnectVisible" :header="t('settings.plugins.reconnect')" :width="460"
      :confirm-btn="{ content: t('settings.plugins.submit'), loading: submitting }"
      :cancel-btn="t('common.cancel')" @confirm="submitReconnect">
      <t-input v-model="reconnectEndpoint" placeholder="host:port" />
    </t-dialog>

    <t-dialog v-model:visible="envsVisible" :header="t('settings.plugins.envsTitle')" :width="520"
      :confirm-btn="{ content: t('settings.plugins.submit'), loading: submitting }"
      :cancel-btn="t('common.cancel')" @confirm="submitEnvs">
      <p class="plugin-settings__hint">{{ t('settings.plugins.envsTip') }}</p>
      <div v-for="(row, i) in envRows" :key="i" class="plugin-settings__env-row">
        <t-input v-model="row.key" :placeholder="t('settings.plugins.envKey')" />
        <t-input v-model="row.value" type="password" :placeholder="t('settings.plugins.envValue')" />
        <t-button theme="default" variant="text" shape="square" @click="envRows.splice(i, 1)">
          <t-icon name="close" size="14px" />
        </t-button>
      </div>
      <t-button theme="default" variant="outline" size="small" @click="envRows.push({ key: '', value: '' })">
        {{ t('settings.plugins.addEnv') }}
      </t-button>
    </t-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { fetchEventSource } from '@microsoft/fetch-event-source'
import {
  PLUGIN_KINDS,
  PLUGIN_POLICIES,
  PLUGIN_TRANSPORTS,
  installPlugin,
  listPlugins,
  pluginInstallEventsUrl,
  reconnectPlugin,
  registerPlugin,
  setPluginEnabled,
  setPluginEnvs,
  uninstallPlugin,
  type PluginEntity,
  type PluginInstallEvent,
  type PluginKind,
  type PluginScope,
} from '@/api/plugin'
import { useAuthStore } from '@/stores/auth'
import i18n from '@/i18n'
import { generateRandomString } from '@/utils'
import { getApiBaseUrl } from '@/utils/api-base'

const { t } = useI18n()
const authStore = useAuthStore()

const SOURCE_TYPES = ['image', 'vcs', 'archive'] as const

const scope = ref<PluginScope>('tenant')
const plugins = ref<PluginEntity[]>([])
const loading = ref(false)
const busyId = ref('')
const submitting = ref(false)

// The process scope is the whole deployment's plugin set; only a system admin
// may see it, and the backend enforces the same bar on /admin/plugins.
const canUseProcessScope = computed(() => authStore.canAccessAllTenants)

const KIND_ICONS: Record<string, string> = {
  websearch: 'search',
  docparser: 'file-search',
  datasource: 'data-base',
  docreader: 'file',
}

function kindIcon(kind: string): string {
  return KIND_ICONS[kind] || 'extension'
}

// A plugin kind the build has no label for is still legal — show its raw id.
function kindLabel(kind: PluginKind): string {
  const key = `settings.plugins.kind_${kind}`
  const translated = t(key)
  return translated !== key ? translated : String(kind)
}

function statusLabel(item: PluginEntity): string {
  const event = progressById.value[item.id]
  if (item.status === 'installing' && event && !event.done) {
    return `${t('settings.plugins.status_installing')} ${Math.round(event.percent)}%`
  }
  const key = `settings.plugins.status_${item.status}`
  const translated = t(key)
  return translated !== key ? translated : item.status
}

async function load() {
  loading.value = true
  try {
    const res = await listPlugins(scope.value)
    plugins.value = Array.isArray(res?.data) ? res.data : []
    syncStreams()
  } catch (e: any) {
    plugins.value = []
    MessagePlugin.error(e?.message || t('settings.plugins.loadFailed'))
  } finally {
    loading.value = false
  }
}

watch(scope, () => {
  stopAllStreams()
  load()
})
load()

/* ---------- install progress ---------- */

const progressById = ref<Record<string, PluginInstallEvent>>({})
const abortById = new Map<string, AbortController>()

function stopStream(id: string) {
  abortById.get(id)?.abort()
  abortById.delete(id)
}

function stopAllStreams() {
  for (const id of [...abortById.keys()]) stopStream(id)
  progressById.value = {}
}

onBeforeUnmount(stopAllStreams)

// One stream per installing plugin. The server replays the buffered log on
// connect, so a page opened mid-install still shows the whole run.
function follow(id: string) {
  if (!id || abortById.has(id)) return
  const controller = new AbortController()
  abortById.set(id, controller)

  const token = localStorage.getItem('weknora_token')
  const tenantId = localStorage.getItem('weknora_selected_tenant_id')

  void fetchEventSource(`${getApiBaseUrl()}${pluginInstallEventsUrl(scope.value, id)}`, {
    method: 'GET',
    headers: {
      Authorization: token ? `Bearer ${token}` : '',
      'Accept-Language': i18n.global.locale?.value || localStorage.getItem('locale') || 'zh-CN',
      'X-Request-ID': generateRandomString(12),
      ...(tenantId ? { 'X-Tenant-ID': tenantId } : {}),
    },
    signal: controller.signal,
    openWhenHidden: true,
    onmessage(ev) {
      if (!ev.data) return
      let parsed: PluginInstallEvent
      try {
        parsed = JSON.parse(ev.data) as PluginInstallEvent
      } catch {
        return
      }
      progressById.value = { ...progressById.value, [id]: parsed }
      if (parsed.done) {
        stopStream(id)
        load()
      }
    },
    onerror() {
      stopStream(id)
      throw new Error('plugin install stream closed')
    },
  }).catch(() => stopStream(id))
}

function syncStreams() {
  const wanted = new Set(plugins.value.filter(p => p.status === 'installing').map(p => p.id))
  for (const id of wanted) follow(id)
  for (const id of [...abortById.keys()]) if (!wanted.has(id)) stopStream(id)
}

/* ---------- mutations ---------- */

async function runOn(item: PluginEntity, fn: () => Promise<unknown>) {
  busyId.value = item.id
  try {
    await fn()
    await load()
  } catch (e: any) {
    MessagePlugin.error(e?.message || t('settings.plugins.actionFailed'))
  } finally {
    busyId.value = ''
  }
}

function toggle(item: PluginEntity, enabled: boolean) {
  return runOn(item, () => setPluginEnabled(scope.value, item.id, enabled))
}

function askUninstall(item: PluginEntity) {
  const dialog = DialogPlugin.confirm({
    header: t('settings.plugins.uninstall'),
    body: t('settings.plugins.uninstallConfirm', { name: item.plugin_id }),
    theme: 'warning',
    confirmBtn: { content: t('settings.plugins.uninstall'), theme: 'danger' },
    onConfirm: async () => {
      dialog.hide()
      await runOn(item, () => uninstallPlugin(scope.value, item.id))
    },
  })
}

const registerVisible = ref(false)
const registerForm = ref({
  plugin_id: '',
  kind: 'websearch' as PluginKind,
  transport: PLUGIN_TRANSPORTS[0],
  endpoint: '',
  policy_class: PLUGIN_POLICIES[0],
})

function openRegister() {
  registerForm.value = {
    plugin_id: '',
    kind: 'websearch',
    transport: PLUGIN_TRANSPORTS[0],
    endpoint: '',
    policy_class: PLUGIN_POLICIES[0],
  }
  registerVisible.value = true
}

async function submitRegister() {
  const form = registerForm.value
  if (!form.plugin_id.trim() || !form.endpoint.trim()) {
    MessagePlugin.warning(t('settings.plugins.missingFields'))
    return
  }
  submitting.value = true
  try {
    await registerPlugin(scope.value, {
      plugin_id: form.plugin_id.trim(),
      kind: form.kind,
      transport: form.transport,
      endpoint: form.endpoint.trim(),
      policy_class: form.policy_class,
    })
    registerVisible.value = false
    await load()
  } catch (e: any) {
    MessagePlugin.error(e?.message || t('settings.plugins.actionFailed'))
  } finally {
    submitting.value = false
  }
}

const installVisible = ref(false)
const installForm = ref({
  plugin_id: '',
  source_type: 'image' as (typeof SOURCE_TYPES)[number],
  source_url: '',
  source_ref: '',
  manifest: '',
})

function openInstall() {
  installForm.value = { plugin_id: '', source_type: 'image', source_url: '', source_ref: '', manifest: '' }
  installVisible.value = true
}

async function submitInstall() {
  const form = installForm.value
  if (!form.plugin_id.trim() || !form.source_url.trim() || !form.manifest.trim()) {
    MessagePlugin.warning(t('settings.plugins.missingFields'))
    return
  }
  submitting.value = true
  try {
    const res = await installPlugin(scope.value, {
      plugin_id: form.plugin_id.trim(),
      source_type: form.source_type,
      source_url: form.source_url.trim(),
      source_ref: form.source_ref.trim() || undefined,
      manifest: form.manifest,
    })
    installVisible.value = false
    // The row exists before the image does; follow it so the card shows the
    // build rather than a silent "installing".
    if (res?.data?.id) follow(res.data.id)
    await load()
  } catch (e: any) {
    MessagePlugin.error(e?.message || t('settings.plugins.actionFailed'))
  } finally {
    submitting.value = false
  }
}

const reconnectVisible = ref(false)
const reconnectEndpoint = ref('')
const editing = ref<PluginEntity | null>(null)

function openReconnect(item: PluginEntity) {
  editing.value = item
  reconnectEndpoint.value = item.endpoint || ''
  reconnectVisible.value = true
}

async function submitReconnect() {
  const item = editing.value
  if (!item || !reconnectEndpoint.value.trim()) return
  submitting.value = true
  try {
    await reconnectPlugin(scope.value, item.id, reconnectEndpoint.value.trim())
    reconnectVisible.value = false
    await load()
  } catch (e: any) {
    MessagePlugin.error(e?.message || t('settings.plugins.actionFailed'))
  } finally {
    submitting.value = false
  }
}

const envsVisible = ref(false)
const envRows = ref<{ key: string; value: string }[]>([])

// Values never come back from the server, so the rows start empty: saving is
// a full replace, and a blank row would blank a live credential.
function openEnvs(item: PluginEntity) {
  editing.value = item
  envRows.value = (item.env_keys || []).map(key => ({ key, value: '' }))
  if (envRows.value.length === 0) envRows.value = [{ key: '', value: '' }]
  envsVisible.value = true
}

async function submitEnvs() {
  const item = editing.value
  if (!item) return
  const envs: Record<string, string> = {}
  for (const row of envRows.value) {
    const key = row.key.trim()
    if (key) envs[key] = row.value
  }
  submitting.value = true
  try {
    await setPluginEnvs(scope.value, item.id, envs)
    envsVisible.value = false
    await load()
  } catch (e: any) {
    MessagePlugin.error(e?.message || t('settings.plugins.actionFailed'))
  } finally {
    submitting.value = false
  }
}
</script>

<style lang="less" scoped>
.plugin-settings {
  width: 100%;
}

.section-header {
  margin-bottom: 20px;

  &__title-row {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 8px;
  }

  h2 {
    font-size: 20px;
    font-weight: 600;
    color: var(--td-text-color-primary);
    margin: 0;
  }

  .section-description {
    font-size: 14px;
    color: var(--td-text-color-secondary);
    margin: 0;
    line-height: 1.6;
  }
}

.plugin-settings__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;

  &-actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }
}

.plugin-settings__hint {
  margin: 0 0 12px;
  font-size: 12px;
  color: var(--td-text-color-placeholder);
}

.plugin-settings__env-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}

.loading-container {
  padding: 40px 0;
  text-align: center;
}

.empty-state {
  padding: 80px 0;
  text-align: center;
}

.plugin-list {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 10px;
  align-items: stretch;
}

.plugin-card {
  position: relative;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  border: 1px solid var(--td-component-stroke);
  border-radius: 10px;
  background: var(--td-bg-color-container);
  transition: border-color 0.18s ease, box-shadow 0.18s ease;
  min-width: 0;
  height: 100%;

  &--ready .plugin-card__badge {
    background: color-mix(in srgb, var(--td-brand-color) 12%, transparent);
    color: var(--td-brand-color);
  }
}

.plugin-card__main {
  display: flex;
  align-items: stretch;
  gap: 10px;
  padding: 10px 12px;
  min-width: 0;
  flex: 1;
}

.plugin-card__badge {
  flex-shrink: 0;
  align-self: flex-start;
  width: 28px;
  height: 28px;
  border-radius: 7px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--td-bg-color-secondarycontainer);
  color: var(--td-text-color-secondary);
}

.plugin-card__body {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.plugin-card__header {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  min-height: 28px;
}

.plugin-card__heading {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: baseline;
  gap: 6px;
}

.plugin-card__title {
  flex: 0 1 auto;
  min-width: 0;
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  line-height: 1.35;
  color: var(--td-text-color-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.plugin-card__type {
  flex-shrink: 0;
  font-size: 12px;
  font-weight: 500;
  line-height: 1.35;
  color: var(--td-text-color-placeholder);
}

.plugin-card__actions {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 2px;
}

.plugin-card__icon-btn {
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  margin: 0;
  padding: 0;
  border: 0;
  border-radius: 6px;
  background: none;
  color: var(--td-text-color-secondary);
  cursor: pointer;

  &:hover:not(:disabled) {
    color: var(--td-text-color-primary);
    background: var(--td-bg-color-container-hover);
  }

  &--danger:hover:not(:disabled) {
    color: var(--td-error-color);
    background: color-mix(in srgb, var(--td-error-color) 8%, transparent);
  }

  &:disabled {
    cursor: not-allowed;
    opacity: 0.4;
  }
}

.plugin-card__desc {
  margin: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12px;
  line-height: 1.45;
  color: var(--td-text-color-secondary);
}

.plugin-card__error {
  margin: 0;
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  overflow: hidden;
  font-size: 12px;
  line-height: 1.45;
  color: var(--td-error-color);
  word-break: break-word;
}

.plugin-card__footer {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: auto;
}

.plugin-card__meta {
  font-size: 12px;
  line-height: 20px;
  color: var(--td-text-color-placeholder);
}

.plugin-card__chip {
  display: inline-flex;
  align-items: center;
  padding: 1px 8px;
  border-radius: 8px;
  background: var(--td-bg-color-secondarycontainer);
  color: var(--td-text-color-secondary);
  font-size: 12px;
  line-height: 20px;

  &--ready {
    color: var(--td-brand-color);
    background: color-mix(in srgb, var(--td-brand-color) 10%, transparent);
  }

  &--failed {
    color: var(--td-error-color);
    background: color-mix(in srgb, var(--td-error-color) 10%, transparent);
  }

  &--installing,
  &--removing {
    color: var(--td-warning-color);
    background: color-mix(in srgb, var(--td-warning-color) 10%, transparent);
  }
}
</style>
