import { Check } from 'lucide-react'
type UpdateVersionSnapshot = {
  current?: { version: string; build: string }
  available?: { version: string; build: string }
  update_available: boolean
  status: string
  last_error?: string
}

export function UpdateVersionSummary({ snapshot }: { snapshot: UpdateVersionSnapshot }) {
  const latest = snapshot.available
  const compare = snapshot.update_available && Boolean(latest?.version || latest?.build)
  const current = !snapshot.update_available && !snapshot.last_error && (snapshot.status === 'current' || snapshot.status === 'installed' || Boolean(snapshot.current?.version && snapshot.current.version === latest?.version && snapshot.current.build === latest?.build))
  return <div className="controller-update-version-summary">
    <div className={`controller-update-versions${compare ? ' has-update' : ''}`}>
      <div><span>当前版本</span><strong>{snapshot.current?.version || '—'}</strong><small>{snapshot.current?.build ? `构建 ${snapshot.current.build}` : '暂无构建信息'}</small></div>
      {compare && <><span className="controller-update-version-arrow" aria-hidden="true">→</span><div><span>最新版本</span><strong>{latest?.version || '—'}</strong><small>{latest?.build ? `构建 ${latest.build}` : '暂无构建信息'}</small></div></>}
    </div>
    {current && <span className="controller-update-current"><Check size={14} aria-hidden="true" />已是最新版本</span>}
  </div>
}

export type AgentUpdateOverview = { target_build: string; enrolled: number; current: number; running: number; pending: number; offline: number; failure_count: number }

export function agentUpdateCompletion(status: AgentUpdateOverview) {
  return status.enrolled > 0 ? Math.min(100, Math.round(status.current / status.enrolled * 100)) : 0
}

export function AgentUpdateSummary({ status }: { status: AgentUpdateOverview }) {
  const segments = [
    { label: '已完成', value: status.current, tone: 'success' },
    { label: '更新中', value: status.running, tone: 'info' },
    { label: '待更新', value: status.pending, tone: 'warning' },
    { label: '离线', value: status.offline, tone: 'muted' },
    { label: '失败', value: status.failure_count, tone: 'danger' },
  ]
  return <div className="agent-update-summary segmented-progress">
    <span className="agent-update-target">目标构建 <strong>{status.target_build || '—'}</strong></span>
    <div className="segmented-progress-bar" role="progressbar" aria-label="Agent 版本同步进度" aria-valuemin={0} aria-valuemax={100} aria-valuenow={agentUpdateCompletion(status)} aria-valuetext={`${status.current} / ${status.enrolled} · ${agentUpdateCompletion(status)}%`}>
      <span className="is-success" style={{ flexGrow: status.current }} />
      <span className="is-muted" style={{ flexGrow: Math.max(0, status.enrolled - status.current) }} />
    </div>
    <div className="segmented-progress-legend">{segments.map(item => <span key={item.label} className={`is-${item.tone}`}><i aria-hidden="true" />{item.label}<strong>{item.value}</strong></span>)}</div>
  </div>
}

export function formatAgentUpdateError(error: string) {
  const raw = String(error || '').trim()
  if (/invalid agent credentials|auth rejected|unauthorized|authentication failed/i.test(raw)) {
    return { title: '认证失败', description: 'Agent 凭证无效或已失效', raw }
  }
  if (/offline|not connected|disconnected/i.test(raw)) {
    return { title: '连接中断', description: '更新时 Agent 未连接，请确认服务器连接状态后重试', raw }
  }
  if (/timeout|timed out|deadline exceeded/i.test(raw)) {
    return { title: '更新超时', description: '等待更新响应超时，请检查网络连接和任务详情', raw }
  }
  if (/checksum|hash mismatch|signature|verification failed/i.test(raw)) {
    return { title: '校验失败', description: '更新文件未通过完整性或签名校验，请查看任务详情后重试', raw }
  }
  if (/no space left|disk full/i.test(raw)) {
    return { title: '空间不足', description: '服务器磁盘空间不足，请释放空间后重试', raw }
  }
  if (/connection refused|network is unreachable|no route to host|dial tcp|download failed/i.test(raw)) {
    return { title: '网络连接失败', description: '无法连接更新服务，请检查服务器网络后重试', raw }
  }
  return { title: '更新失败', description: 'Agent 更新未完成，请查看任务详情确认原因', raw }
}
