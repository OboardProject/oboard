import * as React from 'react'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Select } from '../components/ui/select'
import { Input } from '../components/ui/input'
import { Switch } from '../components/ui/switch'
import { NodeScopeMenu, type NodeScopeRequest, type ScopeNode } from '../components/node-assignment/NodeScopeMenu'
import { NodeScopeActionDialog } from '../components/node-assignment/NodeScopeActionDialog'
import { X, MoreHorizontal, Pencil, Info, Settings, Search, SlidersHorizontal, RotateCcw } from 'lucide-react'
import { hasManagementAccess } from '../permissions'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }

type CatalogNode = ScopeNode & {
  name: string
  source_name: string
  global_name_override?: string | null
  has_global_name_override: boolean
  effective_global_name: string
  metadata_lock_version: number
  entry_server_name?: string
  entry_protocol?: string
  entry_port?: number
  path_summary?: string[]
  enabled: boolean
  status: 'ok' | 'offline' | 'disabled'
  group?: string
  plans: { plan_id: number; name: string; display_group?: string; display_name: string; has_display_name_override: boolean }[]
  effective_users: number
  allow_exceptions: number
  deny_exceptions: number
}

type CatalogResponse = { nodes: CatalogNode[]; total: number; page: number; page_size: number }

type DetailUser = { user_id: number; username: string; nickname?: string; effective: boolean; source?: string; plan_id?: number; plan_name?: string; effect?: string; reason?: string; expires_at?: string }
type DetailResponse = { node: any; users: DetailUser[]; plans: { plan_id: number; name: string; display_group?: string }[]; exceptions: any[]; runtime_authorization_mode: string }
type RenameNode = Pick<CatalogNode, 'type' | 'id' | 'name' | 'source_name' | 'global_name_override' | 'metadata_lock_version'>

function NodeRenameDialog({ node, client, onClose, onSaved }: { node: RenameNode | null; client: AnyClient; onClose: () => void; onSaved: () => Promise<void> }) {
  const [name, setName] = React.useState('')
  const [saving, setSaving] = React.useState(false)
  const [error, setError] = React.useState('')
  React.useEffect(() => { setName(node?.global_name_override || ''); setError('') }, [node])
  const save = async () => {
    if (!node || saving) return
    setSaving(true)
    setError('')
    try {
      await client.request(`/assignable-nodes/${node.type}/${node.id}/metadata`, {
        method: 'PATCH',
        body: JSON.stringify({ display_name_override: name.trim() || null, expected_lock_version: node.metadata_lock_version || 0 }),
      })
      onClose()
      await onSaved()
    } catch (requestError: any) {
      setError(requestError?.message || String(requestError))
    } finally {
      setSaving(false)
    }
  }
  return <Dialog isOpen={node !== null} onClose={onClose} title="全局节点名称" size="default">
    <div className="form">
      <p className="muted">留空将恢复来源名称“{node?.source_name || node?.name}”。</p>
      <label><span>节点名称</span><Input value={name} onChange={event => setName(event.target.value)} maxLength={100} autoFocus aria-label="全局节点名称" /></label>
      {error && <p role="alert" className="muted">{error}</p>}
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}><Button variant="outline" onClick={onClose} disabled={saving}>取消</Button><Button onClick={() => void save()} disabled={saving}>{saving ? '保存中…' : '保存'}</Button></div>
    </div>
  </Dialog>
}

const protocolOptions = ['vless', 'hysteria2', 'anytls', 'shadowsocks', 'mieru', 'socks', 'wireguard']
const statusLabels: Record<string, string> = { ok: '正常', offline: '离线', disabled: '已禁用' }

