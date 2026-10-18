import { formatBytes } from '../../../shared/presentation'
import type { ServerExpiryTone } from '../../../server-expiry'
import { getServerTimeIssue } from '../../../server-time'
import type { Server } from '../../proxy-path/types'

export type Tone = '' | 'ok' | 'warning' | 'danger' | 'muted'

export function isServerOnline(server: Pick<Server, 'status'>) {
  return String(server.status || '').toLowerCase() === 'online'
}

export function isServerEnrolled(server: Pick<Server, 'agent_id'>) {
  return Boolean(String(server.agent_id || '').trim())
}

export function serverDisplayName(server: Pick<Server, 'id' | 'name'>) {
  return server.name || `server-${server.id}`
}

export function resourcePercent(used: number, total: number) {
  if (!total || !Number.isFinite(used) || !Number.isFinite(total)) return 0
  return Math.min(100, Math.max(0, (used / total) * 100))
}

export function usageTone(percent: number): Tone {
  if (percent >= 90) return 'danger'
  if (percent >= 75) return 'warning'
  return ''
}

export function formatPercent(value: number) {
  if (!Number.isFinite(value)) return '—'
  return `${value >= 10 || value === 0 ? Math.round(value) : value.toFixed(1)}%`
}

export function formatByteRate(value: number) {
  return `${formatBytes(Math.max(0, Number(value) || 0))}/s`
}

export function cpuModelLabel(server: Pick<Server, 'cpu'>) {
  return String(server.cpu || '').trim()
}

export function cpuCoresLabel(server: Pick<Server, 'cpu_cores'>) {
  const cores = Math.trunc(Number(server.cpu_cores))
  if (!Number.isFinite(cores) || cores <= 0) return '—'
  return `${cores} 核`
}

export function enrolledDaysLabel(server: Pick<Server, 'created_at'>, now = new Date()) {
  const raw = String(server.created_at || '').trim()
  if (!raw) return ''
  const created = new Date(raw)
  if (Number.isNaN(created.getTime())) return ''
  const days = Math.max(0, Math.floor((now.getTime() - created.getTime()) / 86400000))
  return `${days} 天`
}

export function cardExpiryStatus(server: Pick<Server, 'expires_at' | 'auto_renew_enabled'>, now = new Date()): { label: string; tone: ServerExpiryTone; days: number | null } {
  const raw = String(server.expires_at || '').trim()
  if (!raw) return { label: '未设置', tone: 'muted', days: null }
  const date = new Date(raw)
  if (Number.isNaN(date.getTime())) return { label: '未设置', tone: 'muted', days: null }
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const expiry = new Date(date.getFullYear(), date.getMonth(), date.getDate())
  const days = Math.round((expiry.getTime() - today.getTime()) / 86400000)
  if (days < 0) return { label: server.auto_renew_enabled ? '续期中' : '已到期', tone: server.auto_renew_enabled ? 'warning' : 'danger', days }
  if (days === 0) return { label: '今天', tone: 'danger', days }
  if (days < 7) return { label: `${days} 天`, tone: 'danger', days }
  if (days < 15) return { label: `${days} 天`, tone: 'warning', days }
  return { label: `${days} 天`, tone: 'ok', days }
}

export function offlineAgoLabel(lastSeen?: string, now = new Date()) {
  const raw = String(lastSeen || '').trim()
  if (!raw) return '离线'
  const then = new Date(raw)
  if (Number.isNaN(then.getTime())) return '离线'
  const minutes = Math.floor(Math.max(0, now.getTime() - then.getTime()) / 60000)
  if (minutes < 60) return `${Math.max(1, minutes)} 分钟前离线`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `${hours} 小时前离线`
  return `${Math.floor(hours / 24)} 天前离线`
}

