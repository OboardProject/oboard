import { useEffect, useId, useMemo, useState } from 'react'
import { AlertTriangle, Check, Info, RefreshCw } from 'lucide-react'

import { Dialog } from './components/ui/dialog'
import { configurationSyncBusyRows, configurationSyncBusyStateLabel, configurationSyncFailureIssues, configurationSyncPresentation, type ConfigurationSyncRow, type ConfigurationSyncServerRef } from './configuration-sync'

type ConfigurationSyncServer = ConfigurationSyncServerRef
type ConfigurationSyncInbound = { id: number; server_id: number; name?: string; protocol?: string; listen_ip?: string; port?: number }

type ConfigurationSyncStatusProps = {
  rows: ConfigurationSyncRow[]
  saving?: boolean
  retrying?: boolean
  canOperate?: boolean
  servers?: ConfigurationSyncServer[]
  inbounds?: ConfigurationSyncInbound[]
  onRetry?: () => void
  onNavigate?: (tab: 'proxy-paths' | 'tasks') => void
  onLocateInbound?: (inboundID: number) => void
}

function serverDisplayName(serverID: number, names: Map<number, string>) {
  return names.get(serverID) || `服务器 #${serverID}`
}

export function ConfigurationSyncStatus({ rows, saving = false, retrying = false, canOperate = true, servers = [], inbounds = [], onRetry, onNavigate, onLocateInbound }: ConfigurationSyncStatusProps) {
  const [detailsOpen, setDetailsOpen] = useState(false)
  const popoverID = useId()
  const presentation = configurationSyncPresentation(rows, saving, retrying, servers)
  const failed = rows.filter(item => item.state === 'failed')
  const busyRows = useMemo(() => configurationSyncBusyRows(rows, servers), [rows, servers])
  const issues = useMemo(() => configurationSyncFailureIssues(failed), [rows])
  const serverNames = useMemo(() => new Map(servers.map(server => [Number(server.id), String(server.name || '').trim()])), [servers])
  const inboundByID = useMemo(() => new Map(inbounds.map(inbound => [Number(inbound.id), inbound])), [inbounds])
  const showBusyPopover = failed.length === 0 && busyRows.length > 0
  useEffect(() => {
    if (failed.length === 0) setDetailsOpen(false)
  }, [failed.length])
  if (!canOperate) return null
  if (failed.length > 0) {
    return (
      <>
        <button
          type="button"
          className="deploy-status-pill details-trigger danger"
          onClick={() => setDetailsOpen(true)}
          aria-label={`${presentation.label}，查看详情`}
          aria-haspopup="dialog"
          aria-expanded={detailsOpen}
          aria-controls="configuration-sync-failure-dialog"
          aria-live="polite"
        >
          <AlertTriangle size={15} aria-hidden="true" />
          <span>{presentation.label}</span>
        </button>
        <Dialog isOpen={detailsOpen} onClose={() => setDetailsOpen(false)} title="配置同步失败" size="lg" className="configuration-sync-dialog">
          <div id="configuration-sync-failure-dialog" className="configuration-sync-dialog-body">
            <div className="configuration-sync-summary">
              <AlertTriangle size={20} aria-hidden="true" />
              <div>
                <strong>最新配置在部署准备阶段被阻塞</strong>
                <p>{issues.length} 个配置问题阻塞了 {failed.length} 个同步任务。这不表示 {failed.length} 台服务器各自都有问题。</p>
              </div>
            </div>
            <ol className="configuration-sync-issue-list">
              {issues.map((issue, index) => {
                const inbound = issue.inboundID ? inboundByID.get(issue.inboundID) : undefined
                const sourceServerName = inbound ? serverNames.get(inbound.server_id) : ''
                const inboundName = String(inbound?.name || '').trim()
                const issueTitle = inboundName ? `入口「${inboundName}」存在重复的直接出口分支` : issue.title
                const resolution = inbound
                  ? `系统将直接定位到${sourceServerName ? `服务器「${sourceServerName}」上的` : ''}入口「${inboundName || `#${inbound.id}`}」。删除或停用同一位置的重复直出分支，保存后会自动重新同步。`
                  : issue.resolution
                return <li className="configuration-sync-issue" key={issue.key}>
                  <div className="configuration-sync-issue-head">
                    <div><small>问题 {index + 1}</small><h4>{issueTitle}</h4></div>
                    <span>{issue.serverIDs.length} 个任务被阻塞</span>
                  </div>
                  {(inbound || issue.conflictingPathNames?.length === 2) && <dl className="configuration-sync-resource">
                    {inbound && <>
                      <div><dt>入口</dt><dd>{inboundName || '未命名入口'} <code>#{inbound.id}</code></dd></div>
                      <div><dt>所属服务器</dt><dd>{sourceServerName || `服务器 #${inbound.server_id}`}</dd></div>
                      <div><dt>协议与监听</dt><dd>{String(inbound.protocol || '未知协议').toUpperCase()} · {inbound.listen_ip || '自动监听'}:{Number(inbound.port || 0)}</dd></div>
                    </>}
                    {issue.conflictingPathNames?.length === 2 && <div><dt>冲突分支</dt><dd>{issue.conflictingPathNames.join(' ↔ ')}</dd></div>}
                  </dl>}
                  <p>{issue.explanation}</p>
                  <div className="configuration-sync-resolution"><strong>处理方法</strong><span>{resolution}</span></div>
                  <details>
                    <summary>查看本轮被阻塞的同步任务</summary>
                    <ul>{issue.serverIDs.map(serverID => <li key={serverID}>{serverDisplayName(serverID, serverNames)}</li>)}</ul>
                  </details>
                  {issue.rawError && <details><summary>查看原始错误</summary><code>{issue.rawError}</code></details>}
                  {issue.inboundID && onLocateInbound
                    ? <button type="button" className="ghost configuration-sync-target" onClick={() => { setDetailsOpen(false); onLocateInbound(issue.inboundID!) }}>定位并选中「{inboundName || `入口 #${issue.inboundID}`}」</button>
                    : onNavigate && <button type="button" className="ghost configuration-sync-target" onClick={() => { setDetailsOpen(false); onNavigate(issue.targetTab) }}>{issue.targetLabel}</button>}
                </li>
              })}
            </ol>
            <div className="dialog-actions configuration-sync-actions">
              <button type="button" className="ghost" onClick={() => setDetailsOpen(false)}>稍后处理</button>
              <button type="button" onClick={() => { setDetailsOpen(false); onRetry?.() }} disabled={retrying || !onRetry} aria-busy={retrying}>
                <RefreshCw size={15} className={retrying ? 'spin' : ''} aria-hidden="true" />
                {retrying ? '正在重试...' : `重新尝试 ${failed.length} 个同步任务`}
              </button>
            </div>
          </div>
        </Dialog>
      </>
    )
  }
  return (
    <div
      className={`deploy-status-pill ${saving || presentation.busy ? 'info' : presentation.tone === 'ok' ? 'ok' : 'warn'}${showBusyPopover ? ' has-popover' : ''}`}
      aria-live="polite"
      aria-describedby={showBusyPopover ? popoverID : undefined}
      tabIndex={showBusyPopover ? 0 : undefined}
    >
      {saving || presentation.busy ? <RefreshCw size={15} className="spin" aria-hidden="true" /> : presentation.tone === 'ok' ? <Check size={16} aria-hidden="true" /> : <Info size={16} aria-hidden="true" />}
      <span>{presentation.label}</span>
      {showBusyPopover && (
        <div id={popoverID} role="tooltip" className="configuration-sync-popover">
          <strong>正在同步的服务器</strong>
          <ul>
            {busyRows.map(item => (
              <li key={item.server_id}>
                {serverDisplayName(item.server_id, serverNames)}
                <small>{configurationSyncBusyStateLabel(item.state)}</small>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}
