import * as React from 'react'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Select } from '../components/ui/select'
import { Input } from '../components/ui/input'
import { PlanNodeOrderingPanel } from '../components/node-ordering/PlanNodeOrderingPanel'
import { NodeScopeMenu, type NodeScopeRequest, type ScopeNode } from '../components/node-assignment/NodeScopeMenu'
import { NodeScopeActionDialog } from '../components/node-assignment/NodeScopeActionDialog'
import { AssignPlanUsersDialog } from '../components/node-assignment/AssignPlanUsersDialog'
import { X, Filter, RefreshCw, ListOrdered, MoreHorizontal, Users } from 'lucide-react'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }

type CatalogNode = ScopeNode & {
  name: string
  entry_server_name?: string
  entry_protocol?: string
  entry_port?: number
  path_summary?: string[]
  enabled: boolean
  status: 'ok' | 'offline' | 'disabled'
  group?: string
  plans: { plan_id: number; name: string; display_group?: string }[]
  effective_users: number
  allow_exceptions: number
  deny_exceptions: number
}

type CatalogResponse = { nodes: CatalogNode[]; total: number; page: number; page_size: number }

type DetailUser = { user_id: number; username: string; nickname?: string; effective: boolean; source?: string; plan_id?: number; plan_name?: string; effect?: string; reason?: string; expires_at?: string }
type DetailResponse = { node: any; users: DetailUser[]; plans: { plan_id: number; name: string; display_group?: string }[]; exceptions: any[]; runtime_authorization_mode: string }

const protocolOptions = ['vless', 'hysteria2', 'anytls', 'shadowsocks', 'mieru', 'socks', 'wireguard']
const statusLabels: Record<string, string> = { ok: '正常', offline: '离线', disabled: '已禁用' }

function nodeTypeLabel(type: string) {
  if (type === 'proxy_path') return '代理链路'
  if (type === 'external_outbound') return '导入节点'
  if (type === 'inbound') return '独立入口'
  return type
}

