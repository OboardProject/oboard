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
  'update_agent', 'update_agent_config', 'diagnose_network', 'list_network_interfaces', 'detect_mtu',
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
        title: labelTaskType(type),
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

export function taskStatusSummary(tasks: any[]) {
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
  serverTaskBuckets(tasks).forEach(serverTasks => {
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
