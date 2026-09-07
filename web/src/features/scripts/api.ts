import type { Script, ScriptGrant, ScriptRevision, ScriptRun, ScriptTrigger } from './types'

type RequestV2 = (path: string, init?: RequestInit) => Promise<any>

export function listScripts(requestV2: RequestV2, status = ''): Promise<{ scripts: Script[] }> {
  const query = status ? `?status=${encodeURIComponent(status)}` : ''
  return requestV2(`/scripts${query}`)
}

export function createScript(requestV2: RequestV2, input: { name: string; description: string }): Promise<{ script: Script }> {
  return requestV2('/scripts', { method: 'POST', body: JSON.stringify(input) })
}

export function getScript(requestV2: RequestV2, id: number): Promise<{ script: Script; draft?: ScriptRevision; published?: ScriptRevision; revisions: ScriptRevision[] }> {
  return requestV2(`/scripts/${id}`)
}

export function saveDraft(requestV2: RequestV2, scriptID: number, source: string, manifest: any): Promise<{ revision: ScriptRevision }> {
  return requestV2(`/scripts/${scriptID}/revisions`, { method: 'POST', body: JSON.stringify({ source, manifest }) })
}

export function validateScript(requestV2: RequestV2, scriptID: number, source: string, manifest: any, params: any): Promise<any> {
  return requestV2(`/scripts/${scriptID}/validate`, { method: 'POST', body: JSON.stringify({ source, manifest, params }) })
}

export function simulateScript(requestV2: RequestV2, scriptID: number, revisionID: number, params: any, env: any, idempotencyKey: string): Promise<{ run: ScriptRun }> {
  return requestV2(`/scripts/${scriptID}/simulate`, { method: 'POST', body: JSON.stringify({ revision_id: revisionID, params, env, idempotency_key: idempotencyKey }) })
}

export function runScript(requestV2: RequestV2, scriptID: number, revisionID: number, params: any, env: any, idempotencyKey: string): Promise<{ run: ScriptRun }> {
  return requestV2(`/scripts/${scriptID}/runs`, { method: 'POST', body: JSON.stringify({ revision_id: revisionID, params, env, idempotency_key: idempotencyKey }) })
}

export function publishRevision(requestV2: RequestV2, scriptID: number, revisionID: number): Promise<{ revision: ScriptRevision }> {
  return requestV2(`/scripts/${scriptID}/publish`, { method: 'POST', body: JSON.stringify({ revision_id: revisionID }) })
}

export function listTriggers(requestV2: RequestV2, scriptID?: number): Promise<{ triggers: ScriptTrigger[] }> {
  const query = scriptID ? `?script_id=${scriptID}` : ''
  return requestV2(`/script-triggers${query}`)
}

export function createTrigger(requestV2: RequestV2, input: any): Promise<{ trigger: ScriptTrigger }> {
  return requestV2('/script-triggers', { method: 'POST', body: JSON.stringify(input) })
}

export function updateTrigger(requestV2: RequestV2, id: number, input: any, expected: number): Promise<{ trigger: ScriptTrigger }> {
  return requestV2(`/script-triggers/${id}?expected_binding_revision=${expected}`, { method: 'PATCH', body: JSON.stringify(input) })
}

export function listGrants(requestV2: RequestV2, scriptID?: number): Promise<{ grants: ScriptGrant[] }> {
  const query = scriptID ? `?script_id=${scriptID}` : ''
  return requestV2(`/script-grants${query}`)
}

export function createGrant(requestV2: RequestV2, input: any): Promise<{ grant: ScriptGrant }> {
  return requestV2('/script-grants', { method: 'POST', body: JSON.stringify(input) })
}

export function revokeGrant(requestV2: RequestV2, id: number): Promise<{ revoked: boolean }> {
  return requestV2(`/script-grants/${id}/revoke`, { method: 'POST', body: JSON.stringify({}) })
}

export function getRun(requestV2: RequestV2, id: number): Promise<{ run: ScriptRun }> {
  return requestV2(`/script-runs/${id}`)
}

export function listRunLogs(requestV2: RequestV2, id: number): Promise<{ logs: any[] }> {
  return requestV2(`/script-runs/${id}/logs`)
}

export function listRunActions(requestV2: RequestV2, id: number): Promise<{ actions: any[] }> {
  return requestV2(`/script-runs/${id}/actions`)
}

export function cancelRun(requestV2: RequestV2, id: number): Promise<{ run: ScriptRun }> {
  return requestV2(`/script-runs/${id}/cancel`, { method: 'POST', body: JSON.stringify({}) })
}

export function runtimeStatus(requestV2: RequestV2): Promise<{ status: any }> {
  return requestV2('/script-runtime/status')
}

export function updateRuntimeSettings(requestV2: RequestV2, input: any): Promise<{ status: any }> {
  return requestV2('/script-runtime/status', { method: 'PATCH', body: JSON.stringify(input) })
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
  log.info("script started");
  return { ok: true };
}
`
