import { Activity, CircleAlert, Gauge, Link as LinkIcon, Network, ShieldCheck } from 'lucide-react'

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

const statusReasonLabels: Record<string, string> = {
  account_inactive: '账号已停用',
  no_active_plan: '未开通有效套餐',
  subscription_suspended: '订阅已暂停',
  quota_exceeded: '流量已用尽',
  audit_risk: '审计判定存在风险',
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
  if (!overview.audit.enabled) return '审计未启用'
  return overview.audit.risk ? '审计存在风险' : '审计未发现风险'
}

export function UserDashboardPage({
  overview,
  displayName,
  loading = false,
  onNavigateSubscriptions,
}: {
  overview?: UserDashboardOverview
  displayName: string
  loading?: boolean
  onNavigateSubscriptions?: () => void
}) {
  if (!overview) {
    return <div className="user-dashboard-page">{loading ? <DashboardSkeleton /> : null}</div>
  }

  const assignedCount = Math.max(0, Number(overview.assigned_node_count) || 0)
  const usedBytes = Math.max(0, Number(overview.traffic.used_bytes) || 0)
  const limitBytes = Math.max(0, Number(overview.traffic.limit_bytes) || 0)
  const usagePercent = limitBytes > 0 ? Math.min(100, (usedBytes / limitBytes) * 100) : 0
  const totalLabel = !overview.has_active_plan ? '未开通' : limitBytes > 0 ? formatBytes(limitBytes) : '不限量'
  const statusOK = overview.account_status === 'normal'
  const reasons = overview.status_reasons.map(reason => statusReasonLabels[reason] || reason)

  return (
    <div className="user-dashboard-page">
      <section className="dash-welcome user-dash-welcome">
        <div className="dash-welcome-copy">
          <div className="user-dashboard-kicker">
            <Activity size={14} aria-hidden="true" />
            <span>总览</span>
          </div>
          <h1>欢迎回来，{displayName}</h1>
          <p>以下是您的账户、流量及已分配节点概览。随时配置与获取最新的节点订阅。</p>
        </div>
        <div className="dash-welcome-actions">
          <button type="button" onClick={onNavigateSubscriptions}>
            <LinkIcon size={15} aria-hidden="true" />
            <span>订阅</span>
          </button>
        </div>
        <div className="dash-watermark" aria-hidden="true">{assignedCount}</div>
      </section>

      <section className="user-overview-band" aria-label="账户使用概览">
        <div className="user-overview-cell">
          <div className="user-overview-cell-head"><span>已分配节点</span><Network size={17} aria-hidden="true" /></div>
          <strong>{assignedCount}</strong>
          <small>当前有效分配</small>
        </div>

        <div className={`user-overview-cell account ${statusOK ? 'normal' : 'attention'}`}>
          <div className="user-overview-cell-head"><span>账号状态</span>{statusOK ? <ShieldCheck size={17} aria-hidden="true" /> : <CircleAlert size={17} aria-hidden="true" />}</div>
          <strong>{statusOK ? '正常' : '需要关注'}</strong>
          <div className="user-overview-facts">
            <span>{overview.has_active_plan ? '套餐有效' : '未开通套餐'}</span>
            <span>{auditLabel(overview)}</span>
          </div>
          <small>{reasons.length > 0 ? reasons.join(' · ') : '各项服务均可用'}</small>
        </div>

        <div className="user-overview-cell traffic">
          <div className="user-overview-cell-head"><span>本周期流量</span><Gauge size={17} aria-hidden="true" /></div>
          <strong>{formatBytes(usedBytes)}</strong>
          <small>总量 {totalLabel}</small>
          {overview.has_active_plan && limitBytes > 0 ? (
            <div className="user-traffic-progress" role="progressbar" aria-label="本周期流量使用率" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(usagePercent)}>
              <span className={usagePercent >= 100 ? 'danger' : usagePercent >= 80 ? 'warning' : ''} style={{ width: `${usagePercent}%` }} />
            </div>
          ) : null}
        </div>
      </section>
    </div>
  )
}
