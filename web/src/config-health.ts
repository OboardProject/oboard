// Configuration health is the console view of stored configuration that the
// current model can no longer accept. The logic here is deliberately pure so
// grouping, selection and copy can be tested without rendering.

export type ConfigHealthSeverity = 'blocking' | 'warning' | 'notice'
export type ConfigHealthScope = 'inbound' | 'proxy_path' | 'routing_rule' | 'dns_policy' | 'sync_lane'
export type ConfigHealthRemedyKind = 'none' | 'normalize' | 'disable' | 'delete' | 'resync'

export interface ConfigHealthRemedy {
  kind: ConfigHealthRemedyKind
  summary?: string
  fields?: string[]
  destructive?: boolean
}

export interface ConfigHealthFinding {
  code: string
  severity: ConfigHealthSeverity
  scope: ConfigHealthScope
  resource_id: number
  resource_name?: string
  server_id?: number
  server_name?: string
  title: string
  detail?: string
  path?: string
  remedy: ConfigHealthRemedy
}

export interface ConfigHealthSummary {
  blocking: number
  warning: number
  notice: number
  total: number
  truncated?: boolean
}

export interface ConfigHealthReport {
  summary: ConfigHealthSummary
  findings: ConfigHealthFinding[]
  blocking_by_server?: Record<string, number>
}

export interface ConfigHealthDashboard {
  fingerprint: string
  summary: ConfigHealthSummary
  blocking_by_server?: Record<string, number>
}

export const emptyConfigHealthSummary: ConfigHealthSummary = { blocking: 0, warning: 0, notice: 0, total: 0 }

export function configHealthSummaryOf(data: any): ConfigHealthSummary {
  const summary = data?.config_health?.summary
  if (!summary || typeof summary !== 'object') return emptyConfigHealthSummary
  return {
    blocking: Number(summary.blocking) || 0,
    warning: Number(summary.warning) || 0,
    notice: Number(summary.notice) || 0,
    total: Number(summary.total) || 0,
    truncated: Boolean(summary.truncated),
  }
}

// The card is hidden entirely when there is nothing to act on: an always-present
// "everything is fine" badge trains operators to ignore the spot where the real
// warning will appear.
export function shouldShowConfigHealthCard(summary: ConfigHealthSummary): boolean {
  return summary.total > 0
}

export const severityLabels: Record<ConfigHealthSeverity, string> = {
  blocking: '阻断下发',
  warning: '行为异常',
  notice: '待规范化',
}

export const scopeLabels: Record<ConfigHealthScope, string> = {
  inbound: '入口',
  proxy_path: '链路',
  routing_rule: '分流',
  dns_policy: 'DNS 策略',
  sync_lane: '节点下发',
}

export const remedyLabels: Record<ConfigHealthRemedyKind, string> = {
  none: '需手动处理',
  normalize: '清理不规范字段',
  disable: '停用',
  delete: '删除',
  resync: '重新下发',
}

export function configHealthHeadline(summary: ConfigHealthSummary): string {
  const parts: string[] = []
  if (summary.blocking > 0) parts.push(`${summary.blocking} 项会阻断下发`)
  if (summary.warning > 0) parts.push(`${summary.warning} 项行为异常`)
  if (summary.notice > 0) parts.push(`${summary.notice} 项待规范化`)
  return parts.join(' · ')
}

export interface ConfigHealthGroup {
  scope: ConfigHealthScope
  label: string
  findings: ConfigHealthFinding[]
}

const scopeOrder: ConfigHealthScope[] = ['sync_lane', 'inbound', 'proxy_path', 'routing_rule', 'dns_policy']

export function groupConfigHealthFindings(findings: ConfigHealthFinding[]): ConfigHealthGroup[] {
  const buckets = new Map<ConfigHealthScope, ConfigHealthFinding[]>()
  for (const finding of findings) {
    const list = buckets.get(finding.scope)
    if (list) list.push(finding)
    else buckets.set(finding.scope, [finding])
  }
  return scopeOrder
    .filter(scope => buckets.has(scope))
    .map(scope => ({ scope, label: scopeLabels[scope], findings: buckets.get(scope) as ConfigHealthFinding[] }))
}

export function configHealthFindingKey(finding: { code: string; scope: string; resource_id: number }): string {
  return `${finding.scope}:${finding.resource_id}:${finding.code}`
}

// Only a finding that carries an automatic remedy can be selected. A manual one
// stays visible and readable, but has no checkbox to tick.
export function isSelectableFinding(finding: ConfigHealthFinding): boolean {
  return finding.remedy.kind !== 'none'
}

export function selectableFindings(findings: ConfigHealthFinding[]): ConfigHealthFinding[] {
  return findings.filter(isSelectableFinding)
}

export function hasDestructiveSelection(findings: ConfigHealthFinding[], selected: Set<string>): boolean {
  return findings.some(finding => selected.has(configHealthFindingKey(finding)) && Boolean(finding.remedy.destructive))
}

export interface ConfigHealthCleanupAction {
  code: string
  scope: ConfigHealthScope
  resource_id: number
}

export function cleanupActionsFor(findings: ConfigHealthFinding[], selected: Set<string>): ConfigHealthCleanupAction[] {
  return findings
    .filter(finding => selected.has(configHealthFindingKey(finding)) && isSelectableFinding(finding))
    .map(finding => ({ code: finding.code, scope: finding.scope, resource_id: finding.resource_id }))
}

export interface ConfigHealthCleanupResult {
  code: string
  scope: string
  resource_id: number
  resource_name?: string
  status: 'applied' | 'skipped' | 'failed'
  reason?: string
  removed_fields?: string[]
}

export interface ConfigHealthCleanupResponse {
  fingerprint: string
  dry_run: boolean
  applied: number
  skipped: number
  failed: number
  requires_deployment: boolean
  results: ConfigHealthCleanupResult[]
}

export function cleanupOutcomeMessage(response: ConfigHealthCleanupResponse): string {
  if (response.dry_run) return `预览完成，共 ${response.results.length} 项`
  const parts = [`已处理 ${response.applied} 项`]
  if (response.skipped > 0) parts.push(`跳过 ${response.skipped} 项`)
  if (response.failed > 0) parts.push(`失败 ${response.failed} 项`)
  if (response.applied > 0 && response.requires_deployment) parts.push('需要重新下发才会生效')
  return parts.join('，')
}

export function cleanupOutcomeTone(response: ConfigHealthCleanupResponse): 'success' | 'warning' | 'danger' {
  if (response.failed > 0) return 'danger'
  if (response.skipped > 0) return 'warning'
  return 'success'
}

// blockingDeploymentNotice is the warning a fleet-wide push shows before it
// runs. It informs and never blocks: the operator asked for the push, and a
// blocking finding on one node is not a reason to refuse the other nodes.
export function blockingDeploymentNotice(report: ConfigHealthReport | null): string {
  const findings = (report?.findings || []).filter(finding => finding.severity === 'blocking')
  if (findings.length === 0) return ''
  const servers = new Set(findings.map(finding => finding.server_name || finding.server_id).filter(Boolean))
  const where = servers.size > 0 ? `涉及 ${servers.size} 台服务器` : ''
  return `注意：当前有 ${findings.length} 项配置会导致下发失败${where ? `（${where}）` : ''}，这些服务器仍会下发失败。可先到仪表盘「配置体检」清理后再刷新。`
}