function PlanBadgesCell({ plans }: { plans: Array<{ plan_id: number; name: string }> }) {
  const [expanded, setExpanded] = React.useState(false)
  if (!plans || plans.length === 0) {
    return <span className="muted" style={{ fontSize: 12 }}>未分配</span>
  }

  const allNames = plans.map(p => p.name).join('、')

  if (plans.length <= 2 || expanded) {
    return (
      <div className="plan-badges-wrap" title={allNames}>
        {plans.map(p => (
          <span key={p.plan_id} className="plan-badge-pill">
            {p.name}
          </span>
        ))}
        {expanded && plans.length > 2 && (
          <button
            type="button"
            className="plan-more-chip"
            onClick={() => setExpanded(false)}
            title="收起"
            style={{ fontSize: 10, padding: '1px 5px' }}
          >
            收起
          </button>
        )}
      </div>
    )
  }

  const visiblePlans = plans.slice(0, 2)
  const remainingCount = plans.length - 2

  return (
    <div className="plan-badges-wrap" title={`包含全部 ${plans.length} 个套餐：${allNames}`}>
      {visiblePlans.map(p => (
        <span key={p.plan_id} className="plan-badge-pill">
          {p.name}
        </span>
      ))}
      <button
        type="button"
        className="plan-more-chip"
        onClick={() => setExpanded(true)}
        aria-label={`展开剩余 ${remainingCount} 个套餐：${allNames}`}
      >
        +{remainingCount}
      </button>
    </div>
  )
}

function nodeTypeLabel(type: string) {
  if (type === 'proxy_path') return '代理拓扑'
  if (type === 'external_outbound') return '导入节点'
  if (type === 'inbound') return '独立入口'
  return type
}

