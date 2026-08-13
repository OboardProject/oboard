import * as React from 'react'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Dialog } from '../ui/dialog'
import { Select } from '../ui/select'
import { Input } from '../ui/input'
import { Switch } from '../ui/switch'
import { DateTimePicker } from '../ui/datetime-picker'
import { UserPicker, type UserOption } from './UserPicker'
import type { NodeScopeRequest, ScopeNode } from './NodeScopeMenu'
import { Package, Plus, Trash2, UserCheck, AlertTriangle, Info, Sparkles } from 'lucide-react'

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

type AssignedPlan = {
  plan_id: number
  name: string
  display_group?: string
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

  // Assigned plans state
  const [assignedPlans, setAssignedPlans] = React.useState<AssignedPlan[]>([])
  const [loadingPlans, setLoadingPlans] = React.useState(false)
  const [selectedAddPlanID, setSelectedAddPlanID] = React.useState(0)
  const [addDisplayGroup, setAddDisplayGroup] = React.useState('')
  const [planActionBusyId, setPlanActionBusyId] = React.useState<number | null>(null)
  const [addingPlan, setAddingPlan] = React.useState(false)
  const [planMessage, setPlanMessage] = React.useState<{ text: string; tone: 'success' | 'error' } | null>(null)

  // Secondary User Auth Dialog state
  const [userAuthOpen, setUserAuthOpen] = React.useState(false)
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

  const loadNodePlans = React.useCallback(async () => {
    if (!node) return
    setLoadingPlans(true)
    try {
      const res = await client.request<{ plans?: AssignedPlan[] }>(`/assignable-nodes/${node.type}/${node.id}`)
      if (res?.plans) {
        setAssignedPlans(res.plans)
      } else if ((node as any).plans) {
        setAssignedPlans((node as any).plans)
      }
    } catch {
      if ((node as any).plans) {
        setAssignedPlans((node as any).plans)
      }
    } finally {
      setLoadingPlans(false)
    }
  }, [node, client])

  React.useEffect(() => {
    if (open && node) {
      setPreview(null)
      setScopeError('')
      setPlanMessage(null)
      setSelectedAddPlanID(0)
      setAddDisplayGroup('')
      setUserAuthOpen(false)
      setUserIDs(new Set())
      setReason('')
      setStartsAt('')
      setExpiresAt('')
      setExPreview(null)
      setExMessage('')
      void loadScope()
      void loadNodePlans()
    }
  }, [open, node, loadScope, loadNodePlans])

  // Add node to a plan
  const handleAddPlan = async () => {
    if (!preview || !selectedAddPlanID) {
      setPlanMessage({ text: '请先选择要加入的套餐', tone: 'error' })
      return
    }
    const targetPlan = plans.find(p => p.id === selectedAddPlanID)
    const planName = targetPlan?.name || `套餐 #${selectedAddPlanID}`
    setAddingPlan(true)
    setPlanMessage(null)
    try {
      // 1. Preview
      const prevRes = await client.request<{ preview: PlanChangePreview; expected_revision: number; expected_lock_version: number; base_revision_id: number; node_count: number }>(`/subscription-plans/${selectedAddPlanID}/nodes/preview`, {
        method: 'POST',
        body: JSON.stringify({ op: 'add', nodes: preview.node_refs, display_group: addDisplayGroup.trim() }),
      })

      // 2. Apply
      const applyRes = await client.request<{ access_change_id?: number; no_change?: boolean }>(`/subscription-plans/${selectedAddPlanID}/nodes/apply`, {
        method: 'POST',
        body: JSON.stringify({
          op: 'add',
          nodes: preview.node_refs,
          display_group: addDisplayGroup.trim(),
          base_revision_id: prevRes.base_revision_id || 0,
          expected_lock_version: prevRes.expected_lock_version || prevRes.expected_revision || 0,
        }),
      })

      if (applyRes.no_change) {
        notify?.(`节点已存在于套餐【${planName}】中`, 'warning')
      } else {
        notify?.(`已成功将节点加入套餐【${planName}】`, 'success')
      }
      setSelectedAddPlanID(0)
      setAddDisplayGroup('')
      await loadNodePlans()
      await onDone()
    } catch (e: any) {
      const msg = e?.message || String(e)
      setPlanMessage({ text: msg.includes('conflict') || msg.includes('409') ? '套餐版本冲突，请重试' : '加入失败：' + msg, tone: 'error' })
    } finally {
      setAddingPlan(false)
    }
  }

  // Remove node from a plan
  const handleRemovePlan = async (targetPlanId: number, planName: string) => {
    if (!preview) return
    setPlanActionBusyId(targetPlanId)
    setPlanMessage(null)
    try {
      // 1. Preview
      const prevRes = await client.request<{ preview: PlanChangePreview; expected_revision: number; expected_lock_version: number; base_revision_id: number; node_count: number }>(`/subscription-plans/${targetPlanId}/nodes/preview`, {
        method: 'POST',
        body: JSON.stringify({ op: 'remove', nodes: preview.node_refs, display_group: '' }),
      })

      // 2. Apply
      const applyRes = await client.request<{ access_change_id?: number; no_change?: boolean }>(`/subscription-plans/${targetPlanId}/nodes/apply`, {
        method: 'POST',
        body: JSON.stringify({
          op: 'remove',
          nodes: preview.node_refs,
          display_group: '',
          base_revision_id: prevRes.base_revision_id || 0,
          expected_lock_version: prevRes.expected_lock_version || prevRes.expected_revision || 0,
        }),
      })

      if (applyRes.no_change) {
        notify?.(`套餐【${planName}】未包含此节点`, 'warning')
      } else {
        notify?.(`已从套餐【${planName}】移出该节点`, 'success')
      }
      await loadNodePlans()
      await onDone()
    } catch (e: any) {
      const msg = e?.message || String(e)
      setPlanMessage({ text: msg.includes('conflict') || msg.includes('409') ? '套餐版本冲突，请重试' : '移出失败：' + msg, tone: 'error' })
    } finally {
      setPlanActionBusyId(null)
    }
  }

  // User exception preview
  const runExceptionPreview = async () => {
    if (!preview) return
    if (userIDs.size === 0) { setExMessage('请先选择用户'); return }
    if (!reason.trim()) { setExMessage('请填写授权原因'); return }
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

  // User exception batch apply
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
        notify?.(`已创建授权变更 #${res.access_change_id}（${res.access_change_status}），排队 ${res.queued_tasks} 个任务`, 'success')
      } else {
        notify?.(`没有需要变更的授权（${res.skipped} 项已存在）`, 'warning')
      }
      setExPreview(null)
      setUserAuthOpen(false)
      await onDone()
    } catch (e: any) {
      setExMessage('操作失败：' + (e?.message || String(e)))
    } finally {
      setExApplyBusy(false)
    }
  }

  const unaddedPlans = plans.filter(p => !assignedPlans.some(ap => ap.plan_id === p.id))

  return (
    <>
      <Dialog isOpen={open} onClose={onClose} title={node ? `节点操作：${node.name}` : '节点操作'} size="lg">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          {scopeBusy && <p className="muted" style={{ margin: 0, fontSize: 13 }}>正在解析节点范围...</p>}
          {scopeError && <p style={{ color: 'var(--color-danger)', margin: 0 }}>{scopeError}</p>}
          {preview && (
            <>
              {/* Scope summary */}
              <div className="card-custom" style={{ padding: '10px 14px' }}>
                <div className="section-toolbar" style={{ flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
                  <span style={{ fontWeight: 700, fontSize: 13 }}>已选择 {preview.count} 个节点</span>
                  <Badge variant="outline">{scopeName(preview)}</Badge>
                  <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12, marginLeft: 'auto', cursor: 'pointer' }}>
                    <Switch size="sm" checked={includeDisabled} onChange={setIncludeDisabled} ariaLabel="包含已禁用节点" /> 包含已禁用节点
                  </label>
                </div>
                {preview.sample_nodes.length > 0 && (
                  <p className="muted" style={{ margin: '6px 0 0', fontSize: 12 }}>
                    示例：{preview.sample_nodes.map(n => n.name).join('、')}{preview.count > preview.sample_nodes.length ? ` 等 ${preview.count} 个` : ''}
                  </p>
                )}
                {(preview.warnings || []).map((w, i) => <p key={i} className="muted" style={{ margin: '4px 0 0', fontSize: 12 }}>{w}</p>)}
              </div>

              {/* Section 1: 套餐 */}
              <div className="card-custom" style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 10 }}>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <Package size={15} style={{ color: 'var(--color-primary, #3b82f6)' }} />
                    <h3 style={{ margin: 0, fontSize: 13.5, fontWeight: 700 }}>套餐</h3>
                  </div>
                  <span className="muted" style={{ fontSize: 11.5 }}>包含在 {assignedPlans.length} 个套餐中</span>
                </div>

                {/* Plan list */}
                {loadingPlans ? (
                  <p className="muted" style={{ margin: '4px 0', fontSize: 12 }}>正在加载包含此节点的套餐...</p>
                ) : assignedPlans.length === 0 ? (
                  <div style={{ padding: '12px', textAlign: 'center', background: 'var(--surface-2)', borderRadius: 'var(--radius-md)', color: 'var(--muted)', fontSize: 12 }}>
                    当前节点尚未加入任何套餐
                  </div>
                ) : (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                    {assignedPlans.map(p => (
                      <div
                        key={p.plan_id}
                        style={{
                          display: 'flex',
                          alignItems: 'center',
                          justifyContent: 'space-between',
                          padding: '6px 10px',
                          background: 'var(--surface-2)',
                          borderRadius: 'var(--radius-md)',
                          border: '1px solid var(--border)',
                        }}
                      >
                        <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
                          <span style={{ fontWeight: 600, fontSize: 13, color: 'var(--text-strong)' }}>{p.name}</span>
                          {p.display_group && (
                            <span style={{ fontSize: 11, padding: '1px 6px', background: 'var(--surface-3)', borderRadius: 4, color: 'var(--muted)' }}>
                              分组：{p.display_group}
                            </span>
                          )}
                        </div>
                        <Button
                          variant="ghost"
                          size="sm"
                          busy={planActionBusyId === p.plan_id}
                          disabled={addingPlan || planActionBusyId !== null}
                          onClick={() => void handleRemovePlan(p.plan_id, p.name)}
                          style={{ color: 'var(--danger)', height: 26, padding: '0 8px', fontSize: 12 }}
                          title={`从套餐【${p.name}】移出此节点`}
                        >
                          <Trash2 size={13} style={{ marginRight: 4 }} />
                          移出
                        </Button>
                      </div>
                    ))}
                  </div>
                )}

                {/* Add to plan toolbar */}
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginTop: 4, paddingTop: 10, borderTop: '1px dashed var(--border)' }}>
                  <Select
                    value={selectedAddPlanID}
                    onChange={e => setSelectedAddPlanID(Number(e.target.value))}
                    style={{ minWidth: 160, flex: '1 1 160px' }}
                    aria-label="选择要加入的套餐"
                  >
                    <option value={0}>选择要加入的套餐</option>
                    {unaddedPlans.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
                  </Select>
                  <Input
                    value={addDisplayGroup}
                    onChange={e => setAddDisplayGroup(e.target.value)}
                    placeholder="展示分组（可选）"
                    style={{ maxWidth: 160, flex: '1 1 120px' }}
                  />
                  <Button
                    size="sm"
                    busy={addingPlan}
                    disabled={!selectedAddPlanID || planActionBusyId !== null}
                    onClick={() => void handleAddPlan()}
                    style={{ whiteSpace: 'nowrap' }}
                  >
                    <Plus size={14} style={{ marginRight: 4 }} />
                    加入套餐
                  </Button>
                </div>
                {planMessage && (
                  <p style={{ margin: 0, fontSize: 12, color: planMessage.tone === 'error' ? 'var(--color-danger)' : 'var(--color-success, #16a34a)' }}>
                    {planMessage.text}
                  </p>
                )}
              </div>

              {/* Section 2: 授权用户 */}
              <div className="card-custom" style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 10 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  <UserCheck size={15} style={{ color: 'var(--color-primary, #3b82f6)' }} />
                  <h3 style={{ margin: 0, fontSize: 13.5, fontWeight: 700 }}>用户授权</h3>
                </div>
                <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8, padding: '8px 10px', background: 'var(--surface-2)', borderRadius: 'var(--radius-md)', fontSize: 12, color: 'var(--muted)', lineHeight: 1.5 }}>
                  <Info size={15} style={{ flexShrink: 0, marginTop: 1, color: 'var(--color-primary, #3b82f6)' }} />
                  <span>
                    <strong>授权规则说明</strong>：用户可使用的节点为其<strong>所在套餐节点</strong>与<strong>单独授权节点</strong>的<strong>并集</strong>。即使套餐未包含此节点，在此授权后指定用户也能正常使用。
                  </span>
                </div>
                <div style={{ display: 'flex', justifyContent: 'flex-start' }}>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setUserAuthOpen(true)}
                    style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}
                  >
                    <UserCheck size={14} />
                    <span>授权用户</span>
                  </Button>
                </div>
              </div>
            </>
          )}

          <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 4 }}>
            <Button variant="outline" onClick={onClose}>关闭</Button>
          </div>
        </div>
      </Dialog>

      {/* Secondary Dialog: 授权用户 */}
      <Dialog
        isOpen={userAuthOpen}
        onClose={() => setUserAuthOpen(false)}
        title={node ? `授权用户 · ${node.name}` : '授权用户'}
        size="md"
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8, padding: '8px 10px', background: 'var(--surface-2)', borderRadius: 'var(--radius-md)', fontSize: 12, color: 'var(--muted)', lineHeight: 1.5 }}>
            <Sparkles size={15} style={{ flexShrink: 0, marginTop: 1, color: 'var(--color-primary, #3b82f6)' }} />
            <span>为选定用户独立配置此节点的访问权限（临时允许或临时禁止），权限独立生效并与套餐取并集。</span>
          </div>

          <UserPicker users={users} selected={userIDs} onChange={setUserIDs} />

          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))', gap: 8, marginTop: 4 }}>
            <div>
              <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--muted)', marginBottom: 3 }}>授权效果</label>
              <Select value={effect} onChange={e => setEffect(e.target.value as 'allow' | 'deny')} style={{ width: '100%' }} aria-label="授权效果">
                <option value="allow">临时允许</option>
                <option value="deny">临时禁止</option>
              </Select>
            </div>
            <div>
              <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--muted)', marginBottom: 3 }}>授权原因（必填）</label>
              <Input value={reason} onChange={e => setReason(e.target.value)} placeholder="例如：测试授权、VIP体验" />
            </div>
            <div>
              <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--muted)', marginBottom: 3 }}>开始时间（可选）</label>
              <DateTimePicker value={startsAt} onChange={setStartsAt} placeholder="立即生效" aria-label="开始时间" />
            </div>
            <div>
              <label style={{ display: 'block', fontSize: 11, fontWeight: 600, color: 'var(--muted)', marginBottom: 3 }}>过期时间（必填）</label>
              <DateTimePicker value={expiresAt} onChange={setExpiresAt} placeholder="选择过期时间" aria-label="过期时间" />
            </div>
          </div>

          {exMessage && (
            <p style={{ margin: 0, fontSize: 12, color: exMessage.includes('失败') ? 'var(--color-danger)' : 'var(--color-success, #16a34a)' }}>
              {exMessage}
            </p>
          )}

          {exPreview && (
            <div style={{ padding: '8px 12px', background: 'var(--surface-2)', borderRadius: 'var(--radius-md)', fontSize: 12 }}>
              <p className="muted" style={{ margin: 0 }}>
                将创建 <strong>{exPreview.created}</strong> 条、更新 <strong>{exPreview.updated}</strong> 条、跳过已有 <strong>{exPreview.skipped}</strong> 条 · 受影响用户 <strong>{exPreview.affected_users}</strong> 人
              </p>
            </div>
          )}

          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 8 }}>
            <Button variant="ghost" onClick={() => setUserAuthOpen(false)}>取消</Button>
            {exPreview ? (
              <Button busy={exApplyBusy} onClick={() => void applyExceptionBatch()}>批量应用授权</Button>
            ) : (
              <Button busy={exBusy} onClick={() => void runExceptionPreview()}>预览影响</Button>
            )}
          </div>
        </div>
      </Dialog>
    </>
  )
}

export default NodeScopeActionDialog
