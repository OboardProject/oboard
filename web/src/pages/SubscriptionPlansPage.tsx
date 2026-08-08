import * as React from 'react'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Select } from '../components/ui/select'
import { Input } from '../components/ui/input'
import { Plus, Trash2, RefreshCw, RotateCcw, Ban, Copy, X, Eye, Edit3 } from 'lucide-react'
import { FormField, TrafficLimitInput } from '../components/ui/form-field'
import { PlanNodeOrderingPanel, type OrderingPlan } from '../components/node-ordering/PlanNodeOrderingPanel'
import { Skeleton } from '../components/ui/skeleton'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }

type Plan = {
  id: number
  name: string
  description: string
  enabled: boolean
  revision: number
  lock_version: number
  current_revision_id: number
  latest_revision_id: number
  pending_revision_id?: number
  latest_version_no?: number
  node_count?: number
  member_count?: number
  active_revision_id?: number
  draft_revision_id?: number
  speed_limit_mbps: number
  traffic_limit_bytes: number
  traffic_reset_mode: string
  traffic_reset_day: number
  created_at?: string
  updated_at?: string
}

type PlanNode = { node_type: string; node_id: number; display_group?: string; name?: string }
type Revision = { id: number; revision: number; version_no: number; status: string; change_kind?: string; change_summary?: string; speed_limit_mbps: number; traffic_limit_bytes: number; traffic_reset_mode: string; traffic_reset_day: number; activated_at?: string; created_at: string }
type AccessChange = {
  id: number
  change_type: string
  source_plan_id?: number
  status: 'preparing' | 'activating' | 'finalizing' | 'finalized' | 'failed' | 'cancelled'
  affected_user_count: number
  activate_at?: string
  error?: string
  created_at: string
  activated_at?: string
  finalized_at?: string
  failed_at?: string
  targets: { server_id: number; prepare_task_id?: number; finalize_task_id?: number; status: string; error?: string }[]
}

type CatalogNode = { type: string; id: number; key: string; name: string; entry_server_name?: string; entry_protocol?: string; exit_region?: string; status: string }

const changeTypeLabels: Record<string, string> = {
  plan_publish: '方案发布',
  plan_restore: '版本回滚',
  plan_disable: '方案停用',
  user_bindings: '用户换绑',
  exceptions: '节点例外',
}
const changeStatusLabels: Record<string, { label: string; variant: 'secondary' | 'success' | 'warning' | 'destructive' | 'outline' }> = {
  preparing: { label: '准备中', variant: 'secondary' },
  activating: { label: '激活中', variant: 'warning' },
  finalizing: { label: '收尾中', variant: 'warning' },
  finalized: { label: '已完成', variant: 'success' },
  failed: { label: '失败', variant: 'destructive' },
  cancelled: { label: '已取消', variant: 'outline' },
}
const changeKindLabels: Record<string, string> = {
  create: '创建',
  settings: '方案设置',
  nodes: '节点调整',
  ordering: '排序调整',
  mixed: '综合调整',
  restore: '版本恢复',
  clone: '复制',
  legacy_draft_migration: '草稿迁移',
}
const revisionStatusLabels: Record<string, { label: string; variant: 'success' | 'warning' | 'secondary' | 'outline' }> = {
  current: { label: '当前生效', variant: 'success' },
  latest: { label: '最新保存', variant: 'secondary' },
  applying: { label: '正在应用', variant: 'warning' },
  historical: { label: '历史版本', variant: 'outline' },
}

function fmtBytes(v: number) {
  if (!v || v <= 0) return '不限量'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let n = v
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return `${n.toFixed(n >= 100 ? 0 : 1)} ${units[i]}`
}

function fmtDate(v?: string) {
  if (!v) return '—'
  return new Date(v).toLocaleString()
}

function revisionStatus(r: Revision, plan: Plan) {
  if (r.id === plan.pending_revision_id) return 'applying'
  if (r.id === plan.current_revision_id) return 'current'
  if (r.id === plan.latest_revision_id) return 'latest'
  return 'historical'
}

function nodeKey(n: PlanNode) { return `${n.node_type}:${n.node_id}` }

