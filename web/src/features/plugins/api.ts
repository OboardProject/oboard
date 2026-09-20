import type { Plugin, PluginGrant, PluginRevision, PluginRun, PluginTrigger, PluginPackageInput, PluginPackageInstallInput, PluginPackagePreview, PluginPackageVersion, PluginInstallation, PluginUIDocument } from './types'

type RequestV2 = (path: string, init?: RequestInit) => Promise<any>

export function listPlugins(requestV2: RequestV2, status = ''): Promise<{ plugins: Plugin[] }> {
  const query = status ? `?status=${encodeURIComponent(status)}` : ''
  return requestV2(`/plugins${query}`)
}

export function createPlugin(requestV2: RequestV2, input: { name: string; description: string }): Promise<{ plugin: Plugin }> {
  return requestV2('/plugins', { method: 'POST', body: JSON.stringify(input) })
}

export function getPlugin(requestV2: RequestV2, id: number): Promise<{ plugin: Plugin; draft?: PluginRevision; published?: PluginRevision; revisions: PluginRevision[] }> {
  return requestV2(`/plugins/${id}`)
}

export function saveDraft(requestV2: RequestV2, pluginID: number, source: string, manifest: any): Promise<{ revision: PluginRevision }> {
  return requestV2(`/plugins/${pluginID}/revisions`, { method: 'POST', body: JSON.stringify({ source, manifest }) })
}

export function validatePlugin(requestV2: RequestV2, pluginID: number, source: string, manifest: any, params: any): Promise<any> {
  return requestV2(`/plugins/${pluginID}/validate`, { method: 'POST', body: JSON.stringify({ source, manifest, params }) })
}

export function simulatePlugin(requestV2: RequestV2, pluginID: number, revisionID: number, params: any, env: any, idempotencyKey: string): Promise<{ run: PluginRun }> {
  return requestV2(`/plugins/${pluginID}/simulate`, { method: 'POST', body: JSON.stringify({ revision_id: revisionID, params, env, idempotency_key: idempotencyKey }) })
}

export function runPlugin(requestV2: RequestV2, pluginID: number, revisionID: number, params: any, env: any, idempotencyKey: string): Promise<{ run: PluginRun }> {
  return requestV2(`/plugins/${pluginID}/runs`, { method: 'POST', body: JSON.stringify({ revision_id: revisionID, params, env, idempotency_key: idempotencyKey }) })
}

export function publishRevision(requestV2: RequestV2, pluginID: number, revisionID: number): Promise<{ revision: PluginRevision }> {
  return requestV2(`/plugins/${pluginID}/publish`, { method: 'POST', body: JSON.stringify({ revision_id: revisionID }) })
}

export function listTriggers(requestV2: RequestV2, pluginID?: number): Promise<{ triggers: PluginTrigger[] }> {
  const query = pluginID ? `?plugin_id=${pluginID}` : ''
  return requestV2(`/plugin-triggers${query}`)
}

export function createTrigger(requestV2: RequestV2, input: any): Promise<{ trigger: PluginTrigger }> {
  return requestV2('/plugin-triggers', { method: 'POST', body: JSON.stringify(input) })
}

export function updateTrigger(requestV2: RequestV2, id: number, input: any, expected: number): Promise<{ trigger: PluginTrigger }> {
  return requestV2(`/plugin-triggers/${id}?expected_binding_revision=${expected}`, { method: 'PATCH', body: JSON.stringify(input) })
}

export function listGrants(requestV2: RequestV2, pluginID?: number): Promise<{ grants: PluginGrant[] }> {
  const query = pluginID ? `?plugin_id=${pluginID}` : ''
  return requestV2(`/plugin-grants${query}`)
}

export function createGrant(requestV2: RequestV2, input: any): Promise<{ grant: PluginGrant }> {
  return requestV2('/plugin-grants', { method: 'POST', body: JSON.stringify(input) })
}

export function revokeGrant(requestV2: RequestV2, id: number): Promise<{ revoked: boolean }> {
  return requestV2(`/plugin-grants/${id}/revoke`, { method: 'POST', body: JSON.stringify({}) })
}

export function getRun(requestV2: RequestV2, id: number): Promise<{ run: PluginRun }> {
  return requestV2(`/plugin-runs/${id}`)
}

export function listRunLogs(requestV2: RequestV2, id: number): Promise<{ logs: any[] }> {
  return requestV2(`/plugin-runs/${id}/logs`)
}

export function listRunActions(requestV2: RequestV2, id: number): Promise<{ actions: any[] }> {
  return requestV2(`/plugin-runs/${id}/actions`)
}

export function cancelRun(requestV2: RequestV2, id: number): Promise<{ run: PluginRun }> {
  return requestV2(`/plugin-runs/${id}/cancel`, { method: 'POST', body: JSON.stringify({}) })
}

export function runtimeStatus(requestV2: RequestV2): Promise<{ status: any }> {
  return requestV2('/plugin-runtime/status')
}

