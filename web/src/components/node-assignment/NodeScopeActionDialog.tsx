import * as React from 'react'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { Select } from '../ui/select'
import { Input } from '../ui/input'
import { UserPicker, type UserOption } from './UserPicker'
import type { NodeScopeRequest, ScopeNode } from './NodeScopeMenu'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }

type ScopePreview = {
  scope: { kind: string; server_id?: number; server_name?: string; region?: string }
  count: number
  node_refs: { node_type: string; node_id: number }[]
  sample_nodes: { key: string; name: string }[]
  warnings: string[]
  selection_hash: string
}

type PlanChangePreview = {
  users_affected: number
  users_unchanged: number
  nodes_added: string[]
  nodes_removed: string[]
  nodes_unchanged: number
  affected_servers: number[]
  affected_paths: number
  task_count: number
  offline_servers: number[]
  capacity_issues: string[]
}

const SCOPE_LABELS: Record<string, string> = {
  node: '仅此节点',
  entry_inbound: '同一入口',
  entry_server: '同一入口服务器',
  path_server: '路径服务器',
  exit_server: '同一出口服务器',
  exit_region: '同一出口地区',
  external_outbound: '同一导入出口',
}

function toLocalInputValue(iso?: string) {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function fromLocalInputValue(value: string): string | undefined {
  if (!value) return undefined
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}

function scopeName(preview: ScopePreview): string {
  const label = SCOPE_LABELS[preview.scope.kind] || preview.scope.kind
  const detail = preview.scope.server_name || preview.scope.region || ''
  return detail ? `${label} · ${detail}` : label
}

export function NodeScopeActionDialog({ open, node, scope, plans, users, client, notify, onClose, onDone }: {
  open: boolean
  node: ScopeNode | null
  scope: NodeScopeRequest | null
  plans: { id: number; name: string }[]
  users: UserOption[]
  client: AnyClient
  notify: (message: string, tone?: 'success' | 'error' | 'warning') => void
  onClose: () => void
  onDone: () => void | Promise<void>
}) {
  const [preview, setPreview] = React.useState<ScopePreview | null>(null)
  const [includeDisabled, setIncludeDisabled] = React.useState(false)
  const [scopeBusy, setScopeBusy] = React.useState(false)
  const [scopeError, setScopeError] = React.useState('')

  const [planID, setPlanID] = React.useState(0)
  const [planOp, setPlanOp] = React.useState<'add' | 'remove' | 'replace'>('add')
  const [displayGroup, setDisplayGroup] = React.useState('')
  const [planPreview, setPlanPreview] = React.useState<{ preview: PlanChangePreview; expected_revision: number; expected_lock_version: number; base_revision_id: number; node_count: number } | null>(null)
  const [planBusy, setPlanBusy] = React.useState(false)
  const [planApplyBusy, setPlanApplyBusy] = React.useState(false)
  const [planMessage, setPlanMessage] = React.useState('')

  const [userIDs, setUserIDs] = React.useState<Set<number>>(new Set())
  const [effect, setEffect] = React.useState<'allow' | 'deny'>('allow')
  const [reason, setReason] = React.useState('')
  const [startsAt, setStartsAt] = React.useState('')
  const [expiresAt, setExpiresAt] = React.useState('')
  const [exPreview, setExPreview] = React.useState<{ created: number; updated: number; skipped: number; affected_users: number } | null>(null)
  const [exBusy, setExBusy] = React.useState(false)
  const [exApplyBusy, setExApplyBusy] = React.useState(false)
  const [exMessage, setExMessage] = React.useState('')

  const loadScope = React.useCallback(async () => {
    if (!node || !scope) return
    setScopeBusy(true)
    setScopeError('')
    try {
      const res = await client.request<ScopePreview>('/assignable-node-scopes/preview', {
        method: 'POST',
        body: JSON.stringify({ anchor_node_key: node.key, scope, include_disabled: includeDisabled }),
      })
      setPreview(res)
    } catch (e: any) {
      setScopeError(e?.message || String(e))
      setPreview(null)
    } finally {
      setScopeBusy(false)
    }
  }, [node, scope, includeDisabled, client])

  React.useEffect(() => {
    if (open) {
      setPreview(null)
      setScopeError('')
      setPlanPreview(null)
      setPlanMessage('')
      setExPreview(null)
      setExMessage('')
      setUserIDs(new Set())
      void loadScope()
    }
  }, [open, loadScope])

  const runPlanPreview = async () => {
    if (!preview || !planID) { setPlanMessage('请先选择套餐'); return }
    setPlanBusy(true)
    setPlanMessage('')
    try {
      const res = await client.request<{ preview: PlanChangePreview; expected_revision: number; expected_lock_version: number; base_revision_id: number; node_count: number }>(`/subscription-plans/${planID}/nodes/preview`, {
        method: 'POST',
        body: JSON.stringify({ op: planOp, nodes: preview.node_refs, display_group: planOp === 'remove' ? '' : displayGroup }),
      })
      setPlanPreview(res)
    } catch (e: any) {
      setPlanMessage('预览失败：' + (e?.message || String(e)))
    } finally {
      setPlanBusy(false)
    }
  }

  const applyPlanSync = async () => {
    if (!preview || !planPreview) return
    setPlanApplyBusy(true)
    setPlanMessage('')
    try {
      const res = await client.request<{ access_change_id?: number; no_change?: boolean }>(`/subscription-plans/${planID}/nodes/apply`, {
        method: 'POST',
        body: JSON.stringify({
          op: planOp,
          nodes: preview.node_refs,
          display_group: planOp === 'remove' ? '' : displayGroup,
          base_revision_id: planPreview.base_revision_id || 0,
          expected_lock_version: planPreview.expected_lock_version || planPreview.expected_revision || 0,
        }),
      })
      if (res.no_change) {
        notify?.('节点集合没有变化，未创建新版本', 'warning')
      } else if (res.access_change_id) {
        notify?.(`已保存为新版本，正在应用变更 #${res.access_change_id}`, 'success')
      } else {
        notify?.('已保存为新版本', 'success')
      }
      setPlanPreview(null)
      await onDone()
    } catch (e: any) {
      const message = e?.message || String(e)
      setPlanMessage(message.includes('conflict') || message.includes('409') ? '套餐已发生变化（冲突），请重新预览后重试' : '操作失败：' + message)
    } finally {
      setPlanApplyBusy(false)
    }
  }

  const runExceptionPreview = async () => {
    if (!preview) return
    if (userIDs.size === 0) { setExMessage('请先选择用户'); return }
    if (!reason.trim()) { setExMessage('请填写原因'); return }
    if (!expiresAt) { setExMessage('过期时间必填'); return }
    setExBusy(true)
    setExMessage('')
    try {
      const res = await client.request<{ created: number; updated: number; skipped: number; affected_users: number }>('/user-node-exceptions/batch/preview', {
        method: 'POST',
        body: JSON.stringify({
          user_ids: [...userIDs],
          nodes: preview.node_refs,
          effect,
          reason: reason.trim(),
          starts_at: fromLocalInputValue(startsAt),
          expires_at: fromLocalInputValue(expiresAt),
        }),
      })
      setExPreview(res)
    } catch (e: any) {
      setExMessage('预览失败：' + (e?.message || String(e)))
    } finally {
      setExBusy(false)
    }
  }

  const applyExceptionBatch = async () => {
    if (!preview || !exPreview) return
    setExApplyBusy(true)
    setExMessage('')
    try {
      const res = await client.request<{ created: number; updated: number; skipped: number; affected_users: number; access_change_id: number | null; access_change_status: string; queued_tasks: number }>('/user-node-exceptions/batch/apply', {
        method: 'POST',
        body: JSON.stringify({
          user_ids: [...userIDs],
          nodes: preview.node_refs,
          effect,
          reason: reason.trim(),
          starts_at: fromLocalInputValue(startsAt),
          expires_at: fromLocalInputValue(expiresAt),
        }),
      })
      if (res.access_change_id) {
        notify?.(`已创建聚合变更 #${res.access_change_id}（${res.access_change_status}），排队 ${res.queued_tasks} 个任务`, 'success')
      } else {
        notify?.(`没有需要变更的例外（${res.skipped} 项已存在）`, 'warning')
      }
      setExPreview(null)
      await onDone()
    } catch (e: any) {
      setExMessage('操作失败：' + (e?.message || String(e)))
    } finally {
      setExApplyBusy(false)
    }
  }

  return (
    <Dialog isOpen={open} onClose={onClose} title={node ? `节点操作：${node.name}` : '节点操作'} size="lg">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        {scopeBusy && <p className="muted">正在解析节点范围...</p>}
        {scopeError && <p style={{ color: 'var(--color-danger)' }}>{scopeError}</p>}
        {preview && (
          <>
            <div className="card-custom" style={{ padding: 12 }}>
              <div className="section-toolbar" style={{ flexWrap: 'wrap', gap: 8 }}>
                <span style={{ fontWeight: 600 }}>已选择 {preview.count} 个节点</span>
                <Badge variant="outline">{scopeName(preview)}</Badge>
                <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, marginLeft: 'auto' }}>
                  <input type="checkbox" checked={includeDisabled} onChange={e => setIncludeDisabled(e.target.checked)} /> 包含已禁用节点
                </label>
              </div>
              {preview.sample_nodes.length > 0 && (
                <p className="muted" style={{ margin: '8px 0 0' }}>
                  示例：{preview.sample_nodes.map(n => n.name).join('、')}{preview.count > preview.sample_nodes.length ? ` 等 ${preview.count} 个` : ''}
                </p>
              )}
              {(preview.warnings || []).map((w, i) => <p key={i} className="muted" style={{ margin: '6px 0 0' }}>{w}</p>)}
            </div>

            <div className="card-custom" style={{ padding: 12 }}>
              <h3 style={{ marginTop: 0 }}>套餐节点</h3>
              <div className="section-toolbar" style={{ gap: 8, flexWrap: 'wrap' }}>
                <Select value={planID} onChange={e => setPlanID(Number(e.target.value))} style={{ minWidth: 160 }} aria-label="选择套餐">
                  <option value={0}>选择套餐</option>
                  {plans.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
                </Select>
                <Select value={planOp} onChange={e => setPlanOp(e.target.value as 'add' | 'remove' | 'replace')} aria-label="操作类型">
                  <option value="add">加入套餐</option>
                  <option value="remove">从套餐移除</option>
                  <option value="replace">替换套餐节点</option>
                </Select>
                {planOp !== 'remove' && (
                  <Input value={displayGroup} onChange={e => setDisplayGroup(e.target.value)} placeholder="展示分组（可选）" style={{ maxWidth: 180 }} />
                )}
                <Button variant="outline" size="sm" busy={planBusy} onClick={() => void runPlanPreview()}>预览影响</Button>
              </div>
              {planMessage && <p style={{ margin: '8px 0 0', color: planMessage.includes('失败') || planMessage.includes('冲突') ? 'var(--color-danger)' : 'var(--color-success, #16a34a)' }}>{planMessage}</p>}
              {planPreview && (
                <div style={{ marginTop: 10, display: 'flex', flexDirection: 'column', gap: 8 }}>
                  <p className="muted" style={{ margin: 0 }}>
                    新版本将为 {planPreview.node_count} 个节点 · 新增 {planPreview.preview.nodes_added?.length || 0} · 移除 {planPreview.preview.nodes_removed?.length || 0} · 不变 {planPreview.preview.nodes_unchanged || 0} · 受影响用户 {planPreview.preview.users_affected} · 任务 {planPreview.preview.task_count}
                  </p>
                  {(planPreview.preview.capacity_issues || []).length > 0 && (
                    <p style={{ color: 'var(--color-danger)', margin: 0 }}>{planPreview.preview.capacity_issues.join('；')}</p>
                  )}
                  <div style={{ display: 'flex', gap: 8 }}>
                    <Button size="sm" busy={planApplyBusy} onClick={() => void applyPlanSync()}>应用{planOp === 'add' ? '加入' : planOp === 'remove' ? '移除' : '替换'}</Button>
                    <Button size="sm" variant="ghost" onClick={() => setPlanPreview(null)}>取消</Button>
                  </div>
                </div>
              )}
            </div>

            <div className="card-custom" style={{ padding: 12 }}>
              <h3 style={{ marginTop: 0 }}>用户临时例外</h3>
              <p className="muted" style={{ marginTop: 0 }}>批量创建例外只生成一个聚合 access-change；allow 先部署凭据再对订阅可见，deny 立即隐藏并撤销。</p>
              <UserPicker users={users} selected={userIDs} onChange={setUserIDs} />
              <div className="section-toolbar" style={{ gap: 8, flexWrap: 'wrap', marginTop: 10 }}>
                <Select value={effect} onChange={e => setEffect(e.target.value as 'allow' | 'deny')} style={{ minWidth: 100 }} aria-label="例外效果">
                  <option value="allow">临时允许</option>
                  <option value="deny">临时禁止</option>
                </Select>
                <Input value={reason} onChange={e => setReason(e.target.value)} placeholder="原因（必填）" style={{ maxWidth: 220 }} />
                <Input type="datetime-local" value={startsAt} onChange={e => setStartsAt(e.target.value)} aria-label="开始时间（可选）" title="开始时间（可选）" style={{ maxWidth: 190 }} />
                <Input type="datetime-local" value={expiresAt} onChange={e => setExpiresAt(e.target.value)} aria-label="过期时间（必填）" title="过期时间（必填）" style={{ maxWidth: 190 }} />
                <Button variant="outline" size="sm" busy={exBusy} onClick={() => void runExceptionPreview()}>预览</Button>
              </div>
              {exMessage && <p style={{ margin: '8px 0 0', color: exMessage.includes('失败') ? 'var(--color-danger)' : 'var(--color-success, #16a34a)' }}>{exMessage}</p>}
              {exPreview && (
                <div style={{ marginTop: 10, display: 'flex', flexDirection: 'column', gap: 8 }}>
                  <p className="muted" style={{ margin: 0 }}>
                    将创建 {exPreview.created} 条、更新 {exPreview.updated} 条、跳过已有 {exPreview.skipped} 条 · 受影响用户 {exPreview.affected_users} 人
                  </p>
                  <div style={{ display: 'flex', gap: 8 }}>
                    <Button size="sm" busy={exApplyBusy} onClick={() => void applyExceptionBatch()}>批量应用</Button>
                    <Button size="sm" variant="ghost" onClick={() => setExPreview(null)}>取消</Button>
                  </div>
                </div>
              )}
            </div>
          </>
        )}
        <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
          <Button variant="outline" onClick={onClose}>关闭</Button>
        </div>
      </div>
    </Dialog>
  )
}
