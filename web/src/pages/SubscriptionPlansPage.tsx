import * as React from 'react'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Select } from '../components/ui/select'
import { Input } from '../components/ui/input'
import { Plus, Trash2, RefreshCw, RotateCcw, Ban, Copy, GitCompareArrows } from 'lucide-react'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }

type Plan = {
  id: number
  name: string
  description: string
  enabled: boolean
  revision: number
  active_revision_id?: number
  draft_revision_id?: number
  speed_limit_mbps: number
  traffic_limit_bytes: number
  traffic_reset_mode: string
  traffic_reset_day: number
  created_at?: string
  updated_at?: string
}

type PlanNode = { node_type: string; node_id: number; display_group?: string }
type Revision = { id: number; revision: number; status: 'draft' | 'active' | 'archived' | string; speed_limit_mbps: number; traffic_limit_bytes: number; traffic_reset_mode: string; traffic_reset_day: number; activated_at?: string; created_at: string }
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

export function SubscriptionPlansPage({ data, client, load, notify }: { data: any; client: AnyClient; load: () => Promise<void>; notify?: (message: string, tone?: any) => void }) {
  const [plans, setPlans] = React.useState<Plan[]>(data.subscription_plans || [])
  const [selectedID, setSelectedID] = React.useState<number>(0)
  const [detail, setDetail] = React.useState<any>(null)
  const [detailError, setDetailError] = React.useState('')
  const [createOpen, setCreateOpen] = React.useState(false)
  const [createDraft, setCreateDraft] = React.useState({ name: '', description: '', enabled: true, speed_limit_mbps: 0, traffic_limit_bytes: 0, traffic_reset_mode: 'monthly', traffic_reset_day: 1 })
  const [createNodes, setCreateNodes] = React.useState<PlanNode[]>([])
  const [pickerQuery, setPickerQuery] = React.useState('')
  const [pickerResults, setPickerResults] = React.useState<CatalogNode[]>([])
  const [pickerPlanID, setPickerPlanID] = React.useState(0)
  const [pickerBusy, setPickerBusy] = React.useState(false)
  const [limitDraft, setLimitDraft] = React.useState<Plan | null>(null)
  const [publishPreview, setPublishPreview] = React.useState<any>(null)
  const [previewBusy, setPreviewBusy] = React.useState(false)
  const [applyBusy, setApplyBusy] = React.useState(false)
  const [syncBusy, setSyncBusy] = React.useState(false)
  const [changes, setChanges] = React.useState<AccessChange[]>([])
  const [message, setMessage] = React.useState('')

  const refreshPlans = async () => {
    const res = await client.request<{ subscription_plans: Plan[] }>('/subscription-plans')
    setPlans(res.subscription_plans || [])
  }

  const loadDetail = React.useCallback(async (id: number) => {
    setDetailError('')
    try {
      const res = await client.request<any>(`/subscription-plans/${id}`)
      setDetail(res)
      setLimitDraft(res.subscription_plan)
    } catch (e: any) {
      setDetailError(e?.message || String(e))
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
    if (selectedID) void loadDetail(selectedID)
  }, [selectedID, loadDetail])

  const selectPlan = (id: number) => {
    setSelectedID(id)
    setDetail(null)
    setPublishPreview(null)
    setMessage('')
  }

  const openCreate = () => {
    setCreateDraft({ name: '', description: '', enabled: true, speed_limit_mbps: 0, traffic_limit_bytes: 0, traffic_reset_mode: 'monthly', traffic_reset_day: 1 })
    setCreateNodes([])
    setPickerQuery('')
    setPickerResults([])
    setCreateOpen(true)
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
    if (pickerPlanID === 0) {
      // create mode
      setCreateNodes(list => {
        const exists = list.some(x => x.node_type === n.type && x.node_id === n.id)
        return exists ? list.filter(x => !(x.node_type === n.type && x.node_id === n.id)) : [...list, { node_type: n.type, node_id: n.id }]
      })
      return
    }
    // draft mode
    setDetail((d: any) => {
      if (!d) return d
      const draft = (d.draft_nodes || []) as PlanNode[]
      const exists = draft.some(x => x.node_type === n.type && x.node_id === n.id)
      const next = exists ? draft.filter(x => !(x.node_type === n.type && x.node_id === n.id)) : [...draft, { node_type: n.type, node_id: n.id }]
      return { ...d, draft_nodes: next }
    })
  }

  const createPlan = async () => {
    if (!createDraft.name.trim()) { setMessage('请输入方案名称'); return }
    setMessage('')
    try {
      await client.request('/subscription-plans', { method: 'POST', body: JSON.stringify({ ...createDraft, nodes: createNodes }) })
      setCreateOpen(false)
      await refreshPlans()
      notify?.('方案已创建', 'success')
    } catch (e: any) {
      setMessage('创建失败：' + (e?.message || String(e)))
    }
  }

  const saveLimits = async () => {
    if (!detail || !limitDraft) return
    setMessage('')
    try {
      await client.request(`/subscription-plans/${selectedID}`, {
        method: 'PATCH',
        body: JSON.stringify({
          expected_revision: detail.subscription_plan.revision,
          name: limitDraft.name,
          description: limitDraft.description,
          enabled: limitDraft.enabled,
          speed_limit_mbps: limitDraft.speed_limit_mbps,
          traffic_limit_bytes: limitDraft.traffic_limit_bytes,
          traffic_reset_mode: limitDraft.traffic_reset_mode,
          traffic_reset_day: limitDraft.traffic_reset_day,
        }),
      })
      await loadDetail(selectedID)
      await refreshPlans()
      notify?.('方案信息已保存', 'success')
    } catch (e: any) {
      setMessage('保存失败：' + (e?.message || String(e)))
    }
  }

  const syncDraftNodes = async (op: 'add' | 'remove' | 'replace') => {
    if (!detail) return
    setSyncBusy(true)
    setMessage('')
    try {
      const nodes = (detail.draft_nodes || []).map((n: PlanNode) => ({ node_type: n.node_type, node_id: n.node_id, display_group: n.display_group || '' }))
      await client.request(`/subscription-plans/${selectedID}/nodes/sync`, {
        method: 'POST',
        body: JSON.stringify({ op, nodes, expected_revision: detail.subscription_plan.revision }),
      })
      await loadDetail(selectedID)
      notify?.('草稿节点已更新', 'success')
    } catch (e: any) {
      setMessage('同步失败：' + (e?.message || String(e)))
    } finally {
      setSyncBusy(false)
    }
  }

  const runPublishPreview = async () => {
    if (!detail) return
    setPreviewBusy(true)
    setMessage('')
    try {
      const res = await client.request<any>(`/subscription-plans/${selectedID}/changes/preview`, {
        method: 'POST',
        body: JSON.stringify({ expected_revision: detail.subscription_plan.revision }),
      })
      setPublishPreview(res)
    } catch (e: any) {
      setMessage('预览失败：' + (e?.message || String(e)))
    } finally {
      setPreviewBusy(false)
    }
  }

  const applyPublish = async () => {
    if (!detail || !publishPreview) return
    setApplyBusy(true)
    setMessage('')
    try {
      const res = await client.request<any>(`/subscription-plans/${selectedID}/changes/apply`, {
        method: 'POST',
        body: JSON.stringify({
          preview_hash: publishPreview.preview_hash,
          expected_active_revision_id: publishPreview.expected_active_revision_id,
        }),
      })
      setPublishPreview(null)
      await loadDetail(selectedID)
      await refreshPlans()
      await loadChanges()
      notify?.(res.access_change_id ? `发布已开始（变更 #${res.access_change_id}，状态 ${res.status}）` : '方案已发布', 'success')
    } catch (e: any) {
      setMessage('发布失败：' + (e?.message || String(e)))
    } finally {
      setApplyBusy(false)
    }
  }

  const legacyPublish = async () => {
    if (!detail) return
    setApplyBusy(true)
    setMessage('')
    try {
      const res = await client.request<any>(`/subscription-plans/${selectedID}/publish`, {
        method: 'POST',
        body: JSON.stringify({ expected_revision: detail.subscription_plan.revision }),
      })
      if (res.access_change_id) {
        await loadChanges()
        notify?.(`发布已开始（变更 #${res.access_change_id}）`, 'success')
      } else {
        notify?.('方案已发布', 'success')
      }
      await loadDetail(selectedID)
      await refreshPlans()
    } catch (e: any) {
      setMessage('发布失败：' + (e?.message || String(e)))
    } finally {
      setApplyBusy(false)
    }
  }

  const disablePlan = async () => {
    if (!detail) return
    setMessage('')
    try {
      const res = await client.request<any>(`/subscription-plans/${selectedID}/disable`, {
        method: 'POST',
        body: JSON.stringify({ expected_revision: detail.subscription_plan.revision }),
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
        body: JSON.stringify({ expected_revision: detail.subscription_plan.revision }),
      })
      if (res.access_change_id) {
        await loadChanges()
        notify?.(`回滚已开始（变更 #${res.access_change_id}）`, 'success')
      } else {
        notify?.('已恢复到草稿', 'success')
      }
      await loadDetail(selectedID)
    } catch (e: any) {
      setMessage('回滚失败：' + (e?.message || String(e)))
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

  return (
    <div className="panel subscription-plans-panel">
      <div className="panel-head"><h2>订阅方案</h2></div>
      <div className="panel-body">
        <p className="muted">方案定义节点的可分配集合与速度/流量限额。编辑草稿 → 预览影响 → 应用发布；发布走“准备 → 激活 → 收尾”两阶段下发，确保服务器凭据先于订阅生效。</p>

        <div className="section-toolbar">
          <div><h3>方案列表</h3><p className="muted">共 {plans.length} 个方案。</p></div>
          <Button onClick={openCreate}><Plus size={14} /> 新建方案</Button>
        </div>

        <div className="card-custom" style={{ overflow: 'auto', marginBottom: 16 }}>
          <table className="user-data-table" style={{ minWidth: 760 }}>
            <thead>
              <tr>
                <th>名称</th><th>状态</th><th>修订</th><th>限速</th><th>流量</th><th>重置</th><th style={{ textAlign: 'right' }}>操作</th>
              </tr>
            </thead>
            <tbody>
              {plans.map(p => (
                <tr key={p.id} className="table-row-hover" style={{ backgroundColor: p.id === selectedID ? 'var(--bg-hover, rgba(0,0,0,0.03))' : 'transparent' }}>
                  <td style={{ fontWeight: 600 }}>{p.name}</td>
                  <td><Badge variant={p.enabled ? 'success' : 'secondary'}>{p.enabled ? '启用' : '已停用'}</Badge></td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>v{p.revision}</td>
                  <td>{p.speed_limit_mbps > 0 ? `${p.speed_limit_mbps} Mbps` : '不限'}</td>
                  <td>{fmtBytes(p.traffic_limit_bytes)}</td>
                  <td>{p.traffic_reset_mode === 'monthly' ? `每月 ${p.traffic_reset_day} 日` : p.traffic_reset_mode || '—'}</td>
                  <td style={{ textAlign: 'right' }}><Button variant="outline" size="sm" onClick={() => selectPlan(p.id)}>打开</Button></td>
                </tr>
              ))}
              {plans.length === 0 && <tr><td colSpan={7} className="muted" style={{ textAlign: 'center', padding: 24 }}>还没有方案，点击“新建方案”开始。</td></tr>}
            </tbody>
          </table>
        </div>

        {detailError && <p style={{ color: 'var(--color-danger)' }}>{detailError}</p>}
        {message && <p style={{ color: message.startsWith('失败') ? 'var(--color-danger)' : 'var(--color-success, #16a34a)' }}>{message}</p>}

        {plan && detail && (
          <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1.4fr) minmax(0, 1fr)', gap: 16, alignItems: 'start' }}>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
              <div className="card-custom" style={{ padding: 16 }}>
                <div className="section-toolbar">
                  <div><h3 style={{ margin: 0 }}>{plan.name}</h3><p className="muted">修订 v{plan.revision} · {detail.member_count} 个绑定用户{plan.draft_revision_id ? ` · 有草稿` : ''}</p></div>
                  <div style={{ display: 'flex', gap: 6 }}>
                    <Button variant="outline" size="sm" onClick={() => void clonePlan()}><Copy size={14} /> 复制</Button>
                    {plan.enabled && <Button variant="outline" size="sm" onClick={() => void disablePlan()}><Ban size={14} /> 停用</Button>}
                  </div>
                </div>
                <div className="form" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(170px, 1fr))' }}>
                  <Input value={limitDraft?.name ?? plan.name} onChange={e => setLimitDraft(d => d ? { ...d, name: e.target.value } : d)} placeholder="名称" />
                  <Input value={limitDraft?.description ?? plan.description} onChange={e => setLimitDraft(d => d ? { ...d, description: e.target.value } : d)} placeholder="描述" />
                  <Input type="number" min={0} value={limitDraft?.speed_limit_mbps ?? 0} onChange={e => setLimitDraft(d => d ? { ...d, speed_limit_mbps: Number(e.target.value) } : d)} placeholder="限速 Mbps（0=不限）" />
                  <Input type="number" min={0} value={limitDraft?.traffic_limit_bytes ?? 0} onChange={e => setLimitDraft(d => d ? { ...d, traffic_limit_bytes: Number(e.target.value) } : d)} placeholder="流量限额 Bytes（0=不限）" />
                  <Select value={limitDraft?.traffic_reset_mode ?? 'monthly'} onChange={e => setLimitDraft(d => d ? { ...d, traffic_reset_mode: e.target.value } : d)}>
                    <option value="monthly">每月重置</option><option value="never">不重置</option>
                  </Select>
                  <Input type="number" min={1} max={31} value={limitDraft?.traffic_reset_day ?? 1} onChange={e => setLimitDraft(d => d ? { ...d, traffic_reset_day: Number(e.target.value) } : d)} placeholder="重置日" />
                  <Button size="sm" onClick={() => void saveLimits()}>保存信息</Button>
                </div>
              </div>

              <div className="card-custom" style={{ padding: 16 }}>
                <div className="section-toolbar">
                  <div><h3 style={{ margin: 0 }}>草稿节点（{detail.draft_nodes?.length || 0}）</h3><p className="muted">先编辑草稿，再预览发布；Active 快照不受影响。</p></div>
                  <Button variant="outline" size="sm" onClick={() => { setPickerPlanID(selectedID); setPickerQuery(''); setPickerResults([]); setMessage('') }}><Plus size={14} /> 添加节点</Button>
                </div>
                <table className="user-data-table" style={{ width: '100%' }}>
                  <thead><tr><th>节点</th><th>类型</th><th>分组</th><th style={{ textAlign: 'right' }}>操作</th></tr></thead>
                  <tbody>
                    {(detail.draft_nodes || []).map((n: PlanNode) => (
                      <tr key={`${n.node_type}:${n.node_id}`}>
                        <td style={{ fontWeight: 600 }}>{n.node_type}:{n.node_id}</td>
                        <td><Badge variant="secondary">{n.node_type === 'proxy_path' ? '代理链路' : n.node_type === 'external_outbound' ? '导入节点' : '独立入口'}</Badge></td>
                        <td className="muted">{n.display_group || '—'}</td>
                        <td style={{ textAlign: 'right' }}>
                          <Button variant="ghost" size="sm" onClick={() => setDetail((d: any) => d ? { ...d, draft_nodes: (d.draft_nodes || []).filter((x: PlanNode) => !(x.node_type === n.node_type && x.node_id === n.node_id)) } : d)}><Trash2 size={14} /></Button>
                        </td>
                      </tr>
                    ))}
                    {(detail.draft_nodes || []).length === 0 && <tr><td colSpan={4} className="muted" style={{ textAlign: 'center', padding: 16 }}>草稿为空</td></tr>}
                  </tbody>
                </table>
                <div style={{ display: 'flex', gap: 8, marginTop: 10 }}>
                  <Button size="sm" variant="outline" disabled={syncBusy} onClick={() => void syncDraftNodes('add')}>保存草稿（合并）</Button>
                  <Button size="sm" variant="outline" disabled={syncBusy} onClick={() => void syncDraftNodes('replace')}>保存草稿（替换）</Button>
                </div>
              </div>

              <div className="card-custom" style={{ padding: 16 }}>
                <div className="section-toolbar">
                  <div><h3 style={{ margin: 0 }}>发布</h3><p className="muted">预览计算节点差异、受影响用户与认证服务器。</p></div>
                  <div style={{ display: 'flex', gap: 6 }}>
                    <Button size="sm" variant="outline" disabled={!plan.draft_revision_id || previewBusy} onClick={() => void runPublishPreview()}><GitCompareArrows size={14} /> 预览发布</Button>
                    <Button size="sm" disabled={!plan.draft_revision_id || applyBusy} onClick={() => void legacyPublish()}>直接发布</Button>
                  </div>
                </div>
                {publishPreview && (
                  <div style={{ marginTop: 10, display: 'flex', flexDirection: 'column', gap: 8 }}>
                    <p className="muted">新增 {publishPreview.added_nodes?.length || 0} · 移除 {publishPreview.removed_nodes?.length || 0} · 影响 {publishPreview.affected_users || 0} 个用户 · 认证服务器 {publishPreview.affected_servers?.length || 0} 台{publishPreview.offline_servers?.length ? ` · ${publishPreview.offline_servers.length} 台离线` : ''}</p>
                    <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                      {(publishPreview.added_nodes || []).map((k: string) => <Badge key={k} variant="success">+ {k}</Badge>)}
                      {(publishPreview.removed_nodes || []).map((k: string) => <Badge key={k} variant="destructive">− {k}</Badge>)}
                    </div>
                    {publishPreview.offline_servers?.length > 0 && <p style={{ color: 'var(--color-warning)' }}>有离线服务器：{publishPreview.offline_servers.join(', ')}。仍可继续，任务将等待服务器恢复后自动下发。</p>}
                    <div style={{ display: 'flex', gap: 8 }}>
                      <Button size="sm" disabled={applyBusy} onClick={() => void applyPublish()}>{applyBusy ? '发布中...' : '确认应用发布'}</Button>
                      <Button size="sm" variant="ghost" onClick={() => setPublishPreview(null)}>取消</Button>
                    </div>
                  </div>
                )}
              </div>

              <div className="card-custom" style={{ padding: 16 }}>
                <h3 style={{ marginTop: 0 }}>历史修订</h3>
                <table className="user-data-table" style={{ width: '100%' }}>
                  <thead><tr><th>版本</th><th>状态</th><th>限速</th><th>流量</th><th>激活时间</th><th style={{ textAlign: 'right' }}>操作</th></tr></thead>
                  <tbody>
                    {(detail.revisions || []).map((r: Revision) => (
                      <tr key={r.id}>
                        <td style={{ fontFamily: 'var(--font-mono)' }}>v{r.revision}</td>
                        <td><Badge variant={r.status === 'active' ? 'success' : r.status === 'draft' ? 'warning' : 'secondary'}>{r.status === 'active' ? '当前生效' : r.status === 'draft' ? '草稿' : '已归档'}</Badge></td>
                        <td>{r.speed_limit_mbps > 0 ? `${r.speed_limit_mbps} Mbps` : '不限'}</td>
                        <td>{fmtBytes(r.traffic_limit_bytes)}</td>
                        <td className="muted">{fmtDate(r.activated_at)}</td>
                        <td style={{ textAlign: 'right' }}>
                          {r.status !== 'draft' && <Button variant="outline" size="sm" onClick={() => void restoreRevision(r.id)}><RotateCcw size={14} /> 回滚到草稿</Button>}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>

            <div className="card-custom" style={{ padding: 16 }}>
              <div className="section-toolbar">
                <div><h3 style={{ margin: 0 }}>部署变更</h3><p className="muted">方案相关的两阶段下发记录。</p></div>
                <Button variant="ghost" size="sm" onClick={() => void loadChanges()}><RefreshCw size={14} /></Button>
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 10, maxHeight: 640, overflow: 'auto' }}>
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

      <Dialog isOpen={createOpen} onClose={() => setCreateOpen(false)} title="新建方案" size="lg">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div className="form" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(170px, 1fr))' }}>
            <Input value={createDraft.name} onChange={e => setCreateDraft(d => ({ ...d, name: e.target.value }))} placeholder="名称" />
            <Input value={createDraft.description} onChange={e => setCreateDraft(d => ({ ...d, description: e.target.value }))} placeholder="描述" />
            <Input type="number" min={0} value={createDraft.speed_limit_mbps} onChange={e => setCreateDraft(d => ({ ...d, speed_limit_mbps: Number(e.target.value) }))} placeholder="限速 Mbps" />
            <Input type="number" min={0} value={createDraft.traffic_limit_bytes} onChange={e => setCreateDraft(d => ({ ...d, traffic_limit_bytes: Number(e.target.value) }))} placeholder="流量限额 Bytes" />
            <Select value={createDraft.traffic_reset_mode} onChange={e => setCreateDraft(d => ({ ...d, traffic_reset_mode: e.target.value }))}>
              <option value="monthly">每月重置</option><option value="never">不重置</option>
            </Select>
            <Input type="number" min={1} max={31} value={createDraft.traffic_reset_day} onChange={e => setCreateDraft(d => ({ ...d, traffic_reset_day: Number(e.target.value) }))} placeholder="重置日" />
          </div>
          <div>
            <h3 style={{ marginTop: 0 }}>初始节点（{createNodes.length}）</h3>
            <div style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
              <Input value={pickerQuery} onChange={e => setPickerQuery(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') void runPickerSearch(pickerQuery) }} placeholder="搜索节点..." />
              <Button variant="outline" size="sm" disabled={pickerBusy} onClick={() => void runPickerSearch(pickerQuery)}>搜索</Button>
            </div>
            <div className="card-custom" style={{ maxHeight: 240, overflow: 'auto' }}>
              {pickerResults.map(n => {
                const exists = createNodes.some(x => x.node_type === n.type && x.node_id === n.id)
                return (
                  <label key={n.key} style={{ display: 'flex', gap: 8, alignItems: 'center', padding: '6px 8px', cursor: 'pointer' }}>
                    <input type="checkbox" checked={exists} onChange={() => togglePickerNode(n)} />
                    <span style={{ fontWeight: 600 }}>{n.name}</span>
                    <span className="muted" style={{ fontSize: 12 }}>{n.entry_protocol || ''} {n.exit_region ? `· ${n.exit_region}` : ''}</span>
                  </label>
                )
              })}
              {pickerResults.length === 0 && !pickerBusy && <p className="muted" style={{ padding: 12 }}>输入关键词搜索节点</p>}
            </div>
          </div>
          {message && <p style={{ color: 'var(--color-danger)' }}>{message}</p>}
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>取消</Button>
            <Button onClick={() => void createPlan()}>创建方案</Button>
          </div>
        </div>
      </Dialog>

      <Dialog isOpen={pickerPlanID !== 0} onClose={() => setPickerPlanID(0)} title="添加节点到草稿" size="lg">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div style={{ display: 'flex', gap: 8 }}>
            <Input value={pickerQuery} onChange={e => setPickerQuery(e.target.value)} onKeyDown={e => { if (e.key === 'Enter') void runPickerSearch(pickerQuery) }} placeholder="搜索节点..." />
            <Button variant="outline" size="sm" disabled={pickerBusy} onClick={() => void runPickerSearch(pickerQuery)}>搜索</Button>
          </div>
          <div className="card-custom" style={{ maxHeight: 320, overflow: 'auto' }}>
            {pickerResults.map(n => {
              const exists = (detail?.draft_nodes || []).some((x: PlanNode) => x.node_type === n.type && x.node_id === n.id)
              return (
                <label key={n.key} style={{ display: 'flex', gap: 8, alignItems: 'center', padding: '6px 8px', cursor: 'pointer' }}>
                  <input type="checkbox" checked={exists} onChange={() => togglePickerNode(n)} />
                  <span style={{ fontWeight: 600 }}>{n.name}</span>
                  <span className="muted" style={{ fontSize: 12 }}>{n.entry_protocol || ''} {n.exit_region ? `· ${n.exit_region}` : ''}</span>
                </label>
              )
            })}
            {pickerResults.length === 0 && !pickerBusy && <p className="muted" style={{ padding: 12 }}>输入关键词搜索节点</p>}
          </div>
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
            <Button variant="outline" onClick={() => setPickerPlanID(0)}>关闭</Button>
            <Button onClick={() => { setPickerPlanID(0); void syncDraftNodes('add') }}>保存到草稿</Button>
          </div>
        </div>
      </Dialog>
    </div>
  )
}
