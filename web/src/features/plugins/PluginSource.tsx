import React, { useCallback, useEffect, useRef, useState } from 'react'
import { AlertTriangle, ArrowUpCircle, CheckCircle2, GitBranch, RefreshCw } from 'lucide-react'
import { Badge } from '../../components/ui/badge'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import { DiffList } from './PackageInstallDialog'
import { errorMessage, formatTime, repositoryLabel, shortHash, sourceRefLabel } from './domain'
import type { InstallationDetail, PluginUpdateCheck } from './types'

export interface PluginSourceProps {
  detail: InstallationDetail
  isAdmin: boolean
  busy: boolean
  onCheck: () => Promise<PluginUpdateCheck>
  onApply: (check: PluginUpdateCheck) => Promise<boolean>
}

// PluginSource records where a GitHub plugin came from and lets an
// administrator update it from that same repository and ref.
export function PluginSource({ detail, isAdmin, busy, onCheck, onApply }: PluginSourceProps) {
  const [check, setCheck] = useState<PluginUpdateCheck | null>(null)
  const [checking, setChecking] = useState(false)
  const [applying, setApplying] = useState(false)
  const [error, setError] = useState('')
  const [reviewOpen, setReviewOpen] = useState(false)
  const latestCall = useRef(0)

  const github = detail.source_kind === 'github' && Boolean(detail.source_repository)
  const run = useCallback(async () => {
    const call = ++latestCall.current
    setChecking(true); setError('')
    try {
      const result = await onCheck()
      if (call === latestCall.current) setCheck(result)
    } catch (e) {
      if (call === latestCall.current) { setCheck(null); setError(errorMessage(e, '检查更新失败')) }
    } finally {
      if (call === latestCall.current) setChecking(false)
    }
  }, [onCheck])

  // A new active version invalidates the previous answer.
  useEffect(() => {
    setCheck(null); setError(''); setReviewOpen(false)
    if (github && isAdmin && detail.published) void run()
    return () => { latestCall.current++ }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [detail.id, detail.package_id])

  if (!github) return null
  const preview = check?.preview
  const apply = async () => {
    if (!check) return
    setApplying(true)
    const done = await onApply(check)
    setApplying(false)
    if (done) setReviewOpen(false)
  }
  const startUpdate = () => {
    if (!preview) return
    // Without new permissions there is nothing left to review: update directly.
    if (preview.diff.expanded || preview.diff.new_required.length) setReviewOpen(true)
    else void apply()
  }

  return <section className="plugin-source" aria-label="插件来源">
    <div className="plugin-source-head">
      <GitBranch size={16} aria-hidden="true" />
      <div className="min-w-0">
        <a href={detail.source_repository} target="_blank" rel="noopener noreferrer" className="plugin-source-repo">{repositoryLabel(detail.source_repository)}</a>
        <p className="plugin-env-help">{sourceRefLabel(detail.source_ref)} · 提交 <code>{shortHash(detail.source_commit)}</code></p>
      </div>
      {isAdmin && detail.published && <Button variant="ghost" size="icon" aria-label="检查更新" disabled={checking || applying || busy} onClick={() => void run()}><RefreshCw size={15} className={checking ? 'animate-spin' : ''} /></Button>}
    </div>
    {isAdmin && <div className="plugin-source-status" role="status">
      {checking && !check && <span className="plugin-env-help">正在检查更新…</span>}
      {error && <span className="plugin-field-error">{error}</span>}
      {check?.status === 'up_to_date' && <span className="plugin-source-ok"><CheckCircle2 size={14} aria-hidden="true" />已是最新 · {formatTime(check.checked_at)} 检查</span>}
      {check?.status === 'pinned' && <span className="plugin-env-help">固定在某个提交，不会有新版本。</span>}
      {check?.status === 'update_available' && preview && <>
        <Badge variant="warning">有新版本 {preview.version}</Badge>
        <span className="plugin-env-help">当前 {check.current_version} · 新提交 <code>{shortHash(check.latest_commit)}</code></span>
        <Button size="sm" busy={applying} disabled={busy} onClick={startUpdate}><ArrowUpCircle size={14} />更新到 {preview.version}</Button>
      </>}
      {check?.status === 'blocked' && preview && <div className="plugin-callout danger"><AlertTriangle size={16} aria-hidden="true" /><p>{preview.blocked}</p></div>}
    </div>}
    {reviewOpen && preview && <Dialog isOpen onClose={() => setReviewOpen(false)} title={`更新到 ${preview.version}`} size="default"
      footer={<><Button variant="ghost" onClick={() => setReviewOpen(false)}>取消</Button><Button busy={applying} onClick={() => void apply()}>确认更新</Button></>}>
      <div className="plugin-preview">
        <p className="plugin-env-help">{detail.name} {check?.current_version} → {preview.version}，来源 {repositoryLabel(detail.source_repository)}（{sourceRefLabel(detail.source_ref)}）。</p>
        <h4 className="plugin-subhead">与当前版本相比</h4>
        <DiffList diff={preview.diff} />
        {preview.diff.expanded && <div className="plugin-callout warning"><AlertTriangle size={16} aria-hidden="true" /><p>新版本扩大了权限。更新后，管理员需要重新审核每个实例的授权才能运行。</p></div>}
        {preview.diff.new_required.length > 0 && <div className="plugin-callout warning"><AlertTriangle size={16} aria-hidden="true" /><p>新版本增加了必填配置，相关实例补全前不会运行。</p></div>}
      </div>
    </Dialog>}
  </section>
}