export function NodeAssignmentsPage({ data, client, load }: { data: any; client: AnyClient; load: () => Promise<void> }) {
  const [tab, setTab] = React.useState<'catalog' | 'ordering'>('catalog')
  const [toast, setToast] = React.useState('')
  const notify = (message: string, tone?: 'success' | 'error' | 'warning') => {
    setToast(message)
    window.setTimeout(() => setToast(''), 4000)
  }
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
  const [assignOpen, setAssignOpen] = React.useState(false)
  const [users, setUsers] = React.useState<any[] | null>(null)

  const servers = data.servers || []
  const plans = data.subscription_plans || []
  const isAdmin = data.session?.role === 'admin'
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
      const plan = await client.request<{ subscription_plan?: { revision: number } }>(`/subscription-plans/${syncPlanID}`)
      const revision = plan.subscription_plan?.revision || 0
      if (!revision) throw new Error('无法获取方案修订号，请刷新后重试')
      const nodeRefs = nodes.filter(n => selected[n.key]).map(n => ({ node_type: n.type, node_id: n.id }))
      await client.request(`/subscription-plans/${syncPlanID}/nodes/sync`, {
        method: 'POST',
        body: JSON.stringify({ op: syncOp, nodes: nodeRefs, expected_revision: revision }),
      })
      setSyncMessage(`已${syncOp === 'add' ? '加入' : syncOp === 'remove' ? '移除' : '替换'} ${nodeRefs.length} 个节点到方案草稿`)
      setSelected({})
      await refresh()
    } catch (e: any) {
      const message = e?.message || String(e)
      setSyncMessage(message.includes('conflict') || message.includes('409') ? '操作失败：方案已发生变化（冲突），请刷新后重试' : '操作失败：' + message)
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
      <div className="panel-head" style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
        <h2 style={{ margin: 0 }}>节点分配</h2>
        <div className="section-toolbar" style={{ gap: 4 }}>
          <Button variant={tab === 'catalog' ? 'default' : 'outline'} size="sm" onClick={() => setTab('catalog')}>节点目录</Button>
          <Button variant={tab === 'ordering' ? 'default' : 'outline'} size="sm" onClick={() => setTab('ordering')}><ListOrdered size={14} /> 订阅排序</Button>
        </div>
      </div>
      <div className="panel-body">
        {toast && <p style={{ margin: '0 0 8px', color: 'var(--color-success, #16a34a)' }}>{toast}</p>}
        {tab === 'ordering' ? (
          <PlanNodeOrderingPanel data={data} client={client} notify={notify} onSaved={() => void load()} />
        ) : (
          <>
            <p className="muted">可分配节点目录由代理链路、导入节点与独立入口统一汇总；右键节点或点击行内「⋯」选择范围后，可加入方案草稿或批量创建用户临时例外。</p>

        <div className="section-toolbar" style={{ flexWrap: 'wrap', gap: 8 }}>
          <Input value={query} onChange={e => setQuery(e.target.value)} placeholder="搜索名称 / 服务器 / 协议 / 地区" style={{ maxWidth: 260 }} />
          <Button variant="outline" size="sm" onClick={() => setFiltersOpen(v => !v)}><Filter size={14} /> 筛选</Button>
          <Button variant="ghost" size="sm" onClick={() => { setQuery(''); setEntryServerID(0); setEntryRegion(''); setExitRegion(''); setProtocol(''); setStatus(''); setPlanID(0); setUnassigned(false); setGroupBy(''); setSort('name'); setPage(1) }}><RefreshCw size={14} /> 重置</Button>
          {isAdmin && (
            <Button variant="outline" size="sm" onClick={() => { setAssignOpen(true); void ensureUsers() }}>
              <Users size={14} /> 将此方案分配给用户
            </Button>
          )}
          <span className="muted" style={{ marginLeft: 'auto' }}>共 {total} 个节点</span>
        </div>

        {filtersOpen && (
          <div className="form" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))' }}>
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
              <option value={0}>方案：全部</option>
              {plans.map((p: any) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </Select>
            <label style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13 }}>
              <input type="checkbox" checked={unassigned} onChange={e => setUnassigned(e.target.checked)} /> 仅看未分配节点
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

        {error && <p style={{ color: 'var(--color-danger)' }}>{error}</p>}

        <div className="card-custom" style={{ marginTop: 12, overflow: 'auto' }}>
          <table className="user-data-table node-catalog-table" style={{ minWidth: 960 }}>
            <thead>
              <tr>
                <th style={{ width: 32 }}><input type="checkbox" checked={nodes.length > 0 && selectedCount === nodes.length} onChange={e => toggleAll(e.target.checked)} aria-label="全选本页" /></th>
                <th>节点</th>
                <th>类型</th>
                <th>入口服务器</th>
                <th>协议</th>
                <th>出口地区</th>
                <th>状态</th>
                <th>所属方案</th>
                <th>有效用户</th>
                <th>例外</th>
                <th style={{ textAlign: 'right' }}>操作</th>
              </tr>
            </thead>
            <tbody>
              {nodes.map(n => (
                <tr key={n.key} className="table-row-hover" onContextMenu={e => { e.preventDefault(); openScopeMenu(n, e.clientX, e.clientY) }}>
                  <td><input type="checkbox" checked={Boolean(selected[n.key])} onChange={e => setSelected(s => ({ ...s, [n.key]: e.target.checked }))} aria-label={`选择 ${n.name}`} /></td>
                  <td style={{ fontWeight: 600 }}>{n.name}{n.group ? <span className="muted" style={{ display: 'block', fontSize: 12 }}>{n.group}</span> : null}</td>
                  <td><Badge variant="secondary">{nodeTypeLabel(n.type)}</Badge></td>
                  <td>{n.entry_server_name || '—'}</td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>{n.entry_protocol || '—'}</td>
                  <td>{n.exit_region ? <Badge variant="outline">{n.exit_region}</Badge> : '—'}</td>
                  <td>
                    <Badge variant={n.status === 'ok' ? 'success' : n.status === 'disabled' ? 'secondary' : 'warning'}>
                      {statusLabels[n.status] || n.status}
                    </Badge>
                  </td>
                  <td>
                    {n.plans.length === 0 ? <span className="muted">未分配</span> : n.plans.map(p => <Badge key={p.plan_id} variant="outline" style={{ marginRight: 4 }}>{p.name}</Badge>)}
                  </td>
                  <td>{n.effective_users}</td>
                  <td>
                    {n.allow_exceptions > 0 && <Badge variant="success" style={{ marginRight: 4 }}>允许 {n.allow_exceptions}</Badge>}
                    {n.deny_exceptions > 0 && <Badge variant="destructive">拒绝 {n.deny_exceptions}</Badge>}
                    {n.allow_exceptions === 0 && n.deny_exceptions === 0 && <span className="muted">—</span>}
                  </td>
                  <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                    <Button variant="outline" size="sm" onClick={() => void openDetail(n)}>详情</Button>
                    <Button variant="ghost" size="icon" onClick={e => {
                      const rect = e.currentTarget.getBoundingClientRect()
                      openScopeMenu(n, rect.right, rect.bottom + 4)
                    }} aria-label={`节点操作 ${n.name}`} title="选择节点范围"><MoreHorizontal size={16} /></Button>
                  </td>
                </tr>
              ))}
              {nodes.length === 0 && !loading && <tr><td colSpan={11} className="muted" style={{ textAlign: 'center', padding: 24 }}>没有匹配的节点</td></tr>}
            </tbody>
          </table>
          {loading && <p className="muted" style={{ padding: 12 }}>加载中...</p>}
        </div>

        <div className="section-toolbar" style={{ marginTop: 12, flexWrap: 'wrap' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage(p => Math.max(1, p - 1))}>上一页</Button>
            <span className="muted">第 {page} / {totalPages} 页</span>
            <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={() => setPage(p => Math.min(totalPages, p + 1))}>下一页</Button>
          </div>
          {isAdmin && selectedCount > 0 && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginLeft: 'auto', flexWrap: 'wrap' }}>
              <span className="muted">已选 {selectedCount} 个节点</span>
              <Select value={syncPlanID} onChange={e => setSyncPlanID(Number(e.target.value))} style={{ minWidth: 160 }} aria-label="选择方案">
                <option value={0}>选择方案</option>
                {plans.map((p: any) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </Select>
              <Select value={syncOp} onChange={e => setSyncOp(e.target.value as 'add' | 'remove' | 'replace')} aria-label="操作类型">
                <option value="add">加入草稿</option>
                <option value="remove">从草稿移除</option>
                <option value="replace">替换草稿</option>
              </Select>
              <Button size="sm" disabled={!syncPlanID || syncBusy} onClick={() => void runSync()}>{syncBusy ? '同步中...' : '同步到方案草稿'}</Button>
            </div>
          )}
          {!isAdmin && <span className="muted" style={{ marginLeft: 'auto' }}>方案草稿修改、临时例外与方案分配需要管理员权限。</span>}
        </div>
            {syncMessage && <p style={{ marginTop: 8, color: syncMessage.startsWith('操作失败') ? 'var(--color-danger)' : 'var(--color-success, #16a34a)' }}>{syncMessage}</p>}
          </>
        )}
      </div>

      <Dialog isOpen={detail !== null} onClose={() => setDetail(null)} title={detail ? detail.node?.name || '节点详情' : ''} size="lg">
        {detail && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
            <div className="form" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(160px, 1fr))' }}>
              <div><span className="muted">类型</span><div>{nodeTypeLabel(detail.node?.type)}</div></div>
              <div><span className="muted">入口</span><div>{detail.node?.entry_server_name || '—'}</div></div>
              <div><span className="muted">协议</span><div style={{ fontFamily: 'var(--font-mono)' }}>{detail.node?.entry_protocol || '—'}</div></div>
              <div><span className="muted">出口地区</span><div>{detail.node?.exit_region || '—'}</div></div>
              <div><span className="muted">状态</span><div>{statusLabels[detail.node?.status] || detail.node?.status}</div></div>
              <div><span className="muted">路径</span><div>{(detail.node?.path_summary || []).join(' → ') || '—'}</div></div>
            </div>
            <div>
              <h3 style={{ marginTop: 0 }}>所属方案</h3>
              {detail.plans?.length === 0 ? <p className="muted">未分配任何方案</p> : detail.plans?.map((p: any) => (
                <Badge key={p.plan_id} variant="outline" style={{ marginRight: 6 }}>{p.name}{p.display_group ? ` · ${p.display_group}` : ''}</Badge>
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
                          {u.source === 'plan' && <Badge variant="secondary">{u.plan_name || '方案'}</Badge>}
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

      <AssignPlanUsersDialog
        open={assignOpen}
        defaultPlanID={planID}
        plans={plans}
        users={users || []}
        client={client}
        notify={notify}
        onClose={() => setAssignOpen(false)}
        onDone={refresh}
      />
    </div>
  )
}