export function serverDeliveryStatus(server: Server) {
  if (!server.agent_id) return null
  const usersFallback = server.users_fallback === 'apply_core_config'
    || server.users_pending_reason === 'agent_upgrade_required'
    || server.users_pending_reason === 'core_config_fallback'
  const authPending = server.authorization_confirmed === false
  const usersPending = server.users_confirmed === false && !usersFallback
  if (!authPending && !usersPending) return null
  const title = [authPending ? '授权租约未确认' : '', usersPending ? '运行时用户未确认' : ''].filter(Boolean).join('；')
  return { tone: 'danger' as const, label: '授权待同步', title }
}

export function buildNeedsUpdate(current: string, target: string): boolean {
  const c = String(current || '').trim()
  const t = String(target || '').trim()
  if (!c || !t) return !c && !!t
  if (c.length === t.length && /^[0-9]+$/.test(c) && /^[0-9]+$/.test(t)) return c < t
  return c !== t
}

export function compactBuildLabel(value?: string) {
  const build = String(value || '').trim()
  if (/^\d{14}$/.test(build)) return `${build.slice(4, 8)}.${build.slice(8, 12)}`
  if (build.length > 10) return build.slice(-10)
  return build || '—'
}

export function agentOutdated(server: Pick<Server, 'agent_build'>, expectedBuild?: string) {
  return Boolean(expectedBuild && server.agent_build && expectedBuild !== server.agent_build)
}

export function serverTraffic(server: Pick<Server, 'traffic_upload_bytes' | 'traffic_download_bytes' | 'traffic_limit_bytes'>) {
  const used = (Number(server.traffic_upload_bytes) || 0) + (Number(server.traffic_download_bytes) || 0)
  const limit = Number(server.traffic_limit_bytes || 0)
  const percent = limit > 0 ? Math.min(100, (used / limit) * 100) : 0
  return { used, limit, percent, tone: limit > 0 ? usageTone(percent) : '' as Tone }
}

export type ServerAttention = { key: string; label: string; tone: 'warning' | 'danger'; action?: string; detail?: string }

// Everything an operator should notice on a server, in priority order. The
// card, list row and fleet summary all read from this one list.
export function serverAttention(server: Server, expectedBuild?: string, now = new Date()): ServerAttention[] {
  const items: ServerAttention[] = []
  const enrolled = isServerEnrolled(server)
  if (enrolled && !isServerOnline(server)) items.push({ key: 'offline', label: offlineAgoLabel(server.last_seen_at, now), tone: 'danger' })
  if (!enrolled) items.push({ key: 'unenrolled', label: '未接入', tone: 'warning', action: 'enroll', detail: '安装 Agent 后即可上线' })
  const delivery = serverDeliveryStatus(server)
  if (delivery) items.push({ key: 'delivery', label: delivery.label, tone: 'danger', detail: delivery.title })
  const time = getServerTimeIssue(server)
  if (time) items.push({ key: 'time', label: time.summary, tone: time.tone === 'danger' ? 'danger' : 'warning', action: 'time-details' })
  if (enrolled && agentOutdated(server, expectedBuild)) items.push({ key: 'agent', label: 'Agent 待更新', tone: 'warning', action: 'update-agent', detail: `${compactBuildLabel(server.agent_build)} → ${compactBuildLabel(expectedBuild)}` })
  const expiry = cardExpiryStatus(server, now)
  if (expiry.tone === 'danger' || expiry.tone === 'warning') items.push({ key: 'expiry', label: expiry.days != null && expiry.days >= 0 ? `${expiry.label}后到期` : expiry.label, tone: expiry.tone })
  const traffic = serverTraffic(server)
  if (traffic.tone) items.push({ key: 'traffic', label: `流量已用 ${Math.round(traffic.percent)}%`, tone: traffic.tone as 'warning' | 'danger' })
  return items
}

export function sumBy<T>(items: T[], pick: (item: T) => number) {
  return items.reduce((total, item) => total + (Number(pick(item)) || 0), 0)
}
