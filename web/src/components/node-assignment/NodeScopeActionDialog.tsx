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
import { Package, Plus, Trash2, UserCheck, AlertTriangle, Info } from 'lucide-react'

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

type AssignedAuthorization = {
  id: number
  user_id: number
  username: string
  nickname?: string
  effect: 'allow' | 'deny'
  status: 'pending' | 'active'
  reason?: string
  starts_at?: string
  expires_at?: string
  effective: boolean
  plan_includes: boolean
  plan_id?: number
  plan_name?: string
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
  notify?: (message: string, tone?: 'success' | 'error' | 'warning') => void
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
  const pendingPlanMutationsRef = React.useRef(new Map<string, { id: number; action: 'add' | 'remove'; planID: number; planName: string }>())
  const planMutationQueuesRef = React.useRef(new Map<number, Promise<void>>())
  const planMutationSequenceRef = React.useRef(0)
  const detailRequestRef = React.useRef(0)
  const scopeRequestRef = React.useRef(0)
  const activeNodeKeyRef = React.useRef(node?.key || '')
  activeNodeKeyRef.current = node?.key || ''

  // Direct user authorization state
  const [assignedAuthorizations, setAssignedAuthorizations] = React.useState<AssignedAuthorization[]>([])
  const [loadingAuthorizations, setLoadingAuthorizations] = React.useState(false)
  const [authorizationToDelete, setAuthorizationToDelete] = React.useState<AssignedAuthorization | null>(null)
  const [revokingAuthorizationIDs, setRevokingAuthorizationIDs] = React.useState<Set<number>>(new Set())

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
    const requestID = ++scopeRequestRef.current
    const nodeKey = node.key
    setPreview(null)
    setScopeBusy(true)
    setScopeError('')
    try {
      const res = await client.request<ScopePreview>('/assignable-node-scopes/preview', {
        method: 'POST',
        body: JSON.stringify({ anchor_node_key: node.key, scope, include_disabled: includeDisabled }),
      })
      if (requestID !== scopeRequestRef.current || activeNodeKeyRef.current !== nodeKey) return
      setPreview(res)
    } catch (e: any) {
      if (requestID !== scopeRequestRef.current || activeNodeKeyRef.current !== nodeKey) return
      setScopeError(e?.message || String(e))
      setPreview(null)
    } finally {
      if (requestID === scopeRequestRef.current && activeNodeKeyRef.current === nodeKey) setScopeBusy(false)
    }
  }, [node, scope, includeDisabled, client])

  const loadNodeDetail = React.useCallback(async (silent = false): Promise<AssignedPlan[] | null> => {
    if (!node) return null
    const requestID = ++detailRequestRef.current
    const nodeKey = node.key
    if (!silent) {
      setLoadingPlans(true)
      setLoadingAuthorizations(true)
    }
    try {
      const res = await client.request<{ plans?: AssignedPlan[]; authorizations?: AssignedAuthorization[] }>(`/assignable-nodes/${node.type}/${node.id}`)
      if (requestID !== detailRequestRef.current || activeNodeKeyRef.current !== nodeKey) return null
      const nextPlans = res?.plans || ((node as any).plans as AssignedPlan[] | undefined) || []
      setAssignedPlans(nextPlans)
      setAssignedAuthorizations(res?.authorizations || [])
      return nextPlans
    } catch {
      if (requestID !== detailRequestRef.current || activeNodeKeyRef.current !== nodeKey || silent) return null
      const fallbackPlans = ((node as any).plans as AssignedPlan[] | undefined) || []
      setAssignedPlans(fallbackPlans)
      setAssignedAuthorizations([])
      return fallbackPlans
    } finally {
      if (!silent && requestID === detailRequestRef.current && activeNodeKeyRef.current === nodeKey) {
        setLoadingPlans(false)
        setLoadingAuthorizations(false)
      }
    }
  }, [node, client])

  React.useEffect(() => {
    if (open && node) {
      setPreview(null)
      setScopeError('')
      setAuthorizationToDelete(null)
      setRevokingAuthorizationIDs(new Set())
      setSelectedAddPlanID(0)
      setAddDisplayGroup('')
      const pendingMutation = pendingPlanMutationsRef.current.get(node.key)
      setPlanActionBusyId(pendingMutation?.action === 'remove' ? pendingMutation.planID : null)
      setAddingPlan(pendingMutation?.action === 'add')
      setUserAuthOpen(false)
      setUserIDs(new Set())
      setReason('')
      setStartsAt('')
      setExpiresAt('')
      setExPreview(null)
      setExMessage('')
      void loadScope()
      void loadNodeDetail()
    }
  }, [open, node, loadScope, loadNodeDetail])

  const enqueuePlanMutation = React.useCallback(<T,>(planID: number, mutate: () => Promise<T>): Promise<T> => {
    const previous = planMutationQueuesRef.current.get(planID) || Promise.resolve()
    const result = previous.catch(() => undefined).then(mutate)
    const tail = result.then(() => undefined, () => undefined)
    planMutationQueuesRef.current.set(planID, tail)
    void tail.finally(() => {
      if (planMutationQueuesRef.current.get(planID) === tail) planMutationQueuesRef.current.delete(planID)
    })
    return result
  }, [])

  const confirmDesiredPlanNodes = React.useCallback(async (planID: number, nodeRefs: ScopePreview['node_refs'], shouldContain: boolean): Promise<boolean | null> => {
    try {
      const detail = await client.request<{ latest_nodes?: { node_type: string; node_id: number }[] }>(`/subscription-plans/${planID}`)
      const latestKeys = new Set((detail.latest_nodes || []).map(item => `${item.node_type}:${item.node_id}`))
      return nodeRefs.every(item => latestKeys.has(`${item.node_type}:${item.node_id}`) === shouldContain)
    } catch {
      return null
    }
  }, [client])

  const refreshParent = React.useCallback(() => {
    try {
      void Promise.resolve(onDone()).catch(() => undefined)
    } catch {
      // The save already succeeded; a background list refresh must not turn it into an operation failure.
    }
  }, [onDone])

  const refreshAfterPlanMutation = React.useCallback((expectedNodeKey: string) => {
    if (activeNodeKeyRef.current === expectedNodeKey) void loadNodeDetail(true)
    refreshParent()
  }, [loadNodeDetail, refreshParent])

  // Add node to a plan. Update the row before the network round-trip so the
  // interaction stays responsive, then reconcile from the saved desired state.
  const handleAddPlan = async () => {
    if (!preview || !selectedAddPlanID) {
      notify?.('请先选择要加入的套餐', 'warning')
      return
    }
    const mutationNodeKey = node?.key || ''
    if (pendingPlanMutationsRef.current.has(mutationNodeKey)) return
    const targetPlanID = selectedAddPlanID
    const targetPlan = plans.find(p => p.id === targetPlanID)
    const planName = targetPlan?.name || `套餐 #${targetPlanID}`
    const displayGroup = addDisplayGroup.trim()
    const mutationID = ++planMutationSequenceRef.current
    pendingPlanMutationsRef.current.set(mutationNodeKey, { id: mutationID, action: 'add', planID: targetPlanID, planName })
    detailRequestRef.current++
    setAddingPlan(true)
    setAssignedPlans(current => current.some(p => p.plan_id === targetPlanID)
      ? current
      : [...current, { plan_id: targetPlanID, name: planName, display_group: displayGroup }].sort((a, b) => a.plan_id - b.plan_id))
    setSelectedAddPlanID(0)
    setAddDisplayGroup('')
    try {
      const applyRes = await enqueuePlanMutation(targetPlanID, async () => {
        const prevRes = await client.request<{ preview: PlanChangePreview; expected_revision: number; expected_lock_version: number; base_revision_id: number; node_count: number }>(`/subscription-plans/${targetPlanID}/nodes/preview`, {
          method: 'POST',
          body: JSON.stringify({ op: 'add', nodes: preview.node_refs, display_group: displayGroup }),
        })
        return client.request<{ access_change_id?: number; no_change?: boolean; reconcile_queued?: boolean }>(`/subscription-plans/${targetPlanID}/nodes/apply`, {
          method: 'POST',
          body: JSON.stringify({
            op: 'add',
            nodes: preview.node_refs,
            display_group: displayGroup,
            base_revision_id: prevRes.base_revision_id || 0,
            expected_lock_version: prevRes.expected_lock_version || prevRes.expected_revision || 0,
          }),
        })
      })

      if (activeNodeKeyRef.current === mutationNodeKey) {
        if (applyRes.no_change) {
          notify?.(`节点已存在于套餐【${planName}】中`, 'warning')
        } else if (applyRes.reconcile_queued) {
          notify?.(`已保存到套餐【${planName}】，正在应用`, 'success')
        } else {
          notify?.(`已成功将节点加入套餐【${planName}】`, 'success')
        }
      }
      refreshAfterPlanMutation(mutationNodeKey)
    } catch (e: any) {
      const [confirmed, latestPlans] = activeNodeKeyRef.current === mutationNodeKey
        ? await Promise.all([confirmDesiredPlanNodes(targetPlanID, preview.node_refs, true), loadNodeDetail(true)])
        : [null, null]
      if (activeNodeKeyRef.current === mutationNodeKey) {
        if (confirmed === true) {
          notify?.(`节点已保存到套餐【${planName}】`, 'success')
          refreshParent()
        } else {
          if (latestPlans === null) setAssignedPlans(current => current.filter(p => p.plan_id !== targetPlanID))
          setSelectedAddPlanID(targetPlanID)
          setAddDisplayGroup(displayGroup)
          const msg = e?.message || String(e)
          const uncertain = confirmed === null ? '，最新状态核对失败，请刷新确认' : ''
          notify?.(msg.includes('conflict') || msg.includes('409') ? '套餐版本冲突，请重试' : '加入失败：' + msg + uncertain, 'error')
        }
      }
    } finally {
      if (pendingPlanMutationsRef.current.get(mutationNodeKey)?.id === mutationID) pendingPlanMutationsRef.current.delete(mutationNodeKey)
      if (activeNodeKeyRef.current === mutationNodeKey) setAddingPlan(false)
    }
  }

  // Remove node from a plan with the same optimistic feedback and synchronous
  // guard, preventing a fast double click from submitting the operation twice.
  const handleRemovePlan = async (targetPlan: AssignedPlan) => {
    if (!preview) return
    const mutationNodeKey = node?.key || ''
    if (pendingPlanMutationsRef.current.has(mutationNodeKey)) return
    const mutationID = ++planMutationSequenceRef.current
    pendingPlanMutationsRef.current.set(mutationNodeKey, { id: mutationID, action: 'remove', planID: targetPlan.plan_id, planName: targetPlan.name })
    detailRequestRef.current++
    setPlanActionBusyId(targetPlan.plan_id)
    setAssignedPlans(current => current.filter(p => p.plan_id !== targetPlan.plan_id))
    try {
      const applyRes = await enqueuePlanMutation(targetPlan.plan_id, async () => {
        const prevRes = await client.request<{ preview: PlanChangePreview; expected_revision: number; expected_lock_version: number; base_revision_id: number; node_count: number }>(`/subscription-plans/${targetPlan.plan_id}/nodes/preview`, {
          method: 'POST',
          body: JSON.stringify({ op: 'remove', nodes: preview.node_refs, display_group: '' }),
        })
        return client.request<{ access_change_id?: number; no_change?: boolean; reconcile_queued?: boolean }>(`/subscription-plans/${targetPlan.plan_id}/nodes/apply`, {
          method: 'POST',
          body: JSON.stringify({
            op: 'remove',
            nodes: preview.node_refs,
            display_group: '',
            base_revision_id: prevRes.base_revision_id || 0,
            expected_lock_version: prevRes.expected_lock_version || prevRes.expected_revision || 0,
          }),
        })
      })

      if (activeNodeKeyRef.current === mutationNodeKey) {
        if (applyRes.no_change) {
          notify?.(`套餐【${targetPlan.name}】未包含此节点`, 'warning')
        } else if (applyRes.reconcile_queued) {
          notify?.(`已保存从套餐【${targetPlan.name}】移出节点的操作，正在应用`, 'success')
        } else {
          notify?.(`已从套餐【${targetPlan.name}】移出该节点`, 'success')
        }
      }
      refreshAfterPlanMutation(mutationNodeKey)
    } catch (e: any) {
      const [confirmed, latestPlans] = activeNodeKeyRef.current === mutationNodeKey
        ? await Promise.all([confirmDesiredPlanNodes(targetPlan.plan_id, preview.node_refs, false), loadNodeDetail(true)])
        : [null, null]
      if (activeNodeKeyRef.current === mutationNodeKey) {
        if (confirmed === true) {
          notify?.(`节点已从套餐【${targetPlan.name}】移出`, 'success')
          refreshParent()
        } else {
          if (latestPlans === null) {
            setAssignedPlans(current => current.some(p => p.plan_id === targetPlan.plan_id)
              ? current
              : [...current, targetPlan].sort((a, b) => a.plan_id - b.plan_id))
          }
          const msg = e?.message || String(e)
          const uncertain = confirmed === null ? '，最新状态核对失败，请刷新确认' : ''
          notify?.(msg.includes('conflict') || msg.includes('409') ? '套餐版本冲突，请重试' : '移出失败：' + msg + uncertain, 'error')
        }
      }
    } finally {
      if (pendingPlanMutationsRef.current.get(mutationNodeKey)?.id === mutationID) pendingPlanMutationsRef.current.delete(mutationNodeKey)
      if (activeNodeKeyRef.current === mutationNodeKey) setPlanActionBusyId(null)
    }
  }

  // User exception preview
  const runExceptionPreview = async () => {
    if (!preview) return
    if (userIDs.size === 0) { setExMessage('请先选择用户'); return }
    setExBusy(true)
    setExMessage('')
    try {
      const payload: any = {
        user_ids: [...userIDs],
        nodes: preview.node_refs,
        effect,
        reason: reason.trim(),
      }
      const starts = fromLocalInputValue(startsAt)
      const expires = fromLocalInputValue(expiresAt)
      if (starts) payload.starts_at = starts
      if (expires) payload.expires_at = expires
      const res = await client.request<{ created: number; updated: number; skipped: number; affected_users: number }>('/user-node-exceptions/batch/preview', {
        method: 'POST',
        body: JSON.stringify(payload),
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
      const payload: any = {
        user_ids: [...userIDs],
        nodes: preview.node_refs,
        effect,
        reason: reason.trim(),
      }
      const starts = fromLocalInputValue(startsAt)
      const expires = fromLocalInputValue(expiresAt)
      if (starts) payload.starts_at = starts
      if (expires) payload.expires_at = expires
      const res = await client.request<{ created: number; updated: number; skipped: number; affected_users: number; access_change_id: number | null; access_change_status: string; queued_tasks: number }>('/user-node-exceptions/batch/apply', {
        method: 'POST',
        body: JSON.stringify(payload),
      })
      if (res.access_change_id) {
        notify?.(`已创建授权变更 #${res.access_change_id}（${res.access_change_status}），排队 ${res.queued_tasks} 个任务`, 'success')
      } else {
        notify?.(`没有需要变更的授权（${res.skipped} 项已存在）`, 'warning')
      }
      setExPreview(null)
      setUserAuthOpen(false)
      await loadNodeDetail()
      await onDone()
    } catch (e: any) {
      setExMessage('操作失败：' + (e?.message || String(e)))
    } finally {
      setExApplyBusy(false)
    }
  }

  const revokeAuthorization = async () => {
    const authorization = authorizationToDelete
    if (!authorization || revokingAuthorizationIDs.has(authorization.id)) return
    setAuthorizationToDelete(null)
    setRevokingAuthorizationIDs(current => new Set(current).add(authorization.id))
    try {
      const res = await client.request<{ access_change_id?: number; access_change_status?: string }>(`/user-node-exceptions/${authorization.id}`, { method: 'DELETE' })
      notify?.(res.access_change_id ? `已提交撤销授权：变更 #${res.access_change_id}` : '授权已撤销', 'success')
      await loadNodeDetail()
      await onDone()
    } catch (e: any) {
      setRevokingAuthorizationIDs(current => {
        const next = new Set(current)
        next.delete(authorization.id)
        return next
      })
      notify?.('撤销失败：' + (e?.message || String(e)), 'error')
    }
  }

  const unaddedPlans = plans.filter(p => !assignedPlans.some(ap => ap.plan_id === p.id))

  return (
    <>
      <Dialog isOpen={open} onClose={onClose} title={node ? `节点操作：${node.name}` : '节点操作'} size="xl" className="node-scope-action-dialog node-signal-dialog">
        <div className="node-scope-action-body">
          {scopeBusy && <p className="muted" style={{ margin: 0, fontSize: 13 }}>正在解析节点范围...</p>}
          {scopeError && <p style={{ color: 'var(--color-danger)', margin: 0 }}>{scopeError}</p>}
          {preview && (
            <>
              {/* Scope summary */}
              <section className="node-scope-summary" aria-label="操作范围">
                <div className="node-scope-summary-toolbar">
                  <span className="node-scope-summary-count">已选择 {preview.count} 个节点</span>
                  <Badge variant="outline">{scopeName(preview)}</Badge>
                  <label className="node-scope-summary-include">
                    <Switch size="sm" checked={includeDisabled} onChange={setIncludeDisabled} ariaLabel="包含已禁用节点" /> 包含已禁用节点
                  </label>
                </div>
                {preview.sample_nodes.length > 0 && (
                  <p className="muted" style={{ margin: '6px 0 0', fontSize: 12 }}>
                    示例：{preview.sample_nodes.map(n => n.name).join('、')}{preview.count > preview.sample_nodes.length ? ` 等 ${preview.count} 个` : ''}
                  </p>
                )}
                {(preview.warnings || []).map((w, i) => <p key={i} className="muted" style={{ margin: '4px 0 0', fontSize: 12 }}>{w}</p>)}
              </section>

              <div className="signal-scope-columns">
              {/* Section 1: 套餐 */}
              <section className="node-scope-plan-card">
                <div className="node-scope-section-head">
                  <div className="node-scope-section-title">
                    <Package size={15} style={{ color: 'var(--color-primary, #3b82f6)' }} />
                    <h3>所属套餐</h3>
                  </div>
                  <span className="muted node-scope-section-meta">
                    包含在 <strong>{assignedPlans.length}</strong> 个套餐中
                  </span>
                </div>

                {/* Plan list */}
                {loadingPlans ? (
                  <p className="muted" style={{ margin: '4px 0', fontSize: 12 }}>正在加载包含此节点的套餐...</p>
                ) : assignedPlans.length === 0 ? (
                  <div style={{ padding: '14px 12px', textAlign: 'center', background: 'var(--surface-2)', borderRadius: 'var(--radius-md)', color: 'var(--muted)', fontSize: 12.5 }}>
                    当前节点尚未加入任何套餐
                  </div>
                ) : (
                  <div className="plan-assigned-list">
                    {assignedPlans.map((p, index) => (
                      <div
                        key={p.plan_id}
                        className="plan-assigned-row"
                        style={{
                          borderBottom: index < assignedPlans.length - 1 ? '1px solid var(--border)' : 'none',
                        }}
                      >
                        <div className="plan-assigned-row-copy">
                          <span className="plan-assigned-row-name">{p.name}</span>
                          {p.display_group && (
                            <span className="plan-assigned-row-group">
                              {p.display_group}
                            </span>
                          )}
                        </div>
                        <button
                          type="button"
                          disabled={addingPlan || planActionBusyId !== null}
                          onClick={() => void handleRemovePlan(p)}
                          className="plan-remove-icon-btn"
                          title={`从套餐【${p.name}】移出此节点`}
                          aria-label={`从套餐【${p.name}】移出此节点`}
                        >
                          <Trash2 size={14} />
                        </button>
                      </div>
                    ))}
                  </div>
                )}

                {/* Add to plan toolbar */}
                <div className="node-scope-plan-add">
                  <Select
                    value={selectedAddPlanID}
                    onChange={e => setSelectedAddPlanID(Number(e.target.value))}
                    disabled={loadingPlans || addingPlan || planActionBusyId !== null}
                    aria-label="选择要加入的套餐"
                  >
                    <option value={0}>选择要加入的套餐...</option>
                    {unaddedPlans.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
                  </Select>
                  <Input
                    value={addDisplayGroup}
                    onChange={e => setAddDisplayGroup(e.target.value)}
                    disabled={loadingPlans || addingPlan || planActionBusyId !== null}
                    placeholder="展示分组（可选）"
                    aria-label="展示分组"
                  />
                  <Button
                    size="sm"
                    variant="secondary"
                    busy={addingPlan}
                    disabled={loadingPlans || !selectedAddPlanID || planActionBusyId !== null}
                    onClick={() => void handleAddPlan()}
                  >
                    <Plus size={14} />
                    加入套餐
                  </Button>
                </div>
              </section>

              {/* Section 2: 授权用户 */}
              <section className="node-scope-auth-card">
                <div className="node-scope-section-head">
                  <div className="node-scope-section-title">
                    <UserCheck size={15} style={{ color: 'var(--color-primary, #3b82f6)' }} />
                    <h3>用户授权</h3>
                  </div>
                  <Button variant="outline" size="sm" onClick={() => setUserAuthOpen(true)}>
                    <Plus size={14} />
                    <span>添加用户</span>
                  </Button>
                </div>
                <div className="node-scope-auth-hint">
                  <Info size={15} aria-hidden="true" />
                  <span>
                    <strong>授权规则说明</strong>：单独授权与用户套餐包含的节点取<strong>并集</strong>，同一节点不会重复。拒绝授权优先于套餐和允许授权。
                  </span>
                </div>
                {loadingAuthorizations ? (
                  <p className="muted" style={{ margin: '4px 0', fontSize: 12 }}>正在加载已授权用户...</p>
                ) : assignedAuthorizations.length === 0 ? (
                  <div className="node-scope-auth-empty">尚未添加单独用户授权</div>
                ) : (
                  <div className="node-authorization-list">
                    {assignedAuthorizations.map(authorization => {
                      const revoking = revokingAuthorizationIDs.has(authorization.id)
                      return (
                        <div className="node-authorization-row" key={authorization.id}>
                          <div className="node-authorization-copy">
                            <div className="node-authorization-user-line">
                              <strong>{authorization.nickname || authorization.username}</strong>
                              {authorization.nickname && <span className="muted">@{authorization.username}</span>}
                              <Badge variant={authorization.effect === 'allow' ? 'success' : 'destructive'}>{authorization.effect === 'allow' ? '允许' : '拒绝'}</Badge>
                              {authorization.status === 'pending' && <Badge variant="warning">待生效</Badge>}
                              {authorization.plan_includes && <Badge variant="secondary" title={authorization.plan_name ? `套餐“${authorization.plan_name}”也包含此节点` : '用户套餐也包含此节点'}>套餐也包含</Badge>}
                            </div>
                            <div className="node-authorization-meta">
                              <span>{authorization.reason || '无备注'}</span>
                              <span>{authorization.expires_at ? `到期 ${new Date(authorization.expires_at).toLocaleString()}` : '永久有效'}</span>
                            </div>
                          </div>
                          <button
                            type="button"
                            disabled={revoking}
                            onClick={() => setAuthorizationToDelete(authorization)}
                            className="plan-remove-icon-btn"
                            title={`撤销 ${authorization.username} 的单独授权`}
                            aria-label={`撤销 ${authorization.username} 的单独授权`}
                          >
                            <Trash2 size={14} />
                          </button>
                        </div>
                      )
                    })}
                  </div>
                )}
              </section>
              </div>
            </>
          )}

          <div className="node-scope-dialog-footer">
            <Button variant="outline" onClick={onClose}>关闭</Button>
          </div>
        </div>
      </Dialog>

      <Dialog
        isOpen={authorizationToDelete !== null}
        onClose={() => setAuthorizationToDelete(null)}
        title="撤销单独授权"
        size="default"
      >
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <p style={{ margin: 0 }}>
            确定撤销 <strong>{authorizationToDelete?.nickname || authorizationToDelete?.username}</strong> 的此节点单独授权吗？
          </p>
          {authorizationToDelete?.plan_includes && (
            <div className="node-scope-auth-hint">
              <Info size={15} aria-hidden="true" />
              <span>该用户的套餐也包含此节点。撤销单独授权后，用户仍会通过套餐继续获得此节点。</span>
            </div>
          )}
          <div className="node-scope-auth-actions">
            <Button variant="ghost" onClick={() => setAuthorizationToDelete(null)}>取消</Button>
            <Button variant="destructive" onClick={() => void revokeAuthorization()}>撤销授权</Button>
          </div>
        </div>
      </Dialog>

      {/* Secondary Dialog: 授权用户 */}
      <Dialog
        isOpen={userAuthOpen}
        onClose={() => setUserAuthOpen(false)}
        title={node ? `授权用户 · ${node.name}` : '授权用户'}
        size="xl"
        className="node-scope-auth-dialog node-signal-dialog"
      >
        <div className="node-scope-auth-body">
          <div className="node-scope-auth-hint">
            <Info size={15} aria-hidden="true" />
            <span>操作范围：{preview ? scopeName(preview) : '加载中'} · {preview?.count || 0} 个节点。拒绝优先于套餐与允许授权；时间留空则永久有效。</span>
          </div>

          <UserPicker users={users} selected={userIDs} onChange={setUserIDs} maxHeight={360} />

          <div className="node-scope-auth-fields">
            <div>
              <label className="node-scope-field-label">授权效果</label>
              <Select value={effect} onChange={e => setEffect(e.target.value as 'allow' | 'deny')} aria-label="授权效果">
                <option value="allow">允许</option>
                <option value="deny">禁止</option>
              </Select>
            </div>
            <div>
              <label className="node-scope-field-label">授权原因（可选）</label>
              <Input value={reason} onChange={e => setReason(e.target.value)} placeholder="例如：测试授权、VIP体验（可选）" />
            </div>
            <div>
              <label className="node-scope-field-label">开始时间（可选）</label>
              <DateTimePicker value={startsAt} onChange={setStartsAt} placeholder="立即生效" aria-label="开始时间" />
            </div>
            <div>
              <label className="node-scope-field-label">过期时间（可选）</label>
              <DateTimePicker value={expiresAt} onChange={setExpiresAt} placeholder="永久有效" aria-label="过期时间" />
            </div>
          </div>

          {exMessage && (
            <p style={{ margin: 0, fontSize: 12, color: exMessage.includes('失败') ? 'var(--color-danger)' : 'var(--color-success, #16a34a)' }}>
              {exMessage}
            </p>
          )}

          {exPreview && (
            <div className="node-scope-auth-preview">
              <p className="muted" style={{ margin: 0 }}>
                将创建 <strong>{exPreview.created}</strong> 条、更新 <strong>{exPreview.updated}</strong> 条、跳过已有 <strong>{exPreview.skipped}</strong> 条 · 受影响用户 <strong>{exPreview.affected_users}</strong> 人
              </p>
            </div>
          )}

          <div className="node-scope-auth-actions">
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
