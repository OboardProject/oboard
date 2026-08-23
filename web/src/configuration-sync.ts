export type ConfigurationSyncRow = {
  server_id: number
  desired_revision?: number
  state: 'pending' | 'preparing' | 'queued' | 'running' | 'synced' | 'failed' | string
  config_version?: number
  task_id?: number
  retry_count?: number
  error?: string
}

export type ConfigurationSyncPresentation = {
  tone: 'info' | 'ok' | 'warn' | 'danger'
  label: string
  retryServerIDs: number[]
  busy: boolean
}

export function configurationSyncPresentation(rows: ConfigurationSyncRow[], saving = false, retrying = false): ConfigurationSyncPresentation {
  const failed = rows.filter(item => item.state === 'failed')
  const active = rows.filter(item => ['pending', 'preparing', 'queued', 'running'].includes(item.state))
  const synced = rows.length > 0 && rows.every(item => item.state === 'synced')
  if (saving) return { tone: 'info', label: '正在保存...', retryServerIDs: [], busy: true }
  if (retrying) return { tone: 'info', label: '正在重试同步...', retryServerIDs: failed.map(item => item.server_id), busy: true }
  if (failed.length > 0) return { tone: 'danger', label: `${failed.length} 台同步失败，点击重试`, retryServerIDs: failed.map(item => item.server_id), busy: false }
  if (active.length > 0) return { tone: 'info', label: `正在同步 ${active.length} 台服务器`, retryServerIDs: [], busy: true }
  if (synced) return { tone: 'ok', label: '配置已同步', retryServerIDs: [], busy: false }
  return { tone: 'warn', label: '配置已保存', retryServerIDs: [], busy: false }
}

const mutationCollections: Record<string, { collection: string; singular: string }> = {
  servers: { collection: 'servers', singular: 'server' },
  inbounds: { collection: 'inbounds', singular: 'inbound' },
  outbounds: { collection: 'outbounds', singular: 'outbound' },
  'external-outbounds': { collection: 'external_outbounds', singular: 'external_outbound' },
  'proxy-paths': { collection: 'proxy_paths', singular: 'proxy_path' },
  'proxy-path-steps': { collection: 'proxy_path_steps', singular: 'proxy_path_step' },
  'routing-rules': { collection: 'routing_rules', singular: 'routing_rule' },
  'routing-rule-sets': { collection: 'routing_rule_sets', singular: 'routing_rule_set' },
  'port-forwards': { collection: 'port_forwards', singular: 'port_forward' },
  tunnels: { collection: 'tunnels', singular: 'tunnel' },
  'user-groups': { collection: 'user_groups', singular: 'user_group' },
  'user-group-members': { collection: 'user_group_members', singular: 'user_group_member' },
  users: { collection: 'users', singular: 'user' },
  'dns-lists': { collection: 'dns_lists', singular: 'dns_list' },
  'node-presets': { collection: 'node_presets', singular: 'node_preset' },
  'snell-profiles': { collection: 'snell_profiles', singular: 'snell_profile' },
  'warp-profiles': { collection: 'warp_profiles', singular: 'warp_profile' },
}

export function isConfigurationMutationPath(path: string) {
  const normalized = path.replace(/^\/api\/(?:v1|v2\/ui|v2)\//, '')
  const parts = normalized.split('/').filter(Boolean)
  return Boolean(mutationCollections[parts[0] || ''])
}

function mutationResource(path: string) {
  const normalized = path.replace(/^\/api\/(?:v1|v2\/ui|v2)\//, '')
  const parts = normalized.split('/').filter(Boolean)
  const resource = mutationCollections[parts[0] || '']
  return resource ? { ...resource, id: Number(parts[1] || 0) } : null
}

function mergeMutationCollection(current: any[], incoming: any[]) {
  const byID = new Map(current.map(item => [Number(item.id), item]))
  incoming.forEach(item => {
    if (item && Number(item.id) > 0) byID.set(Number(item.id), { ...byID.get(Number(item.id)), ...item })
  })
  return Array.from(byID.values())
}

export function mergeConfigurationMutationResponse<T extends Record<string, any>>(current: T, response: any, path: string): T {
  let next = mergeConfigurationSyncResponse(current, response)
  const resource = mutationResource(path)
  if (!resource) return next
  const { collection, singular, id } = resource
  const incoming = [
    ...(Array.isArray(response?.[collection]) ? response[collection] : []),
    ...(response?.[singular]?.id ? [response[singular]] : []),
  ]
  if (incoming.length) return { ...next, [collection]: mergeMutationCollection(Array.isArray(next[collection]) ? next[collection] : [], incoming) }
  if (response?.deleted === true && id > 0 && Array.isArray(next[collection])) {
    return { ...next, [collection]: next[collection].filter((item: any) => Number(item.id) !== id) }
  }
  return next
}

export function mergeConfigurationSyncResponse<T extends Record<string, any>>(current: T, response: any): T {
  if (!response || !Array.isArray(response.configuration_sync)) return current
  return {
    ...current,
    desired_revision: response.desired_revision ?? current.desired_revision,
    configuration_sync: response.configuration_sync,
  }
}

export class MutationActivityTracker {
  private pending = 0

  update(started: boolean): boolean {
    this.pending = Math.max(0, this.pending + (started ? 1 : -1))
    return this.pending > 0
  }

  reset() {
    this.pending = 0
  }

  get count() {
    return this.pending
  }
}
