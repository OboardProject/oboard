import type { EnvField, FieldIssue, Risk } from './types'

export const instanceStatusLabels: Record<string, string> = {
  ready: '就绪',
  degraded: '需关注',
  auto_paused: '已自动暂停',
  disabled: '已停用',
  plugin_disabled: '插件已停用',
  permission_review_required: '待审核权限',
  not_published: '未发布',
  configuration_required: '待配置',
  configuration_invalid: '配置无效',
}

export const instanceStatusTone = (status: string): 'success' | 'warning' | 'destructive' | 'secondary' => {
  if (status === 'ready') return 'success'
  if (status === 'configuration_invalid' || status === 'auto_paused') return 'destructive'
  if (status === 'disabled' || status === 'plugin_disabled' || status === 'not_published') return 'secondary'
  return 'warning'
}

export const runStatusLabels: Record<string, string> = {
  queued: '排队中',
  running: '运行中',
  succeeded: '成功',
  failed: '失败',
  timeout: '超时',
  cancelled: '已取消',
  permission_denied: '权限不足',
  resource_limit: '超出资源限制',
}

export const runTerminal = (status: string) => status !== 'queued' && status !== 'running'

export const runTone = (status: string): 'success' | 'warning' | 'destructive' | 'secondary' => {
  if (status === 'succeeded') return 'success'
  if (status === 'queued' || status === 'running') return 'warning'
  if (status === 'cancelled') return 'secondary'
  return 'destructive'
}

export const triggerLabels: Record<string, string> = {
  manual: '手动', interval: '间隔', cron: 'Cron', event: '事件',
}

export const eventLabels: Record<string, string> = {
  'server.online': '服务器上线',
  'server.offline': '服务器离线',
}

export const riskLabels: Record<Risk, string> = { low: '低风险', medium: '中风险', high: '高风险' }

export const groupLabels: Record<string, string> = {
  servers: '服务器', network: '网络诊断', http: '外部 HTTP', state: '插件状态',
  secrets: '密钥', notifications: '通知', events: '事件',
}

export const envTypeLabels: Record<string, string> = {
  string: '单行文本', text: '多行文本', integer: '整数', number: '数值', boolean: '开关',
  select: '单选', multi_select: '多选', server: '服务器', servers: '服务器列表', secret: '密钥',
  url: 'URL', duration: '时长', json: 'JSON',
}

export const errorCodeLabels: Record<string, string> = {
  CAPABILITY_DENIED: '插件未获得该能力授权',
  RESOURCE_DENIED: '目标资源不在授权范围内',
  INVALID_ENVIRONMENT: '环境变量无效',
  CONFIGURATION_REQUIRED: '实例尚未完成配置',
  PERMISSION_REVIEW_REQUIRED: '新版本权限需要管理员审核',
  SERVER_NOT_FOUND: '服务器不存在',
  SERVER_OFFLINE: '目标服务器离线',
  SERVER_BUSY: '服务器正在执行其他任务',
  AGENT_POLICY_DENIED: '节点本地策略未允许插件诊断',
  UNSUPPORTED_CAPABILITY: '节点不支持该诊断',
  TARGET_NOT_ALLOWED: '目标地址不被允许',
  HTTP_HOST_DENIED: 'HTTP 主机未授权',
  HTTP_PRIVATE_ADDRESS_DENIED: '禁止访问内网或保留地址',
  HTTP_TIMEOUT: 'HTTP 请求超时',
  HTTP_FAILED: 'HTTP 请求失败',
  HTTP_RESPONSE_TOO_LARGE: 'HTTP 响应过大',
  OPERATION_TIMEOUT: '节点操作超时',
  OPERATION_FAILED: '操作失败',
  RUN_TIMEOUT: '执行超时',
  RATE_LIMITED: '调用过于频繁',
  LIMIT_EXCEEDED: '超出单次执行配额',
  STATE_QUOTA_EXCEEDED: '插件状态存储已满',
  STATE_CONFLICT: '状态版本冲突',
  SECRET_NOT_CONFIGURED: '密钥尚未配置',
  PLUGIN_DISABLED: '插件执行已关闭',
  RUNTIME_UNAVAILABLE: '运行环境不可用',
  INVALID_ARGUMENT: '参数无效',
  INVALID_MANIFEST: '清单无效',
  INVALID_PACKAGE: '插件包无效',
  SIGNATURE_INVALID: '发布者签名无效',
  PUBLISHER_CHANGED: '发布者与已安装版本不一致',
  RUN_IN_PROGRESS: '该实例已有执行在进行',
  CANCELLED: '已取消',
  SCRIPT_ERROR: '脚本错误',
  RESOURCE_LIMIT: '超出资源限制',
  PERMISSION_DENIED: '没有权限',
  NOT_FOUND: '对象不存在',
  CONFLICT: '数据已被修改，请刷新后重试',
}

