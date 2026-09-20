export type ConfigurationSyncProblem = {
 code: string
 category?: string
 resources?: { type: string; id: string }[]
 retry_policy?: string
 message?: string
}

export type ConfigurationSyncRow = {
  server_id: number
  desired_revision?: number
  state: 'pending' | 'preparing' | 'queued' | 'running' | 'synced' | 'failed' | string
  config_version?: number
  task_id?: number
  retry_count?: number
  error?: string
  problems?: ConfigurationSyncProblem[]
  agent_reachable?: boolean
}

export type ConfigurationSyncServerRef = {
  id: number
  name?: string
  status?: string
  agent_id?: string
}

export type ConfigurationSyncPresentation = {
  tone: 'info' | 'ok' | 'warn' | 'danger'
  label: string
  retryServerIDs: number[]
  busy: boolean
}

export type ConfigurationSyncFailureIssue = {
  key: string
  kind: 'busy' | 'config'
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

export function isConfigurationSyncBusyError(rawError: string) {
  return /SQLITE_BUSY|database is locked/i.test(String(rawError || ''))
}

function describeConfigurationSyncError(rawError: string) {
  if (isConfigurationSyncBusyError(rawError)) {
    return {
      kind: 'busy' as const,
      title: '配置同步暂时中断',
      explanation: '配置已经保存，但后续同步尚未完成。这不是节点配置错误，不需要修改代理链路。',
      resolution: '请重试同步。如果再次中断，可在任务记录中查看原因。',
      targetTab: 'tasks' as const,
      targetLabel: '查看任务记录',
    }
  }
  const directBranch = rawError.match(/入口\s+(\d+).*(?:相同位置的直接出口分支|同一分支位置存在多条直接出口|直接出口分支.*位于同一位置)/)
  if (directBranch) {
    const inboundID = directBranch[1]
    const namedPaths = rawError.match(/直接出口分支「([^」]+)」与「([^」]+)」位于同一位置/)
    return {
      kind: 'config' as const,
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
    kind: 'config' as const,
    title: '配置生成或下发失败',
    explanation: '后续配置同步未完成，相关服务器可能仍在使用此前的配置。具体原因请展开诊断详情。',
    resolution: '请在「任务」查看对应记录，确认原因并处理后再重试。',
    targetTab: 'tasks' as const,
    targetLabel: '打开任务',
  }
}

export function configurationSyncFailureIssues(rows: ConfigurationSyncRow[]): ConfigurationSyncFailureIssue[] {
  const groups = new Map<string, { rawError: string; rows: ConfigurationSyncRow[] }>()
  const structured = new Map<string, ConfigurationSyncFailureIssue>()
  rows.filter(item => item.state === 'failed').forEach(item => {
    if (Array.isArray(item.problems) && item.problems.length > 0) {
      item.problems.slice(0, 16).forEach(problem => {
        const resources = Array.isArray(problem?.resources) ? problem.resources : []
        const key = JSON.stringify([problem?.code, resources, problem?.retry_policy])
        const previous = structured.get(key)
        if (previous) {
          if (!previous.serverIDs.includes(item.server_id)) previous.serverIDs.push(item.server_id)
          if (item.task_id && !previous.taskIDs.includes(item.task_id)) previous.taskIDs.push(item.task_id)
          return
        }
        const busy = problem?.code === 'database_busy'
        const duplicate = problem?.code === 'duplicate_direct_paths'
        const ref = resources.find(resource => resource?.type === 'inbound' && /^[1-9]\d*$/.test(resource.id))
        const inboundID = ref && Number.isSafeInteger(Number(ref.id)) ? Number(ref.id) : undefined
        structured.set(key, {
          key, kind: busy ? 'busy' : 'config',
          title: busy ? '主控数据库正忙' : duplicate ? '入口存在重复的直接出口分支' : '配置生成或下发失败',
          explanation: typeof problem?.message === 'string' ? problem.message : 'Controller 没有返回具体错误信息。',
          resolution: problem?.retry_policy === 'automatic' ? '系统将自动重试，请稍后查看。' : problem?.retry_policy === 'after_change' ? '请检查相关配置，保存后系统会重新同步。' : '请查看任务记录和服务器日志。',
          rawError: typeof problem?.message === 'string' ? problem.message : '',
          serverIDs: [item.server_id], taskIDs: item.task_id ? [item.task_id] : [],
          inboundID: duplicate ? inboundID : undefined,
          targetTab: duplicate && inboundID ? 'proxy-paths' : 'tasks',
          targetLabel: duplicate && inboundID ? '打开代理拓扑' : '查看任务记录',
        })
      })
      return
    }
    const rawError = String(item.error || '').trim()
    const key = rawError || '__missing_error__'
    const current = groups.get(key)
    if (current) current.rows.push(item)
    else groups.set(key, { rawError, rows: [item] })
  })
  return [...structured.values(), ...Array.from(groups.entries()).map(([key, group]) => {
    const description = describeConfigurationSyncError(group.rawError)
    return {
      key,
      ...description,
      rawError: group.rawError,
      serverIDs: group.rows.map(item => item.server_id),
      taskIDs: group.rows.map(item => Number(item.task_id || 0)).filter(Boolean),
    }
  })]
}

const configurationSyncBusyStates = ['pending', 'preparing', 'queued', 'running'] as const

export function configurationSyncAgentReachable(row: ConfigurationSyncRow, servers: ConfigurationSyncServerRef[] = []) {
  if (row.agent_reachable === false) return false
  if (row.agent_reachable === true) return true
  const server = servers.find(item => Number(item.id) === Number(row.server_id))
  if (!server) return true
  if (server.agent_id !== undefined && !String(server.agent_id || '').trim()) return false
  if (server.status !== undefined && String(server.status || '').toLowerCase() === 'offline') return false
  return true
}

export function configurationSyncBusyRows(rows: ConfigurationSyncRow[], servers: ConfigurationSyncServerRef[] = []) {
  return rows.filter(item => (configurationSyncBusyStates as readonly string[]).includes(item.state) && configurationSyncAgentReachable(item, servers))
}

export function configurationSyncFailedRows(rows: ConfigurationSyncRow[], _servers: ConfigurationSyncServerRef[] = []): ConfigurationSyncRow[] {
  return rows.filter(item => item.state === 'failed')
}

export function configurationSyncBusyStateLabel(state: string) {
  if (state === 'preparing') return '准备中'
  if (state === 'queued') return '排队中'
  if (state === 'running') return '下发中'
  return '等待中'
}

export function configurationSyncPresentation(rows: ConfigurationSyncRow[], saving = false, retrying = false, servers: ConfigurationSyncServerRef[] = []): ConfigurationSyncPresentation {
  const failed = configurationSyncFailedRows(rows, servers)
  const active = configurationSyncBusyRows(rows, servers)
  const reachable = rows.filter(item => configurationSyncAgentReachable(item, servers))
  const synced = reachable.length > 0 && reachable.every(item => item.state === 'synced')
  if (failed.length > 0) {
    const issueCount = configurationSyncFailureIssues(failed).length
    return { tone: 'danger', label: `需要处理 · ${issueCount}`, retryServerIDs: failed.map(item => item.server_id), busy: retrying }
  }
  if (saving) return { tone: 'info', label: '正在保存...', retryServerIDs: [], busy: true }
  if (retrying) return { tone: 'info', label: '正在重试同步...', retryServerIDs: [], busy: true }
  if (active.length > 0) return { tone: 'info', label: `正在同步 ${active.length} 台服务器`, retryServerIDs: [], busy: true }
  if (synced && reachable.length === rows.length) return { tone: 'ok', label: '配置已同步', retryServerIDs: [], busy: false }
  if (rows.some(item => (configurationSyncBusyStates as readonly string[]).includes(item.state))) return { tone: 'info', label: '等待服务器连接', retryServerIDs: [], busy: false }
  return { tone: 'info', label: '暂无配置同步信息', retryServerIDs: [], busy: false }
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
  const normalized = path.replace(/^\/api\/v1\/(?:ui\/)?/, '')
  const parts = normalized.split('/').filter(Boolean)
  return Boolean(mutationCollections[parts[0] || ''])
}

function mutationResource(path: string) {
  const normalized = path.replace(/^\/api\/v1\/(?:ui\/)?/, '')
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
  if (response.desired_revision != null && Number(response.desired_revision) < Number(current.desired_revision || 0)) return current
  const previous = new Map<number, ConfigurationSyncRow>((current.configuration_sync || []).map((row: ConfigurationSyncRow) => [row.server_id, row]))
  const rows = response.configuration_sync.map((row: ConfigurationSyncRow) => {
    const known = previous.get(row.server_id)
    if (!known) return row
    if (Number(row.desired_revision || 0) < Number(known.desired_revision || 0)) return known
    if (Number(row.desired_revision || 0) === Number(known.desired_revision || 0) && Number(row.config_version || 0) < Number(known.config_version || 0)) return known
    return row
  })
  return {
    ...current,
    desired_revision: response.desired_revision ?? current.desired_revision,
    configuration_sync: rows,
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
