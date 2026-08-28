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

export type ConfigurationSyncFailureIssue = {
  key: string
  title: string
  explanation: string
  resolution: string
  rawError: string
  serverIDs: number[]
  taskIDs: number[]
  inboundID?: number
  conflictingPathNames?: string[]
  targetTab: 'proxy-paths' | 'tasks'
  targetLabel: string
}

function describeConfigurationSyncError(rawError: string) {
  const directBranch = rawError.match(/入口\s+(\d+).*(?:相同位置的直接出口分支|同一分支位置存在多条直接出口|直接出口分支.*位于同一位置)/)
  if (directBranch) {
    const inboundID = directBranch[1]
    const namedPaths = rawError.match(/直接出口分支「([^」]+)」与「([^」]+)」位于同一位置/)
    return {
      title: `入口 ${inboundID} 存在重复的直接出口分支`,
      explanation: '同一个入口在同一分叉位置只能保留一条直接出口分支，否则无法确定应使用哪条直出路由。',
      resolution: `前往「代理拓扑」，找到入口 ${inboundID}，删除或停用同一位置的重复直出分支。保存后系统会自动重新同步。`,
      inboundID: Number(inboundID),
      conflictingPathNames: namedPaths ? [namedPaths[1], namedPaths[2]] : undefined,
      targetTab: 'proxy-paths' as const,
      targetLabel: '打开代理拓扑',
    }
  }
  return {
    title: '配置生成或下发失败',
    explanation: rawError || 'Controller 没有返回具体错误信息。',
    resolution: '请在「任务部署中心」查看对应任务和服务器日志，修正配置或运行环境后再重试。',
    targetTab: 'tasks' as const,
    targetLabel: '打开任务部署中心',
  }
}

export function configurationSyncFailureIssues(rows: ConfigurationSyncRow[]): ConfigurationSyncFailureIssue[] {
  const groups = new Map<string, { rawError: string; rows: ConfigurationSyncRow[] }>()
  rows.filter(item => item.state === 'failed').forEach(item => {
    const rawError = String(item.error || '').trim()
    const key = rawError || '__missing_error__'
    const current = groups.get(key)
    if (current) current.rows.push(item)
    else groups.set(key, { rawError, rows: [item] })
  })
  return Array.from(groups.entries()).map(([key, group]) => {
    const description = describeConfigurationSyncError(group.rawError)
    return {
      key,
      ...description,
      rawError: group.rawError,
      serverIDs: group.rows.map(item => item.server_id),
      taskIDs: group.rows.map(item => Number(item.task_id || 0)).filter(Boolean),
    }
  })
}

const configurationSyncBusyStates = ['pending', 'preparing', 'queued', 'running'] as const

export function configurationSyncBusyRows(rows: ConfigurationSyncRow[]) {
  return rows.filter(item => (configurationSyncBusyStates as readonly string[]).includes(item.state))
}

export function configurationSyncBusyStateLabel(state: string) {
  if (state === 'preparing') return '准备中'
  if (state === 'queued') return '排队中'
  if (state === 'running') return '下发中'
  return '等待中'
}

export function configurationSyncPresentation(rows: ConfigurationSyncRow[], saving = false, retrying = false): ConfigurationSyncPresentation {
  const failed = rows.filter(item => item.state === 'failed')
  const active = configurationSyncBusyRows(rows)
  const synced = rows.length > 0 && rows.every(item => item.state === 'synced')
  if (saving) return { tone: 'info', label: '正在保存...', retryServerIDs: [], busy: true }
  if (retrying) return { tone: 'info', label: '正在重试同步...', retryServerIDs: failed.map(item => item.server_id), busy: true }
  if (failed.length > 0) {
    const issueCount = configurationSyncFailureIssues(failed).length
    return { tone: 'danger', label: `配置同步被阻塞 · ${issueCount} 个问题`, retryServerIDs: failed.map(item => item.server_id), busy: false }
  }
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
