import { useEffect, useMemo, useState } from 'react'
import { AlertTriangle, Info, RefreshCw } from 'lucide-react'

import { Dialog } from './components/ui/dialog'
import { configurationSyncBusyRows, configurationSyncBusyStateLabel, configurationSyncFailedRows, configurationSyncFailureIssues, configurationSyncPresentation, type ConfigurationSyncRow, type ConfigurationSyncServerRef } from './configuration-sync'

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
  const presentation = configurationSyncPresentation(rows, saving, retrying, servers)
  const failed = useMemo(() => configurationSyncFailedRows(rows, servers), [rows, servers])
  const busyRows = useMemo(() => configurationSyncBusyRows(rows, servers), [rows, servers])
  const issues = useMemo(() => configurationSyncFailureIssues(failed), [failed])
  const serverNames = useMemo(() => new Map(servers.map(server => [Number(server.id), String(server.name || '').trim()])), [servers])
  const inboundByID = useMemo(() => new Map(inbounds.map(inbound => [Number(inbound.id), inbound])), [inbounds])
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
        <Dialog isOpen={detailsOpen} onClose={() => setDetailsOpen(false)} title="需要处理" size="lg" className="configuration-sync-dialog">
          <div id="configuration-sync-failure-dialog" className="configuration-sync-dialog-body">
            <div className="configuration-sync-summary">
              <AlertTriangle size={20} aria-hidden="true" />
              <div>
                <strong>部分配置尚未完成同步</strong>
                <p>{issues.every(issue => issue.kind === 'busy')
                  ? `${failed.length} 个同步任务暂时中断，不是这些服务器各自的配置错误。`
                  : `${issues.length} 个问题影响了 ${failed.length} 个同步任务，不表示每台服务器各自都有问题。`}</p>
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
                  {issue.rawError && <details><summary>诊断详情</summary><code>{issue.rawError}</code></details>}
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
    <>
      <button type="button" className="deploy-status-pill details-trigger" onClick={() => setDetailsOpen(true)} aria-haspopup="dialog" aria-expanded={detailsOpen}>
        <Info size={15} aria-hidden="true" />
        <span>需要处理</span>
      </button>
      <Dialog isOpen={detailsOpen} onClose={() => setDetailsOpen(false)} title="需要处理" size="lg" className="configuration-sync-dialog">
        <div className="configuration-sync-dialog-body">
          <p>当前没有需要处理的配置同步问题。</p>
          {rows.length === 0 && <p className="muted">尚无配置同步记录，这不代表所有服务均已验证可用。</p>}
          {busyRows.length > 0 && <details><summary>查看进行中的同步</summary><ul>{busyRows.map(item => <li key={item.server_id}>{serverDisplayName(item.server_id, serverNames)} · {configurationSyncBusyStateLabel(item.state)}</li>)}</ul></details>}
          {onNavigate && <button type="button" className="ghost" onClick={() => { setDetailsOpen(false); onNavigate('tasks') }}>查看任务</button>}
        </div>
      </Dialog>
    </>
  )
}