export function SubscriptionPlansPage({ data, client, load, notify }: { data: any; client: AnyClient; load: () => Promise<void>; notify?: (message: string, tone?: any) => void }) {
  const [plans, setPlans] = React.useState<Plan[]>(data.subscription_plans || [])
  const [selectedID, setSelectedID] = React.useState<number>(0)
  const [detail, setDetail] = React.useState<any>(null)
  const [detailLoading, setDetailLoading] = React.useState(false)
  const [detailOpen, setDetailOpen] = React.useState(false)
  const [detailError, setDetailError] = React.useState('')
  const [tab, setTab] = React.useState<'overview' | 'nodes' | 'ordering' | 'history'>('overview')
  const [createOpen, setCreateOpen] = React.useState(false)
  const [createDraft, setCreateDraft] = React.useState({ name: '', description: '', enabled: true, speed_limit_mbps: 0, traffic_limit_bytes: 0, traffic_reset_mode: 'monthly', traffic_reset_day: 1 })
  const [createNodes, setCreateNodes] = React.useState<PlanNode[]>([])
  const [editOpen, setEditOpen] = React.useState(false)
  const [editDraft, setEditDraft] = React.useState({ name: '', description: '', enabled: true, speed_limit_mbps: 0, traffic_limit_bytes: 0, traffic_reset_mode: 'monthly', traffic_reset_day: 1 })
  const [pickerQuery, setPickerQuery] = React.useState('')
  const [pickerResults, setPickerResults] = React.useState<CatalogNode[]>([])
  const [pickerBusy, setPickerBusy] = React.useState(false)
  const [workingSettings, setWorkingSettings] = React.useState<Plan | null>(null)
  const [workingNodes, setWorkingNodes] = React.useState<PlanNode[]>([])
  const [nodeNames, setNodeNames] = React.useState<Record<string, string>>({})
  const [nodePreview, setNodePreview] = React.useState<{ preview: any; expected_lock_version: number; base_revision_id: number; node_count: number } | null>(null)
  const [nodeBusy, setNodeBusy] = React.useState(false)
  const [nodeApplyBusy, setNodeApplyBusy] = React.useState(false)
  const [saveBusy, setSaveBusy] = React.useState(false)
  const [changes, setChanges] = React.useState<AccessChange[]>([])
  const [message, setMessage] = React.useState('')
  const [viewRevision, setViewRevision] = React.useState<any>(null)
  const [viewBusy, setViewBusy] = React.useState(false)

  const refreshPlans = async () => {
    const res = await client.request<{ subscription_plans: Plan[] }>('/subscription-plans')
    setPlans(res.subscription_plans || [])
  }

  const loadDetail = React.useCallback(async (id: number) => {
    setDetailError('')
    setDetailLoading(true)
    try {
      const [res, catalog] = await Promise.all([
        client.request<any>(`/subscription-plans/${id}`),
        client.request<{ nodes: CatalogNode[] }>('/assignable-nodes?page=1&page_size=200'),
      ])
      const names: Record<string, string> = {}
      for (const n of catalog.nodes || []) names[`${n.type}:${n.id}`] = n.name
      setNodeNames(names)
      setDetail(res)
      setWorkingSettings(res.subscription_plan)
      setWorkingNodes((res.latest_nodes || []).map((n: any) => ({
        node_type: n.node_type,
        node_id: n.node_id,
        display_group: n.display_group || '',
        name: names[`${n.node_type}:${n.node_id}`] || `${n.node_type}:${n.node_id}`,
      })))
      setNodePreview(null)
    } catch (e: any) {
      setDetailError(e?.message || String(e))
    } finally {
      setDetailLoading(false)
    }
  }, [client])

  const loadChanges = React.useCallback(async () => {
    try {
      const res = await client.request<{ access_changes: AccessChange[] }>('/access-changes?limit=50')
      setChanges(res.access_changes || [])
    } catch { /* 访问变更列表失败时静默，页面主体不受影响 */ }
  }, [client])

  React.useEffect(() => {
    void refreshPlans()
    void loadChanges()
  }, [])
  React.useEffect(() => {
    if (selectedID && detailOpen) void loadDetail(selectedID)
  }, [selectedID, detailOpen, loadDetail])

  const selectPlan = (id: number) => {
    setSelectedID(id)
    setDetail(null)
    setNodePreview(null)
    setMessage('')
    setTab('overview')
    setDetailOpen(true)
  }

  const closeDetail = () => {
    setDetailOpen(false)
    setSelectedID(0)
    setDetail(null)
    setNodePreview(null)
    setMessage('')
  }

  const openCreate = () => {
    setPickerPlanMode('create')
    setCreateDraft({ name: '', description: '', enabled: true, speed_limit_mbps: 0, traffic_limit_bytes: 0, traffic_reset_mode: 'monthly', traffic_reset_day: 1 })
    setCreateNodes([])
    setPickerQuery('')
    setPickerResults([])
    setCreateOpen(true)
    void runPickerSearch('')
  }

  const openEdit = () => {
    if (!plan) return
    setEditDraft({
      name: plan.name || '',
      description: plan.description || '',
      enabled: plan.enabled ?? true,
      speed_limit_mbps: plan.speed_limit_mbps || 0,
      traffic_limit_bytes: plan.traffic_limit_bytes || 0,
      traffic_reset_mode: plan.traffic_reset_mode || 'monthly',
      traffic_reset_day: plan.traffic_reset_day || 1,
    })
    setEditOpen(true)
  }

  const runPickerSearch = async (query: string) => {
    setPickerBusy(true)
    try {
      const params = new URLSearchParams({ page: '1', page_size: '200' })
      if (query) params.set('query', query)
      const res = await client.request<{ nodes: CatalogNode[] }>('/assignable-nodes?' + params.toString())
      setPickerResults(res.nodes || [])
    } catch (e: any) {
      setMessage('节点搜索失败：' + (e?.message || String(e)))
    } finally {
      setPickerBusy(false)
    }
  }

  const togglePickerNode = (n: CatalogNode) => {
    if (pickerPlanMode === 'create') {
      setCreateNodes(list => {
        const exists = list.some(x => x.node_type === n.type && x.node_id === n.id)
        return exists ? list.filter(x => !(x.node_type === n.type && x.node_id === n.id)) : [...list, { node_type: n.type, node_id: n.id, name: n.name }]
      })
      return
    }
    setWorkingNodes(list => {
      const exists = list.some(x => x.node_type === n.type && x.node_id === n.id)
      return exists ? list.filter(x => !(x.node_type === n.type && x.node_id === n.id)) : [...list, { node_type: n.type, node_id: n.id, display_group: '', name: n.name }]
    })
    setNodePreview(null)
  }

  const createPlan = async () => {
    if (!createDraft.name.trim()) { setMessage('请输入方案名称'); return }
    setMessage('')
    try {
      await client.request('/subscription-plans', { method: 'POST', body: JSON.stringify({ ...createDraft, nodes: createNodes.map(n => ({ node_type: n.node_type, node_id: n.node_id })) }) })
      setCreateOpen(false)
      await refreshPlans()
      notify?.('方案已创建', 'success')
    } catch (e: any) {
      setMessage('创建失败：' + (e?.message || String(e)))
    }
  }

  const saveSettings = async () => {
    if (!detail || !editDraft) return
    setSaveBusy(true)
    setMessage('')
    try {
      await client.request(`/subscription-plans/${selectedID}`, {
        method: 'PATCH',
        body: JSON.stringify({
          expected_revision: detail.subscription_plan.lock_version || detail.subscription_plan.revision,
          name: editDraft.name,
          description: editDraft.description,
          enabled: editDraft.enabled,
          speed_limit_mbps: editDraft.speed_limit_mbps,
          traffic_limit_bytes: editDraft.traffic_limit_bytes,
          traffic_reset_mode: editDraft.traffic_reset_mode,
          traffic_reset_day: editDraft.traffic_reset_day,
        }),
      })
      setEditOpen(false)
      await loadDetail(selectedID)
      await refreshPlans()
      notify?.('方案信息已保存', 'success')
    } catch (e: any) {
      const err = e?.message || String(e)
      setMessage(err.includes('conflict') || err.includes('409') ? '保存失败：方案已发生变化（冲突），请重新加载后重试' : '保存失败：' + err)
    } finally {
      setSaveBusy(false)
    }
  }

  const runNodePreview = async () => {
    if (!detail) return
    setNodeBusy(true)
    setMessage('')
    try {
      const res = await client.request<{ preview: any; expected_lock_version: number; base_revision_id: number; node_count: number }>(`/subscription-plans/${selectedID}/nodes/preview`, {
        method: 'POST',
        body: JSON.stringify({ op: 'replace', nodes: workingNodes.map(n => ({ node_type: n.node_type, node_id: n.node_id, display_group: n.display_group || '' })) }),
      })
      setNodePreview(res)
    } catch (e: any) {
      setMessage('预览失败：' + (e?.message || String(e)))
    } finally {
      setNodeBusy(false)
    }
  }

  const applyNodeChange = async () => {
    if (!detail || !nodePreview) return
    setNodeApplyBusy(true)
    setMessage('')
    try {
      const res = await client.request<{ access_change_id?: number; no_change?: boolean }>(`/subscription-plans/${selectedID}/nodes/apply`, {
        method: 'POST',
        body: JSON.stringify({
          op: 'replace',
          nodes: workingNodes.map(n => ({ node_type: n.node_type, node_id: n.node_id, display_group: n.display_group || '' })),
          base_revision_id: nodePreview.base_revision_id,
          expected_lock_version: nodePreview.expected_lock_version,
          change_summary: '调整方案节点集合',
        }),
      })
      setNodePreview(null)
      if (res.no_change) {
        notify?.('节点集合没有变化，未创建新版本', 'warning')
      } else if (res.access_change_id) {
        notify?.(`已保存为新版本，正在应用变更 #${res.access_change_id}`, 'success')
      } else {
        notify?.('已保存为新版本', 'success')
      }
      await loadDetail(selectedID)
      await refreshPlans()
      await loadChanges()
    } catch (e: any) {
      const err = e?.message || String(e)
      setMessage(err.includes('conflict') || err.includes('409') ? '保存失败：方案已发生变化（冲突），请重新加载后重试' : '保存失败：' + err)
    } finally {
      setNodeApplyBusy(false)
    }
  }

  const disablePlan = async () => {
    if (!detail) return
    setMessage('')
    try {
      const res = await client.request<any>(`/subscription-plans/${selectedID}/disable`, {
        method: 'POST',
        body: JSON.stringify({ expected_revision: detail.subscription_plan.lock_version || detail.subscription_plan.revision }),
      })
      if (res.access_change_id) {
        await loadChanges()
        notify?.(`停用已开始（变更 #${res.access_change_id}）`, 'success')
      } else {
        notify?.('方案已停用', 'success')
      }
      await loadDetail(selectedID)
      await refreshPlans()
    } catch (e: any) {
      setMessage('停用失败：' + (e?.message || String(e)))
    }
  }

  const clonePlan = async () => {
    setMessage('')
    try {
      await client.request(`/subscription-plans/${selectedID}/clone`, { method: 'POST', body: '{}' })
      await refreshPlans()
      notify?.('已创建副本', 'success')
    } catch (e: any) {
      setMessage('复制失败：' + (e?.message || String(e)))
    }
  }

  const restoreRevision = async (revisionID: number) => {
    if (!detail) return
    setMessage('')
    try {
      const res = await client.request<any>(`/subscription-plans/${selectedID}/revisions/${revisionID}/restore`, {
        method: 'POST',
        body: JSON.stringify({ expected_lock_version: detail.subscription_plan.lock_version || detail.subscription_plan.revision, change_summary: '基于历史版本恢复' }),
      })
      if (res.access_change_id) {
        await loadChanges()
        notify?.(`已创建恢复版本，正在应用变更 #${res.access_change_id}`, 'success')
      } else {
        notify?.(`已基于 V${res.version_no} 创建新版本`, 'success')
      }
      await loadDetail(selectedID)
      await refreshPlans()
    } catch (e: any) {
      const err = e?.message || String(e)
      setMessage(err.includes('conflict') || err.includes('409') ? '回滚失败：方案已发生变化（冲突），请重新加载后重试' : '回滚失败：' + err)
    }
  }

  const openRevision = async (revisionID: number) => {
    setViewBusy(true)
    setMessage('')
    try {
      const res = await client.request<any>(`/subscription-plans/${selectedID}/revisions/${revisionID}`)
      setViewRevision(res)
    } catch (e: any) {
      setMessage('查看失败：' + (e?.message || String(e)))
    } finally {
      setViewBusy(false)
    }
  }

  const retryChange = async (id: number) => {
    try {
      await client.request(`/access-changes/${id}/retry`, { method: 'POST', body: '{}' })
      await loadChanges()
      notify?.(`变更 #${id} 已重试`, 'success')
    } catch (e: any) {
      setMessage('重试失败：' + (e?.message || String(e)))
    }
  }

  const cancelChange = async (id: number) => {
    try {
      await client.request(`/access-changes/${id}/cancel`, { method: 'POST', body: '{}' })
      await loadChanges()
      notify?.(`变更 #${id} 已取消`, 'success')
    } catch (e: any) {
      setMessage('取消失败：' + (e?.message || String(e)))
    }
  }

  const plan = detail?.subscription_plan as Plan | undefined
  const planChanges = changes.filter(c => c.source_plan_id === selectedID)
  const applying = Boolean(plan?.pending_revision_id)
  const orderingPlan: OrderingPlan | null = plan ? {
    id: plan.id,
    name: plan.name,
    lock_version: plan.lock_version,
    current_revision_id: plan.current_revision_id,
    latest_revision_id: plan.latest_revision_id,
    pending_revision_id: plan.pending_revision_id,
  } : null
  const [pickerPlanMode, setPickerPlanMode] = React.useState<'create' | 'nodes'>('create')
  const [pickerOpen, setPickerOpen] = React.useState(false)

  return (
    <div className="panel subscription-plans-panel">
      <div className="panel-head"><h2>订阅方案</h2></div>
      <div className="panel-body">
        <p className="muted">方案定义节点的可分配集合与速度/流量限额。每次保存都会创建一个不可变版本：排序修改立即生效，节点或限额变化先进入待应用状态，再由“准备 → 激活 → 收尾”两阶段下发。</p>

        <div className="section-toolbar">
          <div><h3>方案列表</h3><p className="muted">共 {plans.length} 个方案。</p></div>
          <Button onClick={openCreate}><Plus size={14} /> 新建方案</Button>
        </div>

        <div className="card-custom" style={{ overflow: 'auto', marginBottom: 16 }}>
          <table className="user-data-table" style={{ minWidth: 780 }}>
            <thead>
              <tr>
                <th>名称</th><th>状态</th><th>版本</th><th>节点</th><th>限速</th><th>流量</th><th style={{ textAlign: 'right' }}>操作</th>
              </tr>
            </thead>
            <tbody>
              {plans.map(p => (
                <tr key={p.id} className="table-row-hover" style={{ backgroundColor: p.id === selectedID && detailOpen ? 'var(--bg-hover, rgba(0,0,0,0.03))' : 'transparent' }}>
                  <td style={{ fontWeight: 600 }}>{p.name}</td>
                  <td>
                    <Badge variant={p.enabled ? 'success' : 'secondary'}>{p.enabled ? '启用' : '已停用'}</Badge>
                    {p.pending_revision_id ? <Badge variant="warning" style={{ marginLeft: 4 }}>正在应用</Badge> : null}
                  </td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>V{p.latest_version_no || '—'}</td>
                  <td>{p.node_count ?? '—'}</td>
                  <td>{p.speed_limit_mbps > 0 ? `${p.speed_limit_mbps} Mbps` : '不限'}</td>
                  <td>{fmtBytes(p.traffic_limit_bytes)}</td>
                  <td style={{ textAlign: 'right' }}>
                    <Button variant="outline" size="sm" onClick={() => selectPlan(p.id)}>打开</Button>
                  </td>
                </tr>
              ))}
              {plans.length === 0 && <tr><td colSpan={7} className="muted" style={{ textAlign: 'center', padding: 24 }}>还没有方案，点击“新建方案”开始。</td></tr>}
            </tbody>
          </table>
        </div>
      </div>

      <Dialog isOpen={detailOpen && selectedID > 0} onClose={closeDetail} title={plan ? `方案详情：${plan.name}` : '方案详情'} size="xl">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14, maxHeight: 'calc(85vh - 80px)', overflow: 'auto', paddingRight: 4 }}>
          {detailError && <p style={{ color: 'var(--color-danger)', margin: 0 }}>{detailError}</p>}
          {message && <p style={{ color: message.includes('失败') ? 'var(--color-danger)' : 'var(--color-success, #16a34a)', margin: 0 }}>{message}</p>}

          {detailLoading && (
            <div className="animate-page-in" style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
              <div className="section-toolbar" style={{ flexWrap: 'wrap', gap: 8 }}>
                <div>
                  <Skeleton className="skeleton-line" style={{ width: 140, height: 24, marginBottom: 6 }} />
                  <Skeleton className="skeleton-line" style={{ width: 260, height: 16 }} />
                </div>
                <div style={{ display: 'flex', gap: 6, marginLeft: 'auto' }}>
                  <Skeleton className="skeleton-line" style={{ width: 88, height: 32 }} />
                  <Skeleton className="skeleton-line" style={{ width: 64, height: 32 }} />
                  <Skeleton className="skeleton-line" style={{ width: 64, height: 32 }} />
                </div>
              </div>
              <div className="section-toolbar" style={{ gap: 4, marginTop: 4 }}>
                <Skeleton className="skeleton-line" style={{ width: 56, height: 28 }} />
                <Skeleton className="skeleton-line" style={{ width: 56, height: 28 }} />
                <Skeleton className="skeleton-line" style={{ width: 56, height: 28 }} />
                <Skeleton className="skeleton-line" style={{ width: 80, height: 28 }} />
              </div>
              <div className="plan-info-grid" style={{ marginTop: 10 }}>
                {[1, 2, 3, 4, 5, 6].map(i => (
                  <div key={i} className="plan-info-item">
                    <Skeleton className="skeleton-line" style={{ width: '40%', height: 14 }} />
                    <Skeleton className="skeleton-line" style={{ width: '75%', height: 20 }} />
                  </div>
                ))}
              </div>
            </div>
          )}

          {!detailLoading && plan && detail && (
            <div className="animate-plan-detail-in" style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
              <div className="section-toolbar" style={{ flexWrap: 'wrap', gap: 8 }}>
                <div>
                  <h3 style={{ margin: 0 }}>{plan.name}</h3>
                  <p className="muted" style={{ margin: '4px 0 0' }}>
                    {detail.member_count} 个绑定用户 · {detail.latest_nodes?.length || 0} 个节点 · 最新 V{plan.latest_version_no || '—'}
                    {plan.current_revision_id === plan.latest_revision_id && !plan.pending_revision_id ? ' · 当前生效' : ''}
                    {applying ? ' · 有版本正在应用' : ''}
                  </p>
                </div>
                <div style={{ display: 'flex', gap: 6, marginLeft: 'auto' }}>
                  <Button variant="outline" size="sm" onClick={openEdit}><Edit3 size={14} /> 修改方案</Button>
                  <Button variant="outline" size="sm" onClick={() => void clonePlan()}><Copy size={14} /> 复制</Button>
                  {plan.enabled && <Button variant="outline" size="sm" onClick={() => void disablePlan()}><Ban size={14} /> 停用</Button>}
                </div>
              </div>

              <div className="section-toolbar" style={{ gap: 4, marginTop: 4 }}>
                {([['overview', '概览'], ['nodes', '节点'], ['ordering', '排序'], ['history', '版本历史']] as const).map(([key, label]) => (
                  <Button key={key} variant={tab === key ? 'default' : 'outline'} size="sm" onClick={() => setTab(key)}>{label}</Button>
                ))}
              </div>

              {tab === 'overview' && (
                <div className="animate-page-in" style={{ marginTop: 8 }}>
                  <div className="plan-info-grid">
                    <div className="plan-info-item">
                      <span className="label">方案名称</span>
                      <span className="value">{plan.name}</span>
                    </div>
                    <div className="plan-info-item">
                      <span className="label">状态</span>
                      <span className="value">
                        <Badge variant={plan.enabled ? 'success' : 'secondary'}>{plan.enabled ? '启用' : '已停用'}</Badge>
                        {plan.pending_revision_id ? <Badge variant="warning" style={{ marginLeft: 4 }}>正在应用</Badge> : null}
                      </span>
                    </div>
                    <div className="plan-info-item">
                      <span className="label">速度上限</span>
                      <span className="value">{plan.speed_limit_mbps > 0 ? `${plan.speed_limit_mbps} Mbps` : '不限速'}</span>
                    </div>
                    <div className="plan-info-item">
                      <span className="label">流量额度</span>
                      <span className="value">{fmtBytes(plan.traffic_limit_bytes)}</span>
                    </div>
                    <div className="plan-info-item">
                      <span className="label">重置方式</span>
                      <span className="value">
                        {plan.traffic_reset_mode === 'monthly' ? `每月重置（第 ${plan.traffic_reset_day} 日）` : '不重置'}
                      </span>
                    </div>
                    <div className="plan-info-item">
                      <span className="label">版本状态</span>
                      <span className="value">
                        V{plan.latest_version_no || '—'}
                        <span className="muted" style={{ fontSize: 12, fontWeight: 400 }}>
                          {plan.current_revision_id === plan.latest_revision_id && !plan.pending_revision_id ? '（当前生效）' : ''}
                        </span>
                      </span>
                    </div>
                    <div className="plan-info-item" style={{ gridColumn: '1 / -1' }}>
                      <span className="label">描述</span>
                      <span className="value" style={{ fontWeight: 400, color: plan.description ? 'var(--text-strong)' : 'var(--muted)' }}>
                        {plan.description || '无备注说明'}
                      </span>
                    </div>
                  </div>

                  <div style={{ marginTop: 16, display: 'flex', justifyContent: 'flex-end' }}>
                    <Button onClick={openEdit}><Edit3 size={14} /> 修改方案信息</Button>
                  </div>
                </div>
              )}

              {tab === 'nodes' && (
                <div className="animate-page-in" style={{ marginTop: 8, display: 'flex', flexDirection: 'column', gap: 10 }}>
                  {applying && <p style={{ color: 'var(--color-warning)', margin: 0 }}>有方案版本正在应用，应用完成前不能保存新的节点版本。</p>}
                  <div className="section-toolbar">
                    <div><h3 style={{ margin: 0 }}>节点集合（{workingNodes.length}）</h3><p className="muted">基于最新保存版本编辑；保存后创建不可变新版本，节点变化会走两阶段下发。</p></div>
                    <Button variant="outline" size="sm" onClick={() => { setPickerPlanMode('nodes'); setPickerOpen(true); setPickerQuery(''); setPickerResults([]); setMessage(''); void runPickerSearch('') }} disabled={applying}><Plus size={14} /> 添加节点</Button>
                  </div>
                  <table className="user-data-table" style={{ width: '100%' }}>
                    <thead><tr><th>节点</th><th>类型</th><th>分组</th><th style={{ textAlign: 'right' }}>操作</th></tr></thead>
                    <tbody>
                      {workingNodes.map(n => (
                        <tr key={nodeKey(n)}>
                          <td style={{ fontWeight: 600 }}>{n.name || nodeKey(n)}</td>
                          <td className="muted" style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>{n.node_type}</td>
                          <td>
                            <Input value={n.display_group || ''} onChange={e => { setWorkingNodes(list => list.map(x => nodeKey(x) === nodeKey(n) ? { ...x, display_group: e.target.value } : x)); setNodePreview(null) }} placeholder="展示分组（可选）" style={{ maxWidth: 160 }} />
                          </td>
                          <td style={{ textAlign: 'right' }}>
                            <Button variant="ghost" size="sm" onClick={() => { setWorkingNodes(list => list.filter(x => nodeKey(x) !== nodeKey(n))); setNodePreview(null) }}><Trash2 size={14} /></Button>
                          </td>
                        </tr>
                      ))}
                      {workingNodes.length === 0 && <tr><td colSpan={4} className="muted" style={{ textAlign: 'center', padding: 16 }}>节点集合为空</td></tr>}
                    </tbody>
                  </table>
                  <div style={{ display: 'flex', gap: 8 }}>
                    <Button size="sm" variant="outline" busy={nodeBusy} onClick={() => void runNodePreview()} disabled={applying}><RefreshCw size={14} /> 预览影响</Button>
                    {nodePreview && (
                      <>
                        <Button size="sm" busy={nodeApplyBusy} onClick={() => void applyNodeChange()} disabled={applying}>确认保存为新版本</Button>
                        <Button size="sm" variant="ghost" onClick={() => setNodePreview(null)}>取消</Button>
                      </>
                    )}
                  </div>
                  {nodePreview && (
                    <p className="muted" style={{ margin: 0 }}>
                      新版本将为 {nodePreview.node_count} 个节点 · 新增 {nodePreview.preview?.nodes_added?.length || 0} · 移除 {nodePreview.preview?.nodes_removed?.length || 0} · 不变 {nodePreview.preview?.nodes_unchanged || 0} · 受影响用户 {nodePreview.preview?.users_affected || 0} · 任务 {nodePreview.preview?.task_count || 0}
                    </p>
                  )}
                </div>
              )}

              {tab === 'ordering' && orderingPlan && (
                <div className="animate-page-in" style={{ marginTop: 8 }}>
                  <PlanNodeOrderingPanel plan={orderingPlan} data={data} client={client} notify={notify} onSaved={() => { void loadDetail(selectedID); void refreshPlans() }} />
                </div>
              )}

              {tab === 'history' && (
                <div className="animate-page-in" style={{ marginTop: 8, display: 'flex', flexDirection: 'column', gap: 14 }}>
                  <div>
                    <h3 style={{ marginTop: 0 }}>版本历史</h3>
                    <table className="user-data-table" style={{ width: '100%' }}>
                      <thead><tr><th>版本</th><th>状态</th><th>类型</th><th>摘要</th><th>限速</th><th>流量</th><th>保存时间</th><th style={{ textAlign: 'right' }}>操作</th></tr></thead>
                      <tbody>
                        {(detail.revisions || []).map((r: Revision) => {
                          const st = revisionStatusLabels[revisionStatus(r, plan)] || revisionStatusLabels.historical
                          return (
                            <tr key={r.id}>
                              <td style={{ fontFamily: 'var(--font-mono)' }}>V{r.version_no || r.revision}</td>
                              <td><Badge variant={st.variant}>{st.label}</Badge></td>
                              <td>{changeKindLabels[r.change_kind || ''] || r.change_kind || '—'}</td>
                              <td className="muted">{r.change_summary || '—'}</td>
                              <td>{r.speed_limit_mbps > 0 ? `${r.speed_limit_mbps} Mbps` : '不限'}</td>
                              <td>{fmtBytes(r.traffic_limit_bytes)}</td>
                              <td className="muted">{fmtDate(r.created_at)}</td>
                              <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                                <Button variant="ghost" size="sm" busy={viewBusy} onClick={() => void openRevision(r.id)}><Eye size={14} /> 查看</Button>
                                <Button variant="outline" size="sm" disabled={r.id === plan.latest_revision_id} onClick={() => void restoreRevision(r.id)}><RotateCcw size={14} /> 基于此版本恢复</Button>
                              </td>
                            </tr>
                          )
                        })}
                      </tbody>
                    </table>
                  </div>

                  <div>
                    <div className="section-toolbar">
                      <div><h3 style={{ margin: 0 }}>部署变更</h3><p className="muted">方案相关的两阶段下发记录。</p></div>
                      <Button variant="ghost" size="sm" onClick={() => void loadChanges()}><RefreshCw size={14} /></Button>
                    </div>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 10, maxHeight: 360, overflow: 'auto', marginTop: 8 }}>
                      {planChanges.length === 0 && <p className="muted">暂无变更记录</p>}
                      {planChanges.map(c => {
                        const st = changeStatusLabels[c.status] || { label: c.status, variant: 'outline' as const }
                        return (
                          <div key={c.id} className="card-custom" style={{ padding: 12 }}>
                            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                              <span style={{ fontWeight: 600 }}>#{c.id} {changeTypeLabels[c.change_type] || c.change_type}</span>
                              <Badge variant={st.variant}>{st.label}</Badge>
                              <span className="muted" style={{ fontSize: 12 }}>{c.affected_user_count} 用户 · {fmtDate(c.created_at)}</span>
                            </div>
                            {c.error && <p style={{ color: 'var(--color-danger)', fontSize: 12, margin: '6px 0 0' }}>{c.error}</p>}
                            {(c.targets || []).length > 0 && (
                              <div style={{ marginTop: 6, display: 'flex', flexDirection: 'column', gap: 4 }}>
                                {c.targets.map(t => (
                                  <div key={t.server_id} style={{ fontSize: 12, display: 'flex', justifyContent: 'space-between', gap: 8 }}>
                                    <span className="muted">服务器 #{t.server_id}</span>
                                    <span>{t.status}</span>
                                  </div>
                                ))}
                              </div>
                            )}
                            {(c.status === 'failed') && <Button variant="outline" size="sm" style={{ marginTop: 8 }} onClick={() => void retryChange(c.id)}>重试</Button>}
                            {(c.status === 'preparing' || c.status === 'activating') && <Button variant="ghost" size="sm" style={{ marginTop: 8 }} onClick={() => void cancelChange(c.id)}>取消</Button>}
                          </div>
                        )
                      })}
                    </div>
                  </div>
                </div>
              )}
            </div>
          )}
        </div>
      </Dialog>

      <Dialog isOpen={createOpen} onClose={() => setCreateOpen(false)} title="新建方案" size="lg">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <p className="muted" style={{ margin: 0 }}>方案定义可分配节点与速度/流量限额；创建后保存为第一个版本 V1。</p>
          <form id="create-plan-form" className="form" onSubmit={e => { e.preventDefault(); void createPlan() }}>
            <FormField label="名称" required><Input value={createDraft.name} onChange={e => setCreateDraft(d => ({ ...d, name: e.target.value }))} placeholder="例如：标准方案" /></FormField>
            <FormField label="描述"><Input value={createDraft.description} onChange={e => setCreateDraft(d => ({ ...d, description: e.target.value }))} placeholder="可选" /></FormField>
            <FormField label="速度上限" hint="0 表示不限速。">
              <div className="input-with-unit"><Input type="number" min={0} value={createDraft.speed_limit_mbps} onChange={e => setCreateDraft(d => ({ ...d, speed_limit_mbps: Number(e.target.value) }))} /><span>Mbps</span></div>
            </FormField>
            <FormField label="流量额度" hint="0 表示不限量。"><TrafficLimitInput bytes={createDraft.traffic_limit_bytes} onChange={v => setCreateDraft(d => ({ ...d, traffic_limit_bytes: v }))} /></FormField>
            <FormField label="重置方式">
              <Select value={createDraft.traffic_reset_mode} onChange={e => setCreateDraft(d => ({ ...d, traffic_reset_mode: e.target.value }))}>
                <option value="monthly">每月重置</option><option value="never">不重置</option>
              </Select>
            </FormField>
            <FormField label="重置日">
              <div className="input-with-unit"><Input type="number" min={1} max={31} value={createDraft.traffic_reset_day} onChange={e => setCreateDraft(d => ({ ...d, traffic_reset_day: Number(e.target.value) }))} /><span>日</span></div>
            </FormField>
          </form>
          <div>
            <div className="section-toolbar">
              <div><h3 style={{ margin: 0 }}>初始节点（{createNodes.length}）</h3><p className="muted">选择该方案可分配的节点；可留空，创建后继续添加。</p></div>
            </div>
            <div style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
              <Input value={pickerQuery} onChange={e => setPickerQuery(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') void runPickerSearch(pickerQuery) }} placeholder="搜索节点名称、协议或地区" />
              <Button variant="outline" size="sm" style={{ whiteSpace: 'nowrap' }} busy={pickerBusy} onClick={() => void runPickerSearch(pickerQuery)}>搜索</Button>
            </div>
            {createNodes.length > 0 && (
              <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', marginBottom: 8 }}>
                {createNodes.map(n => (
                  <Badge key={nodeKey(n)} variant="secondary">
                    {n.name || nodeKey(n)}
                    <button type="button" className="ghost icon-button" style={{ width: 16, height: 16, minHeight: 16, minWidth: 16 }} aria-label={`移除 ${n.name || nodeKey(n)}`} onClick={() => togglePickerNode({ type: n.node_type, id: n.node_id, key: nodeKey(n), name: n.name || '', status: '' })}><X size={12} /></button>
                  </Badge>
                ))}
              </div>
            )}
            <div className="card-custom" style={{ maxHeight: 240, overflow: 'auto' }}>
              {pickerResults.map(n => {
                const exists = createNodes.some(x => nodeKey(x) === n.key)
                return (
                  <label key={n.key} style={{ display: 'flex', gap: 8, alignItems: 'center', padding: '6px 8px', cursor: 'pointer' }}>
                    <input type="checkbox" checked={exists} onChange={() => togglePickerNode(n)} />
                    <span style={{ fontWeight: 600 }}>{n.name}</span>
                    <span className="muted" style={{ fontSize: 12 }}>{n.entry_protocol || ''} {n.exit_region ? `· ${n.exit_region}` : ''}</span>
                  </label>
                )
              })}
              {pickerResults.length === 0 && !pickerBusy && <p className="muted" style={{ padding: 12 }}>没有匹配的节点，可留空稍后添加。</p>}
            </div>
          </div>
          {message && <p style={{ color: 'var(--color-danger)' }}>{message}</p>}
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>取消</Button>
            <Button disabled={!createDraft.name.trim()} type="submit" form="create-plan-form">创建方案</Button>
          </div>
        </div>
      </Dialog>

      <Dialog isOpen={editOpen} onClose={() => setEditOpen(false)} title={`修改方案：${plan?.name || ''}`} size="lg">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <p className="muted" style={{ margin: 0 }}>修改方案的基础配置信息。保存后会更新方案配置。</p>
          <form id="edit-plan-form" className="form" onSubmit={e => { e.preventDefault(); void saveSettings() }}>
            <FormField label="名称" required>
              <Input value={editDraft.name} onChange={e => setEditDraft(d => ({ ...d, name: e.target.value }))} placeholder="例如：标准方案" />
            </FormField>
            <FormField label="描述" hint="仅管理端可见的备注。">
              <Input value={editDraft.description} onChange={e => setEditDraft(d => ({ ...d, description: e.target.value }))} placeholder="可选" />
            </FormField>
            <FormField label="速度上限" hint="0 表示不限速。">
              <div className="input-with-unit">
                <Input type="number" min={0} value={editDraft.speed_limit_mbps} onChange={e => setEditDraft(d => ({ ...d, speed_limit_mbps: Number(e.target.value) }))} />
                <span>Mbps</span>
              </div>
            </FormField>
            <FormField label="流量额度" hint="0 表示不限量，按重置周期统计。">
              <TrafficLimitInput bytes={editDraft.traffic_limit_bytes} onChange={v => setEditDraft(d => ({ ...d, traffic_limit_bytes: v }))} />
            </FormField>
            <FormField label="重置方式" hint="每月重置会按重置日清空已用流量。">
              <Select value={editDraft.traffic_reset_mode} onChange={e => setEditDraft(d => ({ ...d, traffic_reset_mode: e.target.value }))}>
                <option value="monthly">每月重置</option>
                <option value="never">不重置</option>
              </Select>
            </FormField>
            <FormField label="重置日" hint="1–31；仅重置方式为每月重置时生效。">
              <div className="input-with-unit">
                <Input type="number" min={1} max={31} disabled={editDraft.traffic_reset_mode !== 'monthly'} value={editDraft.traffic_reset_day} onChange={e => setEditDraft(d => ({ ...d, traffic_reset_day: Number(e.target.value) }))} />
                <span>日</span>
              </div>
            </FormField>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, marginTop: 4 }}>
              <input type="checkbox" id="edit-plan-enabled" checked={editDraft.enabled} onChange={e => setEditDraft(d => ({ ...d, enabled: e.target.checked }))} />
              <label htmlFor="edit-plan-enabled">启用方案</label>
            </div>
          </form>
          {message && <p style={{ color: 'var(--color-danger)' }}>{message}</p>}
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
            <Button variant="outline" onClick={() => setEditOpen(false)}>取消</Button>
            <Button disabled={!editDraft.name.trim()} busy={saveBusy} type="submit" form="edit-plan-form">保存修改</Button>
          </div>
        </div>
      </Dialog>

      <Dialog isOpen={pickerOpen} onClose={() => setPickerOpen(false)} title="添加节点" size="lg">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div style={{ display: 'flex', gap: 8 }}>
            <Input value={pickerQuery} onChange={e => setPickerQuery(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') void runPickerSearch(pickerQuery) }} placeholder="搜索节点..." />
            <Button variant="outline" size="sm" style={{ whiteSpace: 'nowrap' }} busy={pickerBusy} onClick={() => void runPickerSearch(pickerQuery)}>搜索</Button>
          </div>
          <div className="card-custom" style={{ maxHeight: 320, overflow: 'auto' }}>
            {pickerResults.map(n => {
              const exists = workingNodes.some(x => nodeKey(x) === n.key)
              return (
                <label key={n.key} style={{ display: 'flex', gap: 8, alignItems: 'center', padding: '6px 8px', cursor: 'pointer' }}>
                  <input type="checkbox" checked={exists} onChange={() => togglePickerNode(n)} />
                  <span style={{ fontWeight: 600 }}>{n.name}</span>
                  <span className="muted" style={{ fontSize: 12 }}>{n.entry_protocol || ''} {n.exit_region ? `· ${n.exit_region}` : ''}</span>
                </label>
              )
            })}
            {pickerResults.length === 0 && !pickerBusy && <p className="muted" style={{ padding: 12 }}>没有匹配的节点。</p>}
          </div>
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
            <Button variant="outline" onClick={() => setPickerOpen(false)}>关闭</Button>
            <Button onClick={() => setPickerOpen(false)}>完成</Button>
          </div>
        </div>
      </Dialog>

      <Dialog isOpen={viewRevision !== null} onClose={() => setViewRevision(null)} title={viewRevision ? `V${viewRevision.revision?.version_no || viewRevision.revision?.revision || ''} 详情` : ''} size="lg">
        {viewRevision && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <p className="muted" style={{ margin: 0 }}>
              {changeKindLabels[viewRevision.revision?.change_kind || ''] || '版本'} · {fmtDate(viewRevision.revision?.created_at)} · {viewRevision.nodes?.length || 0} 个节点
              {viewRevision.revision?.change_summary ? ` · ${viewRevision.revision.change_summary}` : ''}
            </p>
            <div className="card-custom" style={{ maxHeight: 360, overflow: 'auto' }}>
              <table className="user-data-table" style={{ width: '100%' }}>
                <thead><tr><th>节点</th><th>类型</th><th>分组</th></tr></thead>
                <tbody>
                  {(viewRevision.nodes || []).map((n: any) => (
                    <tr key={`${n.node_type}:${n.node_id}`}>
                      <td style={{ fontWeight: 600 }}>{nodeNames[`${n.node_type}:${n.node_id}`] || `${n.node_type}:${n.node_id}`}</td>
                      <td className="muted" style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>{n.node_type}</td>
                      <td className="muted">{n.display_group || '—'}</td>
                    </tr>
                  ))}
                  {(viewRevision.nodes || []).length === 0 && <tr><td colSpan={3} className="muted" style={{ textAlign: 'center', padding: 16 }}>该版本没有节点</td></tr>}
                </tbody>
              </table>
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
              <Button variant="outline" onClick={() => setViewRevision(null)}><X size={14} /> 关闭</Button>
            </div>
          </div>
        )}
      </Dialog>
    </div>
  )
}