export const describeError = (code?: string, message?: string) => {
  if (!code) return message || ''
  const label = errorCodeLabels[code]
  if (label && message && message !== label) return `${label}：${message}`
  return label || message || code
}

export function newIdempotencyKey(prefix: string) {
  const random = typeof crypto !== 'undefined' && 'randomUUID' in crypto ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(16).slice(2)}`
  return `${prefix}-${random}`
}

export function formatTime(value?: string) {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { hour12: false })
}

export function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`
}

export function shortHash(value?: string) {
  return value ? value.slice(0, 12) : ''
}

export function publisherLabel(identity?: string, name?: string) {
  if (!identity || identity === 'local') return '本地（未签名）'
  return name ? `${name} · ${identity.slice(0, 22)}…` : `${identity.slice(0, 22)}…`
}

// fieldActive mirrors the Controller rule: a field with depends_on is active
// only while the referenced field currently equals the declared value.
export function fieldActive(field: EnvField, values: Record<string, unknown>, fields: EnvField[]): boolean {
  if (!field.depends_on) return true
  const parent = fields.find(item => item.name === field.depends_on!.field)
  if (!parent || !fieldActive(parent, values, fields)) return false
  const current = values[parent.name] !== undefined ? values[parent.name] : parent.default
  return JSON.stringify(current) === JSON.stringify(field.depends_on.equals)
}

export function issuesByField(issues: FieldIssue[] | undefined) {
  const out: Record<string, string> = {}
  for (const issue of issues || []) {
    const key = issue.field.replace(/^environment\./, '').replace(/^values\./, '')
    if (!out[key]) out[key] = issue.message
  }
  return out
}

export function errorIssues(error: unknown): FieldIssue[] {
  const details = (error as { details?: { issues?: FieldIssue[]; field?: string } } | null)?.details
  if (details?.issues?.length) return details.issues
  if (details?.field) return [{ field: details.field, code: '', message: (error as Error).message }]
  return []
}

export function errorMessage(error: unknown, fallback: string) {
  const code = (error as { code?: string } | null)?.code
  const message = error instanceof Error ? error.message : ''
  return describeError(code, message) || fallback
}

export function readFileAsBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error('读取文件失败'))
    reader.onload = () => {
      const result = String(reader.result || '')
      resolve(result.includes(',') ? result.slice(result.indexOf(',') + 1) : result)
    }
    reader.readAsDataURL(file)
  })
}

export const defaultDraftManifest = {
  id: 'local.my-plugin',
  name: '我的插件',
  version: '0.1.0',
  description: '',
  runtime: 'oboard-js',
  entry: 'main.js',
  capabilities: ['servers.read'],
  resources: { servers: { min: 1, max: 1, reason: '选择要检查的服务器' } },
  environment: [
    { name: 'TARGET_SERVER', type: 'server', label: '目标服务器', required: true },
  ],
  triggers: { schedule: true },
  limits: { timeout: '30s' },
}

export const defaultDraftSource = `async function main(run) {
  const server = await oboard.servers.get(env.TARGET_SERVER)
  log.info('checked', server.name)
  return { name: server.name, status: server.status }
}
`
