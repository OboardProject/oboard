import { ArrowUpRight, CircleAlert, ShieldCheck } from 'lucide-react'

import './UserDashboardPage.css'

import { DashboardSkeleton } from '../components/ui/skeleton'

export type UserDashboardOverview = {
  assigned_node_count: number
  account_status: 'normal' | 'attention'
  status_reasons: string[]
  has_active_plan: boolean
  traffic: {
    used_bytes: number
    limit_bytes: number
    quota_state: string
    period_end?: string
  }
  audit: {
    enabled: boolean
    risk: boolean
  }
}

export type UserDashboardAnnouncement = {
  id: number
  actor_name: string
  title: string
  body: string
  created_at: string
}

const statusReasonLabels: Record<string, string> = {
  account_inactive: '账号已停用',
  no_active_plan: '未开通有效套餐',
  subscription_suspended: '订阅已暂停',
  quota_exceeded: '流量已用尽',
  audit_risk: '账号使用情况需要核实',
}

function formatBytes(value: number) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let amount = Math.max(0, Number(value) || 0)
  let unit = 0
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024
    unit += 1
  }
  return `${amount >= 10 || unit === 0 ? amount.toFixed(0) : amount.toFixed(1)} ${units[unit]}`
}

function auditLabel(overview: UserDashboardOverview) {
  if (overview.audit.risk) return '账号使用情况需要核实，请联系管理员'
  return overview.audit.enabled ? '账号使用情况未见异常' : '审计未启用'
}

function formatAnnouncementTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const pad = (part: number) => String(part).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

export function UserDashboardPage({
  overview,
  announcements = [],
  loading = false,
  onNavigateSubscriptions,
}: {
  overview?: UserDashboardOverview
  announcements?: UserDashboardAnnouncement[]
  displayName: string
  loading?: boolean
  onNavigateSubscriptions?: () => void
}) {
  if (!overview) {
    return <div className="signal-overview" aria-busy={loading}>{loading ? <DashboardSkeleton /> : <p role="status" className="signal-overview-empty">账户概览暂不可用，请刷新后重试。</p>}</div>
  }

  const assignedCount = Number.isFinite(overview.assigned_node_count) ? Math.max(0, overview.assigned_node_count) : null
  const usedBytes = Number.isFinite(overview.traffic.used_bytes) ? Math.max(0, overview.traffic.used_bytes) : null
  const limitBytes = Number.isFinite(overview.traffic.limit_bytes) ? Math.max(0, overview.traffic.limit_bytes) : null
  const usagePercent = limitBytes !== null && limitBytes > 0 && usedBytes !== null ? Math.min(100, (usedBytes / limitBytes) * 100) : null
  const totalLabel = !overview.has_active_plan ? '未开通' : limitBytes === null ? '暂不可用' : limitBytes > 0 ? formatBytes(limitBytes) : '不限量'
  const statusOK = overview.account_status === 'normal'
  const reasons = [...new Set(overview.status_reasons.map(reason => statusReasonLabels[reason] || '账号状态需要核实'))]
  const periodEnd = overview.traffic.period_end && !Number.isNaN(new Date(overview.traffic.period_end).getTime()) ? overview.traffic.period_end : undefined

  return (
    <div className="signal-overview" aria-busy={loading}>
      <section className="signal-overview-usage" aria-label="账户使用概览">
        <div className="signal-overview-traffic">
          <header><h2>本周期流量</h2><span className="signal-overview-plan">{overview.has_active_plan ? '套餐有效' : '未开通套餐'}</span></header>
          <dl className="signal-overview-meter">
            <div><dt>已用流量</dt><dd>{usedBytes === null ? '暂不可用' : formatBytes(usedBytes)}</dd></div>
            <div><dt>总量</dt><dd>{totalLabel}</dd></div>
          </dl>
          {overview.has_active_plan && usagePercent !== null ? (
            <div className="signal-overview-progress" role="progressbar" aria-label="本周期流量使用率" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(usagePercent)} aria-valuetext={`已使用 ${Math.round(usagePercent)}%`}>
              <span data-tone={usagePercent >= 100 ? 'danger' : usagePercent >= 80 ? 'warning' : undefined} style={{ width: `${usagePercent}%` }} />
            </div>
          ) : null}
          <div className="signal-overview-period"><span>周期结束</span>{periodEnd ? <time dateTime={periodEnd}>{formatAnnouncementTime(periodEnd)}</time> : <span>暂未提供</span>}</div>
        </div>
        <div className="signal-overview-access">
          <div className="signal-overview-nodes"><span>已分配节点</span><strong>{assignedCount ?? '暂不可用'}</strong></div>
          <button className="btn primary" type="button" onClick={onNavigateSubscriptions} disabled={!onNavigateSubscriptions}>查看订阅<ArrowUpRight size={16} aria-hidden="true" /></button>
          <div className="signal-overview-status" data-attention={!statusOK}>
            <div><span>账号状态</span><strong>{statusOK ? <ShieldCheck size={16} aria-hidden="true" /> : <CircleAlert size={16} aria-hidden="true" />}{statusOK ? '正常' : '需要关注'}</strong></div>
            <p>{auditLabel(overview)}</p>
            {reasons.length > 0 ? <p>{reasons.join(' · ')}</p> : null}
          </div>
        </div>
      </section>

      <section className="signal-overview-announcements" aria-labelledby="user-announcement-title">
        <header className="signal-overview-announcement-head">
          <h2 id="user-announcement-title">公告</h2>
          <span>{announcements.length > 0 ? `${announcements.length} 条` : '暂无'}</span>
        </header>
        {announcements.length > 0 ? (
          <ol className="signal-overview-announcement-list">
            {announcements.map(announcement => (
              <li key={announcement.id}>
                <article className="signal-overview-announcement-item">
                  <header>
                    <div><h3>{announcement.title}</h3><span>来自 {announcement.actor_name}</span></div>
                    <time dateTime={announcement.created_at}>{formatAnnouncementTime(announcement.created_at)}</time>
                  </header>
                  <p>{announcement.body}</p>
                </article>
              </li>
            ))}
          </ol>
        ) : (
          <div className="signal-overview-announcement-empty"><strong>暂无公告</strong><span>当前没有需要查看的消息。</span></div>
        )}
      </section>
    </div>
  )
}
