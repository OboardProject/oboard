import { useEffect, useMemo, useState } from 'react'
import { AlertTriangle, Check, Info, RefreshCw } from 'lucide-react'

import { Dialog } from './components/ui/dialog'
import { configurationSyncFailureIssues, configurationSyncPresentation, type ConfigurationSyncRow } from './configuration-sync'

type ConfigurationSyncServer = { id: number; name?: string }

type ConfigurationSyncStatusProps = {
  rows: ConfigurationSyncRow[]
  saving?: boolean
  retrying?: boolean
  canOperate?: boolean
  servers?: ConfigurationSyncServer[]
  onRetry?: () => void
  onNavigate?: (tab: 'proxy-paths' | 'tasks') => void
}

export function ConfigurationSyncStatus({ rows, saving = false, retrying = false, canOperate = true, servers = [], onRetry, onNavigate }: ConfigurationSyncStatusProps) {
  const [detailsOpen, setDetailsOpen] = useState(false)
  const presentation = configurationSyncPresentation(rows, saving, retrying)
  const failed = rows.filter(item => item.state === 'failed')
  const issues = useMemo(() => configurationSyncFailureIssues(failed), [rows])
  const serverNames = useMemo(() => new Map(servers.map(server => [Number(server.id), String(server.name || '').trim()])), [servers])
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
                <strong>最新配置未同步到 {failed.length} 台服务器</strong>
                <p>{issues.length} 个配置问题影响了这些服务器。相同错误已合并显示，无需逐台处理。</p>
              </div>
            </div>
            <ol className="configuration-sync-issue-list">
              {issues.map((issue, index) => (
                <li className="configuration-sync-issue" key={issue.key}>
                  <div className="configuration-sync-issue-head">
                    <div><small>问题 {index + 1}</small><h4>{issue.title}</h4></div>
                    <span>{issue.serverIDs.length} 台受影响</span>
                  </div>
                  <p>{issue.explanation}</p>
                  <div className="configuration-sync-resolution"><strong>处理方法</strong><span>{issue.resolution}</span></div>
                  <details>
                    <summary>查看受影响服务器</summary>
                    <ul>{issue.serverIDs.map(serverID => <li key={serverID}>{serverNames.get(serverID) || `服务器 #${serverID}`}</li>)}</ul>
                  </details>
                  {issue.rawError && <details><summary>查看原始错误</summary><code>{issue.rawError}</code></details>}
                  {onNavigate && <button type="button" className="ghost configuration-sync-target" onClick={() => { setDetailsOpen(false); onNavigate(issue.targetTab) }}>{issue.targetLabel}</button>}
                </li>
              ))}
            </ol>
            <div className="dialog-actions configuration-sync-actions">
              <button type="button" className="ghost" onClick={() => setDetailsOpen(false)}>稍后处理</button>
              <button type="button" onClick={() => { setDetailsOpen(false); onRetry?.() }} disabled={retrying || !onRetry} aria-busy={retrying}>
                <RefreshCw size={15} className={retrying ? 'spin' : ''} aria-hidden="true" />
                {retrying ? '正在重试...' : `重试 ${failed.length} 台服务器`}
              </button>
            </div>
          </div>
        </Dialog>
      </>
    )
  }
  return (
    <div className={`deploy-status-pill ${saving || presentation.busy ? 'info' : presentation.tone === 'ok' ? 'ok' : 'warn'}`} aria-live="polite">
      {saving || presentation.busy ? <RefreshCw size={15} className="spin" aria-hidden="true" /> : presentation.tone === 'ok' ? <Check size={16} aria-hidden="true" /> : <Info size={16} aria-hidden="true" />}
      <span>{presentation.label}</span>
    </div>
  )
}