export function updateRuntimeSettings(requestV2: RequestV2, input: any): Promise<{ status: any }> {
  return requestV2('/plugin-runtime/status', { method: 'PATCH', body: JSON.stringify(input) })
}

export function previewPackage(request: RequestV2, input: PluginPackageInput): Promise<PluginPackagePreview> {
  return request(input.source ? '/plugins/github/preview' : '/plugins/packages/preview', { method: 'POST', body: JSON.stringify(input.source || input) })
}

export async function installPackage(request: RequestV2, input: PluginPackageInstallInput, expectedSHA256: string): Promise<{ installation: PluginInstallation; version: PluginPackageVersion }> {
  const result = await request(input.source ? '/plugins/github/install' : '/plugins/packages/install', { method: 'POST', body: JSON.stringify({ ...(input.source || input), expected_sha256: expectedSHA256, confirm: true }) })
  if (!result?.installation?.installed || !Number.isSafeInteger(result.installation.plugin_id) || result.installation.plugin_id <= 0 || result.version?.plugin_id !== result.installation.plugin_id || !Number.isSafeInteger(result.version?.revision_id) || result.version.revision_id <= 0 || result.version.sha256 !== expectedSHA256 || (input.source && (result.version.source_kind !== 'github' || result.version.source_commit !== input.source.commit))) {
    throw new Error('安装结果无法确认，请刷新插件列表核对后再操作')
  }
  return result
}

export function packageVersions(request: RequestV2, id: number): Promise<{ versions: PluginPackageVersion[] }> {
  return request(`/plugins/${id}/versions`)
}

export function activateVersion(request: RequestV2, id: number, revisionID: number) {
  return request(`/plugins/${id}/versions/activate`, { method: 'POST', body: JSON.stringify({ revision_id: revisionID, confirm: true }) })
}

export function getConfig(request: RequestV2, id: number): Promise<{ installation: PluginInstallation }> {
  return request(`/plugins/${id}/config`)
}

export function saveConfig(request: RequestV2, id: number, config: Record<string, unknown>) {
  return request(`/plugins/${id}/config`, { method: 'PUT', body: JSON.stringify({ config }) })
}

export function pluginUI(request: RequestV2, id: number): Promise<{ ui: PluginUIDocument | null }> {
  return request(`/plugins/${id}/ui`)
}

export function setSecret(request: RequestV2, id: number, name: string, value: string) {
  return request(`/plugins/${id}/secrets/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify({ value }) })
}

export function deleteSecret(request: RequestV2, id: number, name: string) {
  return setSecret(request, id, name, '')
}

export function uninstallPlugin(request: RequestV2, id: number, retainData: boolean) {
  return request(`/plugins/${id}/uninstall`, { method: 'POST', body: JSON.stringify({ keep_state: retainData, confirm: true }) })
}

export function updatePluginStatus(request: RequestV2, item: Plugin, enabled: boolean): Promise<{ plugin: Plugin }> {
  return request(`/plugins/${item.id}`, { method: 'PATCH', body: JSON.stringify({ name: item.name, description: item.description, status: enabled ? 'enabled' : 'disabled', expected_updated_at: item.updated_at }) })
}

export const defaultManifest = {
  schema_version: 1,
  runtime: 'oboard-js-v1',
  sdk_version: 'oboard-sdk-v1',
  entry: 'main',
  capabilities: ['servers.status', 'notifications.send'],
  params_schema: { type: 'object', additionalProperties: false, properties: {} },
  env: [],
  limits: { timeout_seconds: 30, memory_mib: 64, sdk_calls: 20, manage_actions: 2 },
}

export const defaultSource = `function main() {
  log.info("plugin started");
  return { ok: true };
}
`

export interface PluginWebhook {
  id: string; plugin_id: number; binding_id: number; binding_revision: number
  revision_id: number; grant_id: number; enabled: boolean; generation: number
}

export type PluginWebhookGrant = PluginGrant & { binding_id?: number | null; expires_at?: string | null }

export function listWebhooks(request: RequestV2, pluginID: number): Promise<{ webhooks: PluginWebhook[] }> {
  return request(`/plugin-webhooks?plugin_id=${pluginID}`)
}

export function listWebhookGrants(request: RequestV2, pluginID: number): Promise<{ grants: PluginWebhookGrant[] }> {
  return request(`/plugin-grants?plugin_id=${pluginID}`)
}

export function createWebhook(request: RequestV2, bindingID: number, grantID: number): Promise<{ webhook: PluginWebhook; secret: string }> {
  return request('/plugin-webhooks', { method: 'POST', body: JSON.stringify({ binding_id: bindingID, grant_id: grantID }) })
}

export function changeWebhook(request: RequestV2, item: PluginWebhook, enabled: boolean, rotate = false): Promise<{ webhook: PluginWebhook; secret?: string }> {
  return request(`/plugin-webhooks/${encodeURIComponent(item.id)}`, { method: 'PATCH', body: JSON.stringify({ expected_generation: item.generation, enabled, rotate_secret: rotate }) })
}
