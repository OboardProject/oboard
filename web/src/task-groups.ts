export type TaskGroupKind = 'deployment' | 'batch' | 'single'

export type TaskGroup = {
  kind: TaskGroupKind
  id: string | number
  title: string
  subtitle?: string
  version?: number
  batchType?: string
  tasks: any[]
  updated_at: string
}

const BATCHABLE_TASK_TYPES = new Set([
  'remote_exec', 'remote_operation', 'update_agent', 'update_agent_config', 'diagnose_network', 'list_network_interfaces', 'detect_mtu',
  'probe_inbounds', 'probe_inbounds_external', 'probe_port_forwards', 'probe_external_egress', 'collect_logs', 'manage_logs', 'check_time',
])

export function groupTasksForTimeline(rows: any[], labelTaskType: (type: string) => string): TaskGroup[] {
  const byVersion = new Map<number, any[]>()
  const leftover: any[] = []

  ;(rows || []).forEach(task => {
    const version = Number(task.config_version || 0)
    if (version > 0) byVersion.set(version, [...(byVersion.get(version) || []), task])
    else leftover.push(task)
  })

  const groups: TaskGroup[] = []

  byVersion.forEach((tasks, version) => {
    if (isDeploymentBundle(tasks)) {
      groups.push({
        kind: 'deployment',
        id: `deploy-${version}`,
        title: '下发配置',
        subtitle: `版本 ${version}`,
        version,
        tasks,
        updated_at: maxTaskTime(tasks),
      })
      return
    }
    leftover.push(...tasks)
  })

  const batches = new Map<string, any[]>()
  leftover.forEach(task => {
    const type = String(task.type || 'task')
    const key = BATCHABLE_TASK_TYPES.has(type)
      ? `${type}:${taskBatchBucket(task)}`
      : `single:${task.id}`
    batches.set(key, [...(batches.get(key) || []), task])
  })

  batches.forEach((tasks, key) => {
    const type = String(tasks[0]?.type || 'task')
    if (key.startsWith('single:')) {
      groups.push({
        kind: 'single',
        id: key,
        title: type === 'remote_exec' ? '远程命令' : type === 'remote_operation' ? '远程操作' : labelTaskType(type),
        tasks,
        updated_at: maxTaskTime(tasks),
      })
      return
    }
    const serverCount = new Set(tasks.map(t => t.server_id)).size
    groups.push({
      kind: tasks.length > 1 || serverCount > 1 ? 'batch' : 'single',
      id: `batch-${key}`,
      title: batchTitleForType(type, labelTaskType),
      batchType: type,
      tasks,
      updated_at: maxTaskTime(tasks),
    })
  })

  return groups.sort((a, b) => String(b.updated_at || '').localeCompare(String(a.updated_at || '')) || String(b.id).localeCompare(String(a.id)))
}

export function maxTaskTime(tasks: any[]) {
  return tasks.map(t => String(t.updated_at || t.created_at || '')).sort().pop() || ''
}

export function deploymentStatusFromSummary(summary: { total: number; pending: number; running: number; succeeded: number; failed: number }) {
  if (summary.total === 0) return 'pending'
  if (summary.failed > 0) return summary.failed >= summary.total ? 'failed' : 'partial_failed'
  if (summary.running) return 'running'
  if (summary.pending) return 'pending'
  return 'succeeded'
}

export function splitTaskAttempts(tasks: any[]) {
  const latest = new Map<string, any>()
  const sorted = [...tasks].sort((a, b) => Number(b.id || 0) - Number(a.id || 0))
  const current: any[] = []
  const history: any[] = []
  sorted.forEach(task => {
    const key = Number(task.server_id) > 0 && Number(task.config_version) > 0 && !String(task.type).startsWith('remote_')
      ? `${task.server_id}:${task.config_version || 0}:${task.type}`
      : `task:${task.id}`
    if (!latest.has(key)) {
      latest.set(key, task)
      current.push(task)
    } else if (['pending', 'running'].includes(task.status)) current.push(task)
    else history.push(task)
  })
  return { current, history }
}

export type TaskCategory = 'deployment' | 'maintenance' | 'diagnostics' | 'remote' | 'other'

export const taskCategories: { id: TaskCategory; label: string }[] = [
  { id: 'deployment', label: '配置部署' },
  { id: 'maintenance', label: '维护更新' },
  { id: 'diagnostics', label: '网络检测' },
  { id: 'remote', label: '远程操作' },
  { id: 'other', label: '其他任务' },
]

