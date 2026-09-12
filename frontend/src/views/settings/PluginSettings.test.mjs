import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = readFileSync(new URL('./PluginSettings.vue', import.meta.url), 'utf8')
const picker = readFileSync(new URL('../knowledge/settings/DataSourceEditorDialog.vue', import.meta.url), 'utf8')

test('plugin mutations and install streams use scoped plugin identifiers', () => {
  for (const operation of ['setPluginEnabled', 'uninstallPlugin', 'reconnectPlugin', 'setPluginEnvs']) {
    assert.match(source, new RegExp(`${operation}\\(scope\\.value, item\\.plugin_id`))
    assert.doesNotMatch(source, new RegExp(`${operation}\\(scope\\.value, item\\.id`))
  }
  assert.match(source, /follow\(res\.data\.plugin_id\)/)
  assert.match(source, /map\(p => p\.plugin_id\)/)
})

test('plugin settings distinguish system authority and health from installation state', () => {
  assert.match(source, /authStore\.isSystemAdmin/)
  assert.doesNotMatch(source, /authStore\.canAccessAllTenants/)
  assert.match(source, /getPlugin\(requestedScope, item\.plugin_id\)/)
  assert.match(source, /policy_class: form\.policy_class/)
  assert.match(source, /key && !row\.value/)
})

test('datasource picker accepts the unwrapped connector array', () => {
  assert.match(picker, /serverConnectors\.value = Array\.isArray\(res\) \? res : \[\]/)
  assert.doesNotMatch(picker, /res\?\.data\?\.data/)
})