export function NodeAssignmentsPage({ data, client, load, notify }: {
  data: any
  client: AnyClient
  load: () => Promise<void>
  notify?: (message: string, tone?: 'success' | 'error' | 'warning') => void
}) {
  const [query, setQuery] = React.useState('')
  const [entryServerID, setEntryServerID] = React.useState(0)
  const [entryRegion, setEntryRegion] = React.useState('')
  const [exitRegion, setExitRegion] = React.useState('')
  const [protocol, setProtocol] = React.useState('')
  const [status, setStatus] = React.useState('')
  const [planID, setPlanID] = React.useState(0)
  const [unassigned, setUnassigned] = React.useState(false)
  const [groupBy, setGroupBy] = React.useState('')
  const [sort, setSort] = React.useState('name')
  const [page, setPage] = React.useState(1)
  const [pageSize, setPageSize] = React.useState(50)
  const [nodes, setNodes] = React.useState<CatalogNode[]>([])
  const [total, setTotal] = React.useState(0)
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState('')
  const [detail, setDetail] = React.useState<DetailResponse | null>(null)
  const [selected, setSelected] = React.useState<Record<string, boolean>>({})
  const [syncPlanID, setSyncPlanID] = React.useState(0)
  const [syncOp, setSyncOp] = React.useState<'add' | 'remove' | 'replace'>('add')
  const [syncBusy, setSyncBusy] = React.useState(false)
  const [syncMessage, setSyncMessage] = React.useState('')
  const [filtersOpen, setFiltersOpen] = React.useState(false)
  const [menu, setMenu] = React.useState<{ x: number; y: number; node: CatalogNode } | null>(null)
  const [scopeAction, setScopeAction] = React.useState<{ node: CatalogNode; scope: NodeScopeRequest } | null>(null)
  const [users, setUsers] = React.useState<any[] | null>(null)
  const [showType, setShowType] = React.useState(false)
  const [renameNode, setRenameNode] = React.useState<CatalogNode | null>(null)
  const [batchDialogOpen, setBatchDialogOpen] = React.useState(false)

  const servers = data.servers || []
  const plans = data.subscription_plans || []
  const isAdmin = hasManagementAccess(data.session?.role)
  const regionCodes = Array.from(new Set(servers.flatMap((s: any) => [s.region_code, s.detected_region_code]).filter((x: any) => Boolean(x)))) as string[]

  const ensureUsers = async () => {
    if (users) return users
    try {
      const res = await client.request<{ users: any[] }>('/users')
      setUsers(res.users || [])
      return res.users || []
    } catch {
      return []
    }
  }

  const loadNodes = React.useCallback(async (nextPage = page) => {
    setLoading(true)
    setError('')
    try {
      const params = new URLSearchParams()
      if (query) params.set('query', query)
      if (entryServerID) params.set('entry_server_id', String(entryServerID))
      if (entryRegion) params.set('entry_region', entryRegion)
      if (exitRegion) params.set('exit_region', exitRegion)
      if (protocol) params.set('protocol', protocol)
      if (status) params.set('status', status)
      if (planID) params.set('plan_id', String(planID))
      if (unassigned) params.set('unassigned', '1')
      if (groupBy) params.set('group_by', groupBy)
      if (sort) params.set('sort', sort)
      params.set('page', String(nextPage))
      params.set('page_size', String(pageSize))
      const res = await client.request<CatalogResponse>('/assignable-nodes?' + params.toString())
      setNodes(res.nodes || [])
      setTotal(res.total || 0)
      setSelected({})
    } catch (e: any) {
      setError(e?.message || String(e))
    } finally {
      setLoading(false)
    }
  }, [query, entryServerID, entryRegion, exitRegion, protocol, status, planID, unassigned, groupBy, sort, pageSize, page])

  React.useEffect(() => { void loadNodes(1) }, [loadNodes])

  const openDetail = async (node: CatalogNode) => {
    try {
      const res = await client.request<DetailResponse>(`/assignable-nodes/${node.type}/${node.id}`)
      setDetail(res)
    } catch (e: any) {
      setError(e?.message || String(e))
    }
  }

  const toggleAll = (checked: boolean) => {
    const next: Record<string, boolean> = {}
    if (checked) for (const n of nodes) next[n.key] = true
    setSelected(next)
  }

  const selectedKeys = Object.keys(selected).filter(k => selected[k])
  const selectedCount = selectedKeys.length

  const refresh = async () => {
    await load()
    await loadNodes(page)
  }

  const runSync = async () => {
    if (!syncPlanID || selectedCount === 0) return
    setSyncBusy(true)
    setSyncMessage('')
    try {
      const plan = await client.request<{ subscription_plan?: { lock_version: number; latest_revision_id: number } }>(`/subscription-plans/${syncPlanID}`)
      const lockVersion = plan.subscription_plan?.lock_version || 0
      const baseRevisionID = plan.subscription_plan?.latest_revision_id || 0
      if (!lockVersion || !baseRevisionID) throw new Error('无法获取套餐版本信息，请刷新后重试')
      const nodeRefs = nodes.filter(n => selected[n.key]).map(n => ({ node_type: n.type, node_id: n.id }))
      const res = await client.request<{ access_change_id?: number; no_change?: boolean }>(`/subscription-plans/${syncPlanID}/nodes/apply`, {
        method: 'POST',
        body: JSON.stringify({ op: syncOp, nodes: nodeRefs, base_revision_id: baseRevisionID, expected_lock_version: lockVersion }),
      })
      if (res.no_change) {
        setSyncMessage('节点没有变化')
        notify?.('节点没有变化', 'warning')
      } else if (res.access_change_id) {
        setSyncMessage(`已保存，正在应用变更 #${res.access_change_id}`)
        notify?.(`已保存，正在应用变更 #${res.access_change_id}`, 'success')
      } else {
        setSyncMessage('已保存为新版本')
        notify?.('已保存为新版本', 'success')
      }
      setSelected({})
      setBatchDialogOpen(false)
      await refresh()
    } catch (e: any) {
      setSyncMessage('操作失败：' + (e?.message || String(e)))
    } finally {
      setSyncBusy(false)
    }
  }

  const openScopeMenu = (node: CatalogNode, x: number, y: number) => {
    setMenu({ x, y, node })
  }

  const handleScopeSelect = (scope: NodeScopeRequest) => {
    if (!menu) return
    setScopeAction({ node: menu.node, scope })
    void ensureUsers()
  }

  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  return (
    <div className="panel node-assignments-panel">
      <div className="panel-body">
        <>
        <div className="node-list-toolbar">
          <div className="node-list-search">
            <Search size={14} aria-hidden="true" />
            <input
              type="search"
              value={query}
              onChange={e => setQuery(e.target.value)}
              placeholder="搜索名称 / 服务器 / 协议 / 地区"
              aria-label="搜索节点"
            />
            {query && (
              <button type="button" className="ghost icon-button" onClick={() => setQuery('')} aria-label="清除搜索" title="清除搜索">
                <X size={13} />
              </button>
            )}
          </div>
          <div className="node-list-toolbar-tools">
            <button
              type="button"
              className={`ghost icon-button node-filter-toggle-btn ${filtersOpen || Boolean(entryServerID || entryRegion || exitRegion || protocol || status || planID || unassigned || groupBy || sort !== 'name') ? 'is-active' : ''}`}
              onClick={() => setFiltersOpen(v => !v)}
              aria-label={filtersOpen ? '收起筛选' : '展开筛选'}
              title={filtersOpen ? '收起筛选' : '展开筛选'}
            >
              <SlidersHorizontal size={14} />
              {Boolean(entryServerID || entryRegion || exitRegion || protocol || status || planID || unassigned || groupBy || sort !== 'name') && (
                <span className="node-filter-badge" />
              )}
            </button>
            {Boolean(query || entryServerID || entryRegion || exitRegion || protocol || status || planID || unassigned || groupBy || sort !== 'name') && (
              <button
                type="button"
                className="ghost icon-button"
                onClick={() => { setQuery(''); setEntryServerID(0); setEntryRegion(''); setExitRegion(''); setProtocol(''); setStatus(''); setPlanID(0); setUnassigned(false); setGroupBy(''); setSort('name'); setPage(1) }}
                aria-label="重置筛选"
                title="重置筛选"
              >
                <RotateCcw size={14} />
              </button>
            )}
            <label className="node-toolbar-switch-label">
              <Switch size="sm" checked={showType} onChange={setShowType} ariaLabel="显示类型" />
              <span>显示类型</span>
            </label>
            <span className="node-list-result-count">共 {total} 个节点</span>
          </div>
          {isAdmin && selectedCount > 0 && (
            <div className="node-list-batch">
              <Button variant="default" size="sm" onClick={() => { setSyncMessage(''); setBatchDialogOpen(true) }}>
                <Settings size={14} />
                批量设置（{selectedCount}）
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setSelected({})}>取消</Button>
            </div>
          )}
        </div>

        {filtersOpen && (
          <div className="node-filter-drawer">
            <Select value={entryServerID} onChange={e => setEntryServerID(Number(e.target.value))}>
              <option value={0}>入口服务器：全部</option>
              {servers.map((s: any) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </Select>
            <Select value={entryRegion} onChange={e => setEntryRegion(e.target.value)}>
              <option value="">入口地区：全部</option>
              {regionCodes.map((r: string) => <option key={r} value={r}>{r}</option>)}
            </Select>
            <Select value={exitRegion} onChange={e => setExitRegion(e.target.value)}>
              <option value="">出口地区：全部</option>
              {regionCodes.map((r: string) => <option key={r} value={r}>{r}</option>)}
            </Select>
            <Select value={protocol} onChange={e => setProtocol(e.target.value)}>
              <option value="">协议：全部</option>
              {protocolOptions.map(p => <option key={p} value={p}>{p}</option>)}
            </Select>
            <Select value={status} onChange={e => setStatus(e.target.value)}>
              <option value="">状态：全部</option>
              {Object.entries(statusLabels).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            </Select>
            <Select value={planID} onChange={e => setPlanID(Number(e.target.value))}>
              <option value={0}>套餐：全部</option>
              {plans.map((p: any) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </Select>
            <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, cursor: 'pointer' }}>
              <Switch size="sm" checked={unassigned} onChange={setUnassigned} ariaLabel="仅看未分配节点" /> 仅看未分配节点
            </label>
            <Select value={groupBy} onChange={e => setGroupBy(e.target.value)}>
              <option value="">分组：不分组</option>
              <option value="entry_server">按入口服务器</option>
              <option value="exit_region">按出口地区</option>
            </Select>
            <Select value={sort} onChange={e => setSort(e.target.value)}>
              <option value="name">排序：名称</option>
              <option value="entry_server">排序：入口服务器</option>
              <option value="exit_region">排序：出口地区</option>
              <option value="users">排序：用户数</option>
              <option value="status">排序：状态</option>
            </Select>
            <Select value={pageSize} onChange={e => setPageSize(Number(e.target.value))}>
              <option value={25}>每页 25</option>
              <option value={50}>每页 50</option>
              <option value={100}>每页 100</option>
              <option value={200}>每页 200</option>
            </Select>
          </div>
        )}

        {syncMessage && <p style={{ margin: '8px 0 0', color: syncMessage.startsWith('操作失败') ? 'var(--color-danger)' : 'var(--color-success, #16a34a)', fontSize: 13 }}>{syncMessage}</p>}

        {error && <p style={{ color: 'var(--color-danger)' }}>{error}</p>}

        {/* Desktop Table View */}
        <div className="card-custom node-table-card node-desktop-table" style={{ padding: 0, marginTop: 12, overflow: 'auto' }}>
          <table className="user-data-table node-catalog-table" style={{ minWidth: 980, tableLayout: 'fixed' }}>
            <colgroup>
              <col style={{ width: 44 }} />
              <col style={{ width: showType ? '16%' : '18%' }} />
              {showType && <col style={{ width: '8%' }} />}
              <col style={{ width: showType ? '11%' : '12%' }} />
              <col style={{ width: showType ? '8%' : '9%' }} />
              <col style={{ width: showType ? '7%' : '8%' }} />
              <col style={{ width: showType ? '18%' : '20%' }} />
              <col style={{ width: showType ? '7%' : '8%' }} />
              <col style={{ width: showType ? '10%' : '10%' }} />
              <col style={{ width: showType ? '15%' : '15%' }} />
            </colgroup>
            <thead>
              <tr>
                <th style={{ width: 44, padding: '10px 12px' }}><input type="checkbox" checked={nodes.length > 0 && selectedCount === nodes.length} onChange={e => toggleAll(e.target.checked)} aria-label="全选本页" /></th>
                <th style={{ padding: '10px 12px' }}>节点</th>
                {showType && <th style={{ padding: '10px 12px' }}>类型</th>}
                <th style={{ padding: '10px 12px' }}>所属服务器</th>
                <th style={{ padding: '10px 12px' }}>协议</th>
                <th style={{ padding: '10px 12px' }}>状态</th>
                <th style={{ padding: '10px 12px' }}>所属套餐</th>
                <th style={{ padding: '10px 12px' }}>有效用户</th>
                <th style={{ padding: '10px 12px' }}>例外</th>
                <th style={{ textAlign: 'right', padding: '10px 16px' }}>操作</th>
              </tr>
            </thead>
            <tbody>
              {nodes.map(n => (
                <tr key={n.key} className="table-row-hover" onContextMenu={e => { e.preventDefault(); openScopeMenu(n, e.clientX, e.clientY) }}>
                  <td style={{ padding: '10px 12px' }}><input type="checkbox" checked={Boolean(selected[n.key])} onChange={e => setSelected(s => ({ ...s, [n.key]: e.target.checked }))} aria-label={`选择 ${n.name}`} /></td>
                  <td className="node-name-cell" style={{ padding: '10px 12px' }}>
                    <span style={{ fontWeight: 600, display: 'inline-block', whiteSpace: 'normal', wordBreak: 'break-word' }}>{n.name}</span>
                    {n.has_global_name_override && <span className="muted" style={{ display: 'block', fontSize: 12 }}>来源：{n.source_name} · 全局别名</span>}
                    {n.group ? <span className="muted" style={{ display: 'block', fontSize: 12, fontWeight: 400 }}>{n.group}</span> : null}
                  </td>
                  {showType && <td style={{ padding: '10px 12px' }}><Badge variant="secondary">{nodeTypeLabel(n.type)}</Badge></td>}
                  <td style={{ fontWeight: 500, padding: '10px 12px' }}>{n.entry_server_name || '—'}</td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 12, padding: '10px 12px' }}>{n.entry_protocol || '—'}</td>
                  <td style={{ padding: '10px 12px' }}>
                    <Badge variant={n.status === 'ok' ? 'success' : n.status === 'disabled' ? 'secondary' : 'warning'}>
                      {statusLabels[n.status] || n.status}
                    </Badge>
                  </td>
                  <td style={{ padding: '10px 12px' }}>
                    <PlanBadgesCell plans={n.plans} />
                  </td>
                  <td style={{ padding: '10px 12px' }}>{n.effective_users}</td>
                  <td style={{ padding: '10px 12px' }}>
                    {n.allow_exceptions > 0 && <Badge variant="success" style={{ marginRight: 4 }}>允许 {n.allow_exceptions}</Badge>}
                    {n.deny_exceptions > 0 && <Badge variant="destructive">拒绝 {n.deny_exceptions}</Badge>}
                    {n.allow_exceptions === 0 && n.deny_exceptions === 0 && <span className="muted">—</span>}
                  </td>
                  <td style={{ textAlign: 'right', whiteSpace: 'nowrap', padding: '8px 16px' }}>
                    <div className="node-row-actions">
                      {isAdmin && (
                        <button
                          type="button"
                          className="node-row-icon-button"
                          onClick={() => setRenameNode(n)}
                          aria-label={`重命名 ${n.name}`}
                          title="修改全局名称"
                        >
                          <Pencil size={15} />
                        </button>
                      )}
                      <button
                        type="button"
                        className="node-row-icon-button"
                        onClick={() => void openDetail(n)}
                        aria-label={`查看详情 ${n.name}`}
                        title="查看详情"
                      >
                        <Info size={15} />
                      </button>
                      <button
                        type="button"
                        className="node-row-icon-button"
                        onClick={e => {
                          const rect = e.currentTarget.getBoundingClientRect()
                          openScopeMenu(n, rect.right, rect.bottom + 4)
                        }}
                        aria-label={`节点操作 ${n.name}`}
                        title="选择节点范围"
                      >
                        <MoreHorizontal size={15} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
              {nodes.length === 0 && !loading && <tr><td colSpan={showType ? 10 : 9} className="muted" style={{ textAlign: 'center', padding: 24 }}>没有匹配的节点</td></tr>}
            </tbody>
          </table>
          {loading && <p className="muted" style={{ padding: 12 }}>加载中...</p>}
        </div>

        {/* Mobile Card View */}
        <div className="node-mobile-cards">
          {nodes.map(n => (
            <div key={n.key} className="card-custom node-mobile-card">
              <div className="node-mobile-card-head">
                <div className="node-mobile-card-title-group">
                  <input
                    type="checkbox"
                    checked={Boolean(selected[n.key])}
                    onChange={e => setSelected(s => ({ ...s, [n.key]: e.target.checked }))}
                    aria-label={`选择 ${n.name}`}
                  />
                  <div className="node-mobile-card-copy">
                    <div className="node-mobile-card-title-line">
                      <span className="node-mobile-card-name">{n.name}</span>
                      <Badge variant={n.status === 'ok' ? 'success' : n.status === 'disabled' ? 'secondary' : 'warning'}>
                        {statusLabels[n.status] || n.status}
                      </Badge>
                      {showType && <Badge variant="secondary">{nodeTypeLabel(n.type)}</Badge>}
                    </div>
                    {n.has_global_name_override && <span className="muted node-mobile-card-sub">来源：{n.source_name} · 全局别名</span>}
                    {n.group && <span className="muted node-mobile-card-sub">{n.group}</span>}
                  </div>
                </div>
              </div>
              <div className="node-mobile-card-meta">
                <div className="node-mobile-meta-item">
                  <span className="label">服务器</span>
                  <span className="value">{n.entry_server_name || '—'} ({n.entry_protocol || '—'})</span>
                </div>
                <div className="node-mobile-meta-item">
                  <span className="label">用户</span>
                  <span className="value">{n.effective_users} 人</span>
                  {n.allow_exceptions > 0 && <Badge variant="success">+{n.allow_exceptions}</Badge>}
                  {n.deny_exceptions > 0 && <Badge variant="destructive">-{n.deny_exceptions}</Badge>}
                </div>
                {n.plans && n.plans.length > 0 && (
                  <div className="node-mobile-meta-plans">
                    <PlanBadgesCell plans={n.plans} />
                  </div>
                )}
              </div>
              <div className="node-mobile-card-actions">
                {isAdmin && (
                  <button
                    type="button"
                    className="node-row-icon-button"
                    onClick={() => setRenameNode(n)}
                    aria-label={`重命名 ${n.name}`}
                    title="修改全局名称"
                  >
                    <Pencil size={14} />
                    <span>重命名</span>
                  </button>
                )}
                <button
                  type="button"
                  className="node-row-icon-button"
                  onClick={() => void openDetail(n)}
                  aria-label={`查看详情 ${n.name}`}
                  title="查看详情"
                >
                  <Info size={14} />
                  <span>详情</span>
                </button>
                <button
                  type="button"
                  className="node-row-icon-button"
                  onClick={e => {
                    const rect = e.currentTarget.getBoundingClientRect()
                    openScopeMenu(n, rect.right, rect.bottom + 4)
                  }}
                  aria-label={`节点操作 ${n.name}`}
                  title="选择节点范围"
                >
                  <MoreHorizontal size={14} />
                  <span>操作</span>
                </button>
              </div>
            </div>
          ))}
          {nodes.length === 0 && !loading && (
            <div className="card-custom" style={{ textAlign: 'center', padding: 24, color: 'var(--muted)', fontSize: 13 }}>
              没有匹配的节点
            </div>
          )}
        </div>

        <div className="node-assignments-pager">
          <div className="node-pager-buttons">
            <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage(p => Math.max(1, p - 1))}>上一页</Button>
            <span className="muted node-pager-status">第 {page} / {totalPages} 页</span>
            <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={() => setPage(p => Math.min(totalPages, p + 1))}>下一页</Button>
          </div>
          {isAdmin ? (
            <span className="muted node-assignments-hint">
              {selectedCount > 0 ? `已在上方开启 ${selectedCount} 个节点的批量设置` : '勾选节点可在顶部开启批量设置'}
            </span>
          ) : (
            <span className="muted node-assignments-hint">套餐修改、用户授权与套餐分配需要管理员权限。</span>
          )}
        </div>
      </>
    </div>

      <Dialog isOpen={detail !== null} onClose={() => setDetail(null)} title={detail ? detail.node?.name || '节点详情' : ''} size="lg">
        {detail && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
            <div className="form" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(160px, 1fr))' }}>
              <div><span className="muted">类型</span><div>{nodeTypeLabel(detail.node?.type)}</div></div>
              <div><span className="muted">全局名称</span><div>{detail.node?.effective_global_name || detail.node?.name}</div></div>
              <div><span className="muted">来源名称</span><div>{detail.node?.source_name || '—'}</div></div>
              <div><span className="muted">稳定标识</span><div><code>{detail.node?.key}</code></div></div>
              <div><span className="muted">入口</span><div>{detail.node?.entry_server_name || '—'}</div></div>
              <div><span className="muted">协议</span><div style={{ fontFamily: 'var(--font-mono)' }}>{detail.node?.entry_protocol || '—'}</div></div>
              <div><span className="muted">出口地区</span><div>{detail.node?.exit_region || '—'}</div></div>
              <div><span className="muted">状态</span><div>{statusLabels[detail.node?.status] || detail.node?.status}</div></div>
              <div><span className="muted">路径</span><div>{(detail.node?.path_summary || []).join(' → ') || '—'}</div></div>
            </div>
            <div>
              <h3 style={{ marginTop: 0 }}>所属套餐</h3>
              {detail.plans?.length === 0 ? <p className="muted">未分配任何套餐</p> : detail.plans?.map((p: any) => (
                <div key={p.plan_id} className="node-plan-name-row">
                  <strong>{p.name}</strong>
                  <span>{p.display_name}</span>
                  <Badge variant={p.has_display_name_override ? 'secondary' : 'outline'}>{p.has_display_name_override ? '方案独立' : '继承全局'}</Badge>
                </div>
              ))}
            </div>
            <div>
              <h3 style={{ marginTop: 0 }}>有效用户（{detail.users?.length || 0}）</h3>
              <div className="card-custom" style={{ maxHeight: 260, overflow: 'auto' }}>
                <table className="user-data-table">
                  <thead><tr><th>用户</th><th>来源</th><th>原因</th><th>到期</th></tr></thead>
                  <tbody>
                    {(detail.users || []).map((u: DetailUser) => (
                      <tr key={u.user_id}>
                        <td style={{ fontWeight: 600 }}>{u.username}{u.nickname ? `（${u.nickname}）` : ''}</td>
                        <td>
                          {u.source === 'exception_allow' && <Badge variant="success">允许例外</Badge>}
                          {u.source === 'exception_deny' && <Badge variant="destructive">拒绝例外</Badge>}
                          {u.source === 'plan' && <Badge variant="secondary">{u.plan_name || '套餐'}</Badge>}
                        </td>
                        <td className="muted">{u.reason || '—'}</td>
                        <td className="muted">{u.expires_at ? new Date(u.expires_at).toLocaleString() : '—'}</td>
                      </tr>
                    ))}
                    {detail.users?.length === 0 && <tr><td colSpan={4} className="muted" style={{ textAlign: 'center', padding: 16 }}>暂无有效用户</td></tr>}
                  </tbody>
                </table>
              </div>
            </div>
            <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
              <Button variant="outline" onClick={() => setDetail(null)}><X size={14} /> 关闭</Button>
            </div>
          </div>
        )}
      </Dialog>

      {menu && (
        <NodeScopeMenu
          x={menu.x}
          y={menu.y}
          node={menu.node}
          onSelect={handleScopeSelect}
          onClose={() => setMenu(null)}
        />
      )}

      <NodeScopeActionDialog
        open={scopeAction !== null}
        node={scopeAction?.node || null}
        scope={scopeAction?.scope || null}
        plans={plans}
        users={users || []}
        client={client}
        notify={notify}
        onClose={() => setScopeAction(null)}
        onDone={refresh}
      />

      <NodeRenameDialog node={renameNode as RenameNode | null} client={client} onClose={() => setRenameNode(null)} onSaved={async () => { await refresh(); notify?.('全局节点名称已更新', 'success') }} />

      <Dialog isOpen={batchDialogOpen} onClose={() => setBatchDialogOpen(false)} title={`批量设置节点套餐（已选 ${selectedCount} 个节点）`} size="default">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <p className="muted" style={{ margin: 0 }}>为当前已选中的 {selectedCount} 个节点批量分配或调整套餐。</p>
          {selectedCount > 0 && (
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', maxHeight: 120, overflow: 'auto', padding: 8, background: 'var(--bg-subtle)', borderRadius: 'var(--radius-md)' }}>
              {nodes.filter(n => selected[n.key]).map(n => (
                <Badge key={n.key} variant="secondary">{n.name}</Badge>
              ))}
            </div>
          )}
          <div className="form">
            <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 13, fontWeight: 500 }}>
              <span>目标套餐</span>
              <Select value={syncPlanID} onChange={e => setSyncPlanID(Number(e.target.value))}>
                <option value={0}>选择目标套餐</option>
                {plans.map((p: any) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </Select>
            </label>
            <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 13, fontWeight: 500 }}>
              <span>批量操作</span>
              <Select value={syncOp} onChange={e => setSyncOp(e.target.value as 'add' | 'remove' | 'replace')}>
                <option value="add">加入套餐</option>
                <option value="remove">从套餐移除</option>
                <option value="replace">替换套餐节点</option>
              </Select>
            </label>
          </div>
          {syncMessage && <p style={{ margin: 0, color: syncMessage.startsWith('操作失败') ? 'var(--color-danger)' : 'var(--color-success, #16a34a)', fontSize: 13 }}>{syncMessage}</p>}
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 8 }}>
            <Button variant="outline" onClick={() => setBatchDialogOpen(false)}>取消</Button>
            <Button disabled={!syncPlanID || syncBusy} busy={syncBusy} onClick={() => void runSync()}>{syncBusy ? '保存中...' : '保存到套餐'}</Button>
          </div>
        </div>
      </Dialog>
    </div>
  )
}
