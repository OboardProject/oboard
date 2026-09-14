import { useEffect, useMemo, useState } from 'react'
import { Dialog } from '../ui/dialog'
import {
  cleanupActionsFor,
  cleanupOutcomeMessage,
  cleanupOutcomeTone,
  configHealthFindingKey,
  groupConfigHealthFindings,
  hasDestructiveSelection,
  isSelectableFinding,
  remedyLabels,
  selectableFindings,
  severityLabels,
  type ConfigHealthCleanupResponse,
  type ConfigHealthFinding,
  type ConfigHealthReport,
} from '../../config-health'

type Client = { request: (path: string, init?: RequestInit) => Promise<any> }

export interface ConfigHealthDialogProps {
  client: Client
  canCleanup: boolean
  onClose: () => void
  onCleaned?: (response: ConfigHealthCleanupResponse) => void
}

// ConfigHealthDialog is the repair surface for records the ordinary form cannot
// fix. It never edits a field directly: the operator picks conclusions, the
// Controller derives the change, and a destructive pick needs a second confirm.
export function ConfigHealthDialog({ client, canCleanup, onClose, onCleaned }: ConfigHealthDialogProps) {
  const [report, setReport] = useState<ConfigHealthReport | null>(null)
  const [revision, setRevision] = useState(0)
  const [reload, setReload] = useState(0)
  const [loading, setLoading] = useState(true)
  // Load and action errors are separate state on purpose. A revision conflict
  // triggers a reload, and a single error slot would clear the conflict message
  // before the operator could read why their cleanup did not run.
  const [loadError, setLoadError] = useState('')
  const [actionError, setActionError] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [preview, setPreview] = useState<ConfigHealthCleanupResponse | null>(null)
  const [busy, setBusy] = useState(false)
  const [confirmingDestructive, setConfirmingDestructive] = useState(false)
  const [outcome, setOutcome] = useState<ConfigHealthCleanupResponse | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setLoadError('')
    void client.request('/config-health', { signal: controller.signal }).then((result: any) => {
      if (controller.signal.aborted) return
      setReport(result?.report || null)
      setRevision(Number(result?.revision) || 0)
      // A refreshed report invalidates any preview and any selection that no
      // longer corresponds to a live finding.
      setPreview(null)
      setConfirmingDestructive(false)
      const live = new Set<string>(((result?.report?.findings || []) as ConfigHealthFinding[]).map(configHealthFindingKey))
      setSelected(previous => new Set([...previous].filter(key => live.has(key))))
    }).catch((err: any) => {
      if (!controller.signal.aborted) setLoadError(err?.message || '配置体检加载失败')
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [client, reload])

  const findings = report?.findings || []
  const groups = useMemo(() => groupConfigHealthFindings(findings), [findings])
  const selectable = useMemo(() => selectableFindings(findings), [findings])
  const destructive = hasDestructiveSelection(findings, selected)
  const previewByKey = useMemo(() => {
    const map = new Map<string, string>()
    for (const result of preview?.results || []) {
      const key = configHealthFindingKey({ code: result.code, scope: result.scope, resource_id: result.resource_id })
      map.set(key, result.removed_fields?.length ? `将移除 ${result.removed_fields.join('、')}` : (result.reason || ''))
    }
    return map
  }, [preview])

  const toggle = (key: string) => {
    setPreview(null)
    setActionError('')
    setConfirmingDestructive(false)
    setSelected(previous => {
      const next = new Set(previous)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }
  const toggleAll = () => {
    setPreview(null)
    setActionError('')
    setConfirmingDestructive(false)
    setSelected(previous => (previous.size === selectable.length ? new Set() : new Set(selectable.map(configHealthFindingKey))))
  }

  const run = async (confirm: boolean) => {
    if (busy || selected.size === 0) return
    setBusy(true)
    setActionError('')
    try {
      const response = await client.request('/config-health/cleanup', {
        method: 'POST',
        body: JSON.stringify({ revision, confirm, actions: cleanupActionsFor(findings, selected) }),
      }) as ConfigHealthCleanupResponse
      if (!confirm) {
        setPreview(response)
        return
      }
      setOutcome(response)
      onCleaned?.(response)
      setSelected(new Set())
      setPreview(null)
      setConfirmingDestructive(false)
      setReload(value => value + 1)
    } catch (err: any) {
      setActionError(err?.message || '清理失败')
      // A revision conflict means the topology moved; reload so the operator
      // acts on what is actually there now.
      if (String(err?.message || '').includes('发生了变化')) setReload(value => value + 1)
    } finally { setBusy(false) }
  }

  const primaryLabel = !canCleanup ? '仅管理员可清理'
    : destructive && !confirmingDestructive ? `确认删除 ${selected.size} 项`
    : busy ? '处理中…'
    : `清理选中的 ${selected.size} 项`

  const onPrimary = () => {
    if (!canCleanup || selected.size === 0) return
    if (destructive && !confirmingDestructive) {
      setConfirmingDestructive(true)
      return
    }
    void run(true)
  }

  return <Dialog isOpen onClose={() => { if (!busy) onClose() }} title="配置体检" size="lg" footer={<>
    <button type="button" className="ghost" disabled={busy} onClick={onClose}>关闭</button>
    <button type="button" className="ghost" disabled={busy || selected.size === 0} onClick={() => void run(false)}>预览改动</button>
    <button type="button" className={destructive && confirmingDestructive ? 'danger' : ''} disabled={!canCleanup || busy || selected.size === 0} aria-busy={busy} onClick={onPrimary}>{primaryLabel}</button>
  </>}>
    {loading && <p className="muted">正在检查入口、链路与分流配置…</p>}

    {!loading && findings.length === 0 && !outcome && (
      <p className="muted">入口、链路、分流与 DNS 策略均未发现不规范配置。</p>
    )}

    {outcome && (
      <p className={cleanupOutcomeTone(outcome) === 'danger' ? 'danger-text' : 'muted'} role="status">
        {cleanupOutcomeMessage(outcome)}
      </p>
    )}

    {!loading && findings.length > 0 && (
      <>
        <div className="config-health-toolbar">
          <p className="muted">
            这里可以移除普通表单无法保存修正的内容，也可以为版本冲突的下发通道重新分配版本号。选中后可先预览改动，删除类操作需要二次确认。
            {report?.summary.truncated ? ' 问题过多，仅显示前若干项。' : ''}
          </p>
          <button type="button" className="ghost" disabled={busy || selectable.length === 0} onClick={toggleAll}>
            {selected.size === selectable.length && selectable.length > 0 ? '取消全选' : `全选可清理的 ${selectable.length} 项`}
          </button>
        </div>

        {groups.map(group => (
          <section key={group.scope} className="config-health-group" aria-label={group.label}>
            <h3>{group.label} · {group.findings.length}</h3>
            <ul className="config-health-list">
              {group.findings.map(item => {
                const key = configHealthFindingKey(item)
                const selectableItem = isSelectableFinding(item)
                const previewText = previewByKey.get(key)
                return (
                  <li key={key} className={`config-health-item severity-${item.severity}${selected.has(key) ? ' is-selected' : ''}`}>
                    <label className="config-health-item-head">
                      <input
                        type="checkbox"
                        checked={selected.has(key)}
                        disabled={!selectableItem || busy || !canCleanup}
                        onChange={() => toggle(key)}
                        aria-label={`选择：${item.title}`}
                      />
                      <span className="config-health-item-title">
                        <strong>{item.title}</strong>
                        <em className={`config-health-severity severity-${item.severity}`}>{severityLabels[item.severity]}</em>
                      </span>
                    </label>
                    <p className="config-health-item-target">
                      {item.resource_name ? `${item.resource_name} · ` : ''}#{item.resource_id}
                      {item.server_name && item.server_name !== item.resource_name ? ` · ${item.server_name}` : ''}
                      {item.path ? ` · ${item.path}` : ''}
                    </p>
                    {item.detail && <p className="muted config-health-item-detail">{item.detail}</p>}
                    <p className="config-health-item-remedy">
                      <span>{remedyLabels[item.remedy.kind]}</span>
                      {item.remedy.summary ? `：${item.remedy.summary}` : ''}
                    </p>
                    {previewText && <p className="config-health-item-preview" role="note">{previewText}</p>}
                  </li>
                )
              })}
            </ul>
          </section>
        ))}
      </>
    )}

    {destructive && confirmingDestructive && (
      <p className="danger-text" role="alert">选中的内容包含删除操作，删除后无法恢复。再次点击确认继续。</p>
    )}

    {actionError && <p className="danger-text" role="alert">{actionError}</p>}

    {loadError && <div role="alert"><p className="danger-text">{loadError}</p><button type="button" className="ghost" disabled={busy} onClick={() => setReload(value => value + 1)}>重新检查</button></div>}
  </Dialog>
}