export function taskCategory(task: any): TaskCategory {
  const type = String(task.type || '')
  if (type.startsWith('remote_') || type === 'exec_shell') return 'remote'
  if (Number(task.config_version) > 0 || ['apply_deployment', 'apply_core_config', 'apply_traffic_policy'].includes(type)) return 'deployment'
  if (/^(probe_|detect_|diagnose_|list_network|benchmark_dns$)/.test(type)) return 'diagnostics'
  if (/^(update_|uninstall_|collect_logs$|manage_logs$|check_time$|host_power)/.test(type)) return 'maintenance'
  return 'other'
}

export function latestDeploymentTasks(tasks: any[]) {
  const versions = new Map<number, number>()
  const deployments = tasks.filter(task => taskCategory(task) === 'deployment')
  deployments.forEach(task => {
    const server = Number(task.server_id || 0)
    versions.set(server, Math.max(versions.get(server) || 0, Number(task.config_version || 0)))
  })
  return splitTaskAttempts(deployments.filter(task => Number(task.config_version || 0) === versions.get(Number(task.server_id || 0)))).current
}

export function taskStatusSummary(tasks: any[]) {
  tasks = splitTaskAttempts(tasks).current
  const out = { total: tasks.length, pending: 0, running: 0, succeeded: 0, failed: 0 }
  tasks.forEach(task => {
    const result = parseJSONLoose(task.result_json)
    const status = result?.timeout ? 'timeout' : String(task.status || '')
    if (status === 'pending') out.pending++
    else if (status === 'running') out.running++
    else if (status === 'succeeded') out.succeeded++
    else if (status.includes('fail') || status === 'timeout') out.failed++
  })
  return out
}

export function serverTaskStatusSummary(tasks: any[]) {
  const out = { total: 0, pending: 0, running: 0, succeeded: 0, failed: 0, skipped: 0 }
  serverTaskBuckets(tasks).forEach(attempts => {
    const serverTasks = splitTaskAttempts(attempts).current
    out.total++
    if (serverTasks.every(task => parseJSONLoose(task.result_json)?.skipped || parseJSONLoose(task.payload_json)?.skipped)) {
      out.skipped++
      return
    }
    const status = deploymentStatusFromSummary(taskStatusSummary(serverTasks))
    if (status === 'failed' || status === 'partial_failed') out.failed++
    else if (status === 'running') out.running++
    else if (status === 'pending') out.pending++
    else out.succeeded++
  })
  return out
}

function isDeploymentBundle(tasks: any[]) {
  const types = new Set(tasks.map(t => String(t.type || '')))
  return types.has('apply_deployment')
}

function taskBatchBucket(task: any) {
  const raw = String(task.created_at || task.updated_at || '')
  const ms = Date.parse(raw)
  if (!Number.isFinite(ms)) return raw || 'unknown'
  return String(Math.floor(ms / (2 * 60 * 1000)))
}

function batchTitleForType(type: string, labelTaskType: (type: string) => string) {
  switch (type) {
    case 'remote_exec': return '远程命令'
    case 'remote_operation': return '远程操作'
    case 'update_agent': return '更新 Agent'
    case 'update_agent_config': return '同步 Agent 配置'
    case 'detect_mtu': return 'MTU 检测'
    case 'check_time': return '时间检测'
    case 'diagnose_network': return '网络诊断'
    case 'list_network_interfaces': return '读取网卡'
    case 'probe_inbounds': return '入口监听探测'
    case 'probe_inbounds_external': return '公网端口探测'
    case 'probe_port_forwards': return '端口转发探测'
    case 'probe_external_egress': return '第三方出口探测'
    case 'collect_logs': return '拉取日志'
    case 'manage_logs': return '管理日志'
    default: return labelTaskType(type || 'task')
  }
}

function serverTaskBuckets(tasks: any[]) {
  const buckets = new Map<string, any[]>()
  tasks.forEach((task, index) => {
    const serverID = Number(task.server_id || 0)
    const key = serverID > 0 ? `server-${serverID}` : `task-${task.id || index}`
    buckets.set(key, [...(buckets.get(key) || []), task])
  })
  return Array.from(buckets.values())
}

function parseJSONLoose(raw: any) {
  if (!raw) return null
  if (typeof raw === 'object') return raw
  try { return JSON.parse(String(raw)) } catch { return String(raw) }
}
