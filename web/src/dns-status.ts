// Server DNS policy status derivation shared by the DNS page overview, the
// attention list, and the server policy manager. Configuration (which lists a
// server uses) and run state (what the last benchmark produced) are different
// kinds of information, so the page classifies every policy into one status and
// renders run state only where it is being diagnosed.

export type DNSPolicyStatus = 'ok' | 'failed' | 'stale' | 'untested'

export type DNSStatusList = {
  id: number
  kind?: string
  revision: number
}

export type DNSStatusPolicy = {
  server_id: number
  encrypted_list_id: number
  bootstrap_list_id: number
  encrypted_selection_revision: number
  bootstrap_selection_revision: number
  last_success_at?: string
  last_error?: string
  needs_benchmark?: boolean
}

// Controller stores one fixed marker whenever a bound resolver group produced no
// usable candidate, and matches that exact string to fall the server back to
// local DNS. It is a wire sentinel, not operator copy: its wording names both
// groups even for a server that binds no encrypted list, which reads as if the
// missing encrypted list were the failure. The panel always renders it through
// dnsPolicyErrorText so the operator sees what actually has to be fixed.
export const dnsNoUsableCandidatesError = 'both encrypted and bootstrap dns groups require at least one usable candidate'

export function dnsPolicyErrorText(error: string | undefined, policy?: Pick<DNSStatusPolicy, 'encrypted_list_id'>) {
  const raw = String(error || '').trim()
  if (raw !== dnsNoUsableCandidatesError) return raw
  return policy && !policy.encrypted_list_id
    ? '基础解析列表里没有可用的解析服务，服务器暂时改用系统解析'
    : '加密解析和基础解析都需要至少一个可用的解析服务，服务器暂时改用系统解析'
}

export const dnsPolicyStatusLabels: Record<DNSPolicyStatus, string> = {
  ok: '正常',
  failed: '测试失败',
  stale: '等待重新测试',
  untested: '等待首次测试',
}

export function dnsPolicyStatusLabel(status: DNSPolicyStatus) {
  return dnsPolicyStatusLabels[status]
}

export function dnsPolicyStatusTone(status: DNSPolicyStatus) {
  if (status === 'ok') return 'ok'
  if (status === 'failed') return 'danger'
  return 'warning'
}

// A selection is stale when Controller already knows the recorded winners no
// longer match the bound lists: either the policy carries the server-side
// needs_benchmark flag, or a list has been edited since the selection snapshot.
export function isDNSSelectionStale(policy: DNSStatusPolicy | undefined, lists: readonly DNSStatusList[]) {
  if (!policy) return false
  if (policy.needs_benchmark) return true
  const encrypted = lists.find(list => list.id === policy.encrypted_list_id)
  const bootstrap = lists.find(list => list.id === policy.bootstrap_list_id)
  if (encrypted && encrypted.revision !== policy.encrypted_selection_revision) return true
  if (bootstrap && bootstrap.revision !== policy.bootstrap_selection_revision) return true
  return false
}

export function dnsPolicyStatus(policy: DNSStatusPolicy | undefined, lists: readonly DNSStatusList[]): DNSPolicyStatus {
  if (!policy) return 'untested'
  if (String(policy.last_error || '').trim()) return 'failed'
  if (!String(policy.last_success_at || '').trim()) return 'untested'
  return isDNSSelectionStale(policy, lists) ? 'stale' : 'ok'
}

export function dnsPolicyNeedsAttention(status: DNSPolicyStatus) {
  return status !== 'ok'
}

export type DNSPolicySummary = {
  total: number
  ok: number
  pending: number
  failed: number
}

export function summarizeDNSPolicyStatuses(statuses: readonly DNSPolicyStatus[]): DNSPolicySummary {
  return {
    total: statuses.length,
    ok: statuses.filter(status => status === 'ok').length,
    pending: statuses.filter(status => status === 'stale' || status === 'untested').length,
    failed: statuses.filter(status => status === 'failed').length,
  }
}

// Attention first, then by name, so the page never asks the operator to scan a
// long alphabetical list to find the one server that needs work.
const statusOrder: Record<DNSPolicyStatus, number> = { failed: 0, stale: 1, untested: 2, ok: 3 }

export function compareDNSPolicyStatus(a: DNSPolicyStatus, b: DNSPolicyStatus) {
  return statusOrder[a] - statusOrder[b]
}

// 'attention' is everything that is not converged; 'pending' is the narrower
// waiting-for-a-test bucket the overview counts separately from failures.
export type DNSPolicyStatusFilter = 'all' | 'attention' | 'pending' | DNSPolicyStatus

export function matchesDNSPolicyStatusFilter(status: DNSPolicyStatus, filter: DNSPolicyStatusFilter) {
  if (filter === 'all') return true
  if (filter === 'attention') return dnsPolicyNeedsAttention(status)
  if (filter === 'pending') return status === 'stale' || status === 'untested'
  return status === filter
}

export type DNSPolicyFilterRow = {
  serverName: string
  status: DNSPolicyStatus
  encryptedListID: number
  bootstrapListID: number
}

export type DNSPolicyFilter = {
  query?: string
  status?: DNSPolicyStatusFilter
  encryptedListID?: number
  bootstrapListID?: number
}

export function filterDNSPolicyRows<T extends DNSPolicyFilterRow>(rows: readonly T[], filter: DNSPolicyFilter) {
  const query = String(filter.query || '').trim().toLowerCase()
  const status = filter.status || 'all'
  return rows.filter(row => {
    if (query && !row.serverName.toLowerCase().includes(query)) return false
    if (!matchesDNSPolicyStatusFilter(row.status, status)) return false
    if (filter.encryptedListID && row.encryptedListID !== filter.encryptedListID) return false
    if (filter.bootstrapListID && row.bootstrapListID !== filter.bootstrapListID) return false
    return true
  })
}

// Coarse relative time for status lines. Diagnostics keep the absolute stamp.
export function dnsRelativeTime(value: string | undefined, now: Date = new Date()) {
  const raw = String(value || '').trim()
  if (!raw) return ''
  const then = new Date(raw)
  if (Number.isNaN(then.getTime())) return ''
  const seconds = Math.floor(Math.max(0, now.getTime() - then.getTime()) / 1000)
  if (seconds < 60) return '刚刚'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} 分钟前`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} 小时前`
  return `${Math.floor(hours / 24)} 天前`
}

export function latestDNSSuccessAt(policies: readonly DNSStatusPolicy[]) {
  let latest = ''
  let latestTime = Number.NEGATIVE_INFINITY
  for (const policy of policies) {
    const raw = String(policy.last_success_at || '').trim()
    if (!raw) continue
    const time = new Date(raw).getTime()
    if (Number.isNaN(time) || time <= latestTime) continue
    latestTime = time
    latest = raw
  }
  return latest
}
