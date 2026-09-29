import type {
  AuditEvent, Catalog, CustomVar, EditorDiagnostics, Grant, Installation, InstallationDetail, InstanceDetail,
  PackagePreview, PackageSource, RequestFn, Run, RunLog, RuntimeStatus, Schedule, ServerOption, StateEntry,
} from './types'

const json = (body: unknown): RequestInit => ({ body: JSON.stringify(body) })

export const listInstallations = (request: RequestFn) => request<{ plugins: Installation[] }>('/plugins')
export const getInstallation = (request: RequestFn, id: number) => request<InstallationDetail>(`/plugins/${id}`)
export const setInstallationEnabled = (request: RequestFn, id: number, enabled: boolean) =>
  request<InstallationDetail>(`/plugins/${id}`, { method: 'PATCH', ...json({ enabled }) })
export const uninstall = (request: RequestFn, id: number) =>
  request<{ uninstalled: boolean }>(`/plugins/${id}/uninstall`, { method: 'POST', ...json({ confirm: true }) })
export const activateVersion = (request: RequestFn, id: number, packageID: number) =>
  request(`/plugins/${id}/versions/${packageID}/activate`, { method: 'POST', ...json({ confirm: true }) })
export const catalog = (request: RequestFn) => request<Catalog>('/plugins/catalog')
export const serverOptions = (request: RequestFn) => request<{ servers: ServerOption[] }>('/plugins/servers')

export const previewPackage = (request: RequestFn, source: PackageSource) =>
  request<PackagePreview>('/plugins/packages/preview', { method: 'POST', ...json({ source }) })
export const installPackage = (request: RequestFn, source: PackageSource, expectedSHA256: string) =>
  request<{ installation_id: number; instance_id?: number; review_required: boolean }>('/plugins/packages/install', { method: 'POST', ...json({ source, expected_sha256: expectedSHA256, confirm: true }) })

export const diagnoseDraft = (request: RequestFn, manifest: string, source: string) =>
  request<EditorDiagnostics>('/plugins/drafts/diagnose', { method: 'POST', ...json({ manifest, source }) })
export const createDraftPlugin = (request: RequestFn, manifest: string, source: string) =>
  request<{ plugin_id: number }>('/plugins/drafts', { method: 'POST', ...json({ manifest, source }) })
export const saveDraft = (request: RequestFn, id: number, manifest: string, source: string) =>
  request(`/plugins/${id}/draft`, { method: 'PUT', ...json({ manifest, source }) })
export const publishDraft = (request: RequestFn, id: number) =>
  request(`/plugins/${id}/draft/publish`, { method: 'POST', ...json({ confirm: true }) })

export const createInstance = (request: RequestFn, id: number, name: string) =>
  request<InstanceDetail>(`/plugins/${id}/instances`, { method: 'POST', ...json({ name }) })
export const getInstance = (request: RequestFn, id: number) => request<InstanceDetail>(`/plugin-instances/${id}`)
export const updateInstance = (request: RequestFn, id: number, input: { name?: string; enabled?: boolean; resume?: boolean }) =>
  request<InstanceDetail>(`/plugin-instances/${id}`, { method: 'PATCH', ...json(input) })
export const deleteInstance = (request: RequestFn, id: number) => request(`/plugin-instances/${id}`, { method: 'DELETE' })
export const saveEnvironment = (request: RequestFn, id: number, expectedRevision: number, values: Record<string, unknown>, custom: CustomVar[]) =>
  request<InstanceDetail>(`/plugin-instances/${id}/environment`, { method: 'PUT', ...json({ expected_revision: expectedRevision, values, custom }) })
export const setSecret = (request: RequestFn, id: number, name: string, value: string) =>
  request<{ configured: boolean }>(`/plugin-instances/${id}/secrets/${encodeURIComponent(name)}`, { method: 'PUT', ...json({ value }) })
export const setGrant = (request: RequestFn, id: number, expectedRevision: number, grant: Grant) =>
  request<InstanceDetail>(`/plugin-instances/${id}/grant`, { method: 'PUT', ...json({ expected_revision: expectedRevision, grant }) })
export const revokeGrant = (request: RequestFn, id: number) => request(`/plugin-instances/${id}/grant`, { method: 'DELETE' })
export const runInstance = (request: RequestFn, id: number, idempotencyKey: string) =>
  request<{ run: Run }>(`/plugin-instances/${id}/runs`, { method: 'POST', ...json({ idempotency_key: idempotencyKey }) })
export const listInstanceRuns = (request: RequestFn, id: number, limit = 30) => request<{ runs: Run[] }>(`/plugin-instances/${id}/runs?limit=${limit}`)
export const createSchedule = (request: RequestFn, id: number, input: Partial<Schedule>) =>
  request<Schedule>(`/plugin-instances/${id}/schedules`, { method: 'POST', ...json(input) })
export const updateSchedule = (request: RequestFn, id: number, input: Partial<Schedule>) =>
  request<Schedule>(`/plugin-schedules/${id}`, { method: 'PATCH', ...json(input) })
export const deleteSchedule = (request: RequestFn, id: number) => request(`/plugin-schedules/${id}`, { method: 'DELETE' })
export const listState = (request: RequestFn, id: number) =>
  request<{ entries: StateEntry[]; usage: InstanceDetail['state'] }>(`/plugin-instances/${id}/state`)
export const clearState = (request: RequestFn, id: number, key = '') =>
  request(`/plugin-instances/${id}/state${key ? `?key=${encodeURIComponent(key)}` : ''}`, { method: 'DELETE' })
export const listAudit = (request: RequestFn, id: number, limit = 100) => request<{ events: AuditEvent[] }>(`/plugin-instances/${id}/audit?limit=${limit}`)

export const listRuns = (request: RequestFn, filter: { pluginID?: number; instanceID?: number; limit?: number } = {}) => {
  const query = new URLSearchParams()
  if (filter.pluginID) query.set('plugin_id', String(filter.pluginID))
  if (filter.instanceID) query.set('instance_id', String(filter.instanceID))
  query.set('limit', String(filter.limit || 50))
  return request<{ runs: Run[] }>(`/plugin-runs?${query}`)
}
export const getRun = (request: RequestFn, id: number) => request<{ run: Run }>(`/plugin-runs/${id}`)
export const runLogs = (request: RequestFn, id: number, afterSeq = 0) => request<{ logs: RunLog[] }>(`/plugin-runs/${id}/logs?after_seq=${afterSeq}`)
export const cancelRun = (request: RequestFn, id: number) => request<{ run: Run }>(`/plugin-runs/${id}/cancel`, { method: 'POST', ...json({}) })

export const runtimeStatus = (request: RequestFn) => request<RuntimeStatus>('/plugin-runtime')
export const updateRuntime = (request: RequestFn, input: Partial<Pick<RuntimeStatus, 'enabled' | 'scheduler_paused' | 'max_concurrency' | 'max_timeout_seconds' | 'log_retention_days' | 'run_retention_days'>>) =>
  request<RuntimeStatus>('/plugin-runtime', { method: 'PATCH', ...json(input) })
