import * as React from 'react'
import './node-workspace.css'
import './node-signal.css'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { Select } from '../components/ui/select'
import { Switch } from '../components/ui/switch'
import { NodeScopeMenu, type NodeScopeRequest } from '../components/node-assignment/NodeScopeMenu'
import { NodeScopeActionDialog } from '../components/node-assignment/NodeScopeActionDialog'
import { NodeRenameDialog } from '../components/node-assignment/NodeRenameDialog'
import { NodeDetailDrawer, type NodeDetail } from '../components/node-assignment/NodeDetailDrawer'
import { NodeBatchPlanDialog } from '../components/node-assignment/NodeBatchPlanDialog'
import { catalogProtocols, nodeStatusLabels, nodeStatusVariant, nodeTypeLabel, type CatalogNode } from '../components/node-assignment/catalog'
import { ChevronLeft, ChevronRight, Info, ListChecks, MoreHorizontal, Pencil, RotateCcw, Search, SlidersHorizontal, X } from 'lucide-react'
import { hasManagementAccess } from '../permissions'
import { useRegisterPageRefresh } from '../page-refresh-context'

type AnyClient = { request<T = any>(path: string, init?: RequestInit): Promise<T> }
type CatalogResponse = { nodes: CatalogNode[]; total: number; page: number; page_size: number }

type Filters = {
  entryServerID: number
  entryRegion: string
  exitRegion: string
  protocol: string
  status: string
  planID: number
  unassigned: boolean
  groupBy: string
  sort: string
}

const emptyFilters: Filters = { entryServerID: 0, entryRegion: '', exitRegion: '', protocol: '', status: '', planID: 0, unassigned: false, groupBy: '', sort: 'name' }
const groupByLabels: Record<string, string> = { entry_server: '按入口服务器', exit_region: '按出口地区' }
const sortLabels: Record<string, string> = { name: '名称', entry_server: '入口服务器', exit_region: '出口地区', users: '用户数', status: '状态' }

function PlanChips({ plans }: { plans: Array<{ plan_id: number; name: string }> }) {
  if (!plans || plans.length === 0) return <span className="nw-unassigned">未加入套餐</span>
  const visible = plans.slice(0, 2)
  const rest = plans.slice(2)
  return <div className="nw-chips">
    {visible.map(plan => <span key={plan.plan_id} className="nw-chip">{plan.name}</span>)}
    {rest.length > 0 && <span className="nw-chip nw-chip-more" tabIndex={0} title={rest.map(plan => plan.name).join('、')} aria-label={`其余 ${rest.length} 个套餐：${rest.map(plan => plan.name).join('、')}`}>+{rest.length}</span>}
  </div>
}

function UserCount({ node }: { node: CatalogNode }) {
  return <div className="nw-user-count">
    <span><b>{node.effective_users}</b> 人</span>
    {node.allow_exceptions > 0 && <Badge variant="success" title="允许例外">+{node.allow_exceptions}</Badge>}
    {node.deny_exceptions > 0 && <Badge variant="destructive" title="拒绝例外">−{node.deny_exceptions}</Badge>}
  </div>
}

function NodeIdentity({ node, showGroup }: { node: CatalogNode; showGroup: boolean }) {
  const meta = [nodeTypeLabel(node.type), node.has_global_name_override ? `来源名称：${node.source_name}` : '', showGroup ? node.group || '' : ''].filter(Boolean)
  return <>
    <strong className="nw-cell-title">{node.name}</strong>
    <span className="nw-cell-sub">{meta.join(' · ')}</span>
  </>
}

export function NodeAssignmentsPage({ data, client, load, notify }: {
  data: any
  client: AnyClient
  load: () => Promise<void>
  notify?: (message: string, tone?: 'success' | 'error' | 'warning') => void
}) {
  const [query, setQuery] = React.useState('')
  const [filters, setFilters] = React.useState<Filters>(emptyFilters)
  const [page, setPage] = React.useState(1)
  const [pageSize, setPageSize] = React.useState(50)
  const [nodes, setNodes] = React.useState<CatalogNode[]>([])
  const [total, setTotal] = React.useState(0)
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState('')
  const [detail, setDetail] = React.useState<NodeDetail | null>(null)
  const [selected, setSelected] = React.useState<Record<string, boolean>>({})
  const [filtersOpen, setFiltersOpen] = React.useState(false)
  const [menu, setMenu] = React.useState<{ x: number; y: number; node: CatalogNode } | null>(null)
  const [scopeAction, setScopeAction] = React.useState<{ node: CatalogNode; scope: NodeScopeRequest } | null>(null)
  const [users, setUsers] = React.useState<any[] | null>(null)
  const [renameNode, setRenameNode] = React.useState<CatalogNode | null>(null)
  const [batchOpen, setBatchOpen] = React.useState(false)
  const nodeRead = React.useRef(0)
  const detailRead = React.useRef(0)
  const filterKey = JSON.stringify([query, filters, pageSize])
  const previousFilter = React.useRef(filterKey)
  const currentQuery = React.useRef('')
  currentQuery.current = `${filterKey}:${page}`

  const servers = data.servers || []
  const plans = data.subscription_plans || []
  const isAdmin = hasManagementAccess(data.session?.role)
  const regionCodes = Array.from(new Set(servers.flatMap((s: any) => [s.region_code, s.detected_region_code]).filter((x: any) => Boolean(x)))).sort() as string[]
  const setFilter = <K extends keyof Filters>(key: K, value: Filters[K]) => setFilters(current => ({ ...current, [key]: value }))

  const activeFilters: Array<{ key: keyof Filters; label: string }> = []
  if (filters.entryServerID) activeFilters.push({ key: 'entryServerID', label: `入口：${servers.find((s: any) => s.id === filters.entryServerID)?.name || `#${filters.entryServerID}`}` })
  if (filters.entryRegion) activeFilters.push({ key: 'entryRegion', label: `入口地区：${filters.entryRegion}` })
  if (filters.exitRegion) activeFilters.push({ key: 'exitRegion', label: `出口地区：${filters.exitRegion}` })
  if (filters.protocol) activeFilters.push({ key: 'protocol', label: `协议：${filters.protocol}` })
  if (filters.status) activeFilters.push({ key: 'status', label: `状态：${nodeStatusLabels[filters.status] || filters.status}` })
  if (filters.planID) activeFilters.push({ key: 'planID', label: `套餐：${plans.find((p: any) => p.id === filters.planID)?.name || `#${filters.planID}`}` })
  if (filters.unassigned) activeFilters.push({ key: 'unassigned', label: '仅未加入套餐' })
  if (filters.groupBy) activeFilters.push({ key: 'groupBy', label: groupByLabels[filters.groupBy] || filters.groupBy })
  if (filters.sort !== 'name') activeFilters.push({ key: 'sort', label: `排序：${sortLabels[filters.sort] || filters.sort}` })
  const anyFilter = activeFilters.length > 0 || Boolean(query)

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
    const read = ++nodeRead.current
    const requestedQuery = currentQuery.current
    const isCurrent = () => read === nodeRead.current && requestedQuery === currentQuery.current
    setLoading(true)
    setError('')
    try {
      const params = new URLSearchParams()
      if (query) params.set('query', query)
      if (filters.entryServerID) params.set('entry_server_id', String(filters.entryServerID))
      if (filters.entryRegion) params.set('entry_region', filters.entryRegion)
      if (filters.exitRegion) params.set('exit_region', filters.exitRegion)
      if (filters.protocol) params.set('protocol', filters.protocol)
      if (filters.status) params.set('status', filters.status)
      if (filters.planID) params.set('plan_id', String(filters.planID))
      if (filters.unassigned) params.set('unassigned', '1')
      if (filters.groupBy) params.set('group_by', filters.groupBy)
      if (filters.sort) params.set('sort', filters.sort)
      params.set('page', String(nextPage))
      params.set('page_size', String(pageSize))
      const res = await client.request<CatalogResponse>('/assignable-nodes?' + params.toString())
      if (!isCurrent()) return
      setNodes(res.nodes || [])
      setTotal(res.total || 0)
      const visibleKeys = new Set((res.nodes || []).map(node => node.key))
      setSelected(current => Object.fromEntries(Object.entries(current).filter(([key, checked]) => checked && visibleKeys.has(key))))
    } catch (e: any) {
      if (isCurrent()) setError(e?.message || String(e))
    } finally {
      if (isCurrent()) setLoading(false)
    }
  }, [client, query, filters, pageSize, page])

  React.useEffect(() => {
    const filtersChanged = previousFilter.current !== filterKey
    previousFilter.current = filterKey
    if (filtersChanged && page !== 1) { setPage(1); return }
    void loadNodes(page)
  }, [loadNodes, filterKey, page])

  const openDetail = async (node: CatalogNode) => {
    const read = ++detailRead.current
    try {
      const res = await client.request<NodeDetail>(`/assignable-nodes/${node.type}/${node.id}`)
      if (read === detailRead.current) setDetail(res)
    } catch (e: any) {
      if (read === detailRead.current) setError(e?.message || String(e))
    }
  }

  const selectedNodes = nodes.filter(node => selected[node.key])
  const selectedCount = selectedNodes.length
  const allSelected = nodes.length > 0 && selectedCount === nodes.length
  const toggleAll = (checked: boolean) => setSelected(checked ? Object.fromEntries(nodes.map(node => [node.key, true])) : {})
  const toggleNode = (node: CatalogNode, checked: boolean) => setSelected(current => ({ ...current, [node.key]: checked }))

  const refresh = async () => {
    await load()
    await loadNodes(page)
  }
  const detailRef = React.useRef(detail)
  detailRef.current = detail
  useRegisterPageRefresh(async () => {
    await loadNodes(page)
    const current = detailRef.current
    if (!current?.node?.type || !current.node.id) return
    try {
      const read = ++detailRead.current
      const updated = await client.request<NodeDetail>(`/assignable-nodes/${current.node.type}/${current.node.id}`)
      if (read === detailRead.current && detailRef.current === current) setDetail(updated)
    } catch {
      // Keep the open detail until the next successful load.
    }
  })

  const openScopeMenu = (node: CatalogNode, x: number, y: number) => setMenu({ x, y, node })
  const handleScopeSelect = (scope: NodeScopeRequest) => {
    if (!menu) return
    setScopeAction({ node: menu.node, scope })
    void ensureUsers()
  }
  const resetFilters = () => { setQuery(''); setFilters(emptyFilters); setPage(1) }

  const totalPages = Math.max(1, Math.ceil(total / pageSize))
  const grouped = Boolean(filters.groupBy)
  const rowActions = (node: CatalogNode, labelled = false) => <div className="nw-row-actions">
    {isAdmin && <button type="button" className="ghost icon-button" onClick={() => setRenameNode(node)} aria-label={`重命名 ${node.name}`} title="修改全局名称"><Pencil size={15} />{labelled && <span>重命名</span>}</button>}
    <button type="button" className="ghost icon-button" onClick={() => void openDetail(node)} aria-label={`查看详情 ${node.name}`} title="查看详情"><Info size={15} />{labelled && <span>详情</span>}</button>
    <button type="button" className="ghost icon-button" onClick={event => { const rect = event.currentTarget.getBoundingClientRect(); openScopeMenu(node, rect.right, rect.bottom + 4) }} aria-label={`节点操作 ${node.name}`} title="按范围授权或调整套餐"><MoreHorizontal size={15} />{labelled && <span>操作</span>}</button>
  </div>
  const groupStarts = (index: number) => grouped && (index === 0 || (nodes[index - 1].group || '') !== (nodes[index].group || ''))
  const groupSize = (group: string) => nodes.filter(node => (node.group || '') === group).length

  return <div className="nw-catalog">
    <div className="nw-toolbar">
      <label className="nw-query">
        <Search size={15} aria-hidden="true" />
        <input type="search" value={query} onChange={e => setQuery(e.target.value)} placeholder="搜索名称、服务器、协议或地区" aria-label="搜索节点" />
        {query && <button type="button" className="ghost icon-button" onClick={() => setQuery('')} aria-label="清除搜索" title="清除搜索"><X size={13} /></button>}
      </label>
      <button type="button" className={`ghost nw-filter-toggle${filtersOpen ? ' is-open' : ''}`} onClick={() => setFiltersOpen(v => !v)} aria-expanded={filtersOpen} aria-controls="node-catalog-filters">
        <SlidersHorizontal size={15} aria-hidden="true" />筛选与排序{activeFilters.length > 0 && <span className="nw-tab-count">{activeFilters.length}</span>}
      </button>
      <span className="nw-toolbar-meta">共 {total} 个节点</span>
    </div>

    {filtersOpen && <div className="nw-filter-panel" id="node-catalog-filters" role="group" aria-label="筛选与排序">
      <fieldset>
        <legend>筛选</legend>
        <label><span>入口服务器</span><Select aria-label="入口服务器" value={filters.entryServerID} onChange={e => setFilter('entryServerID', Number(e.target.value))}>
          <option value={0}>全部</option>
          {servers.map((s: any) => <option key={s.id} value={s.id}>{s.name}</option>)}
        </Select></label>
        <label><span>入口地区</span><Select aria-label="入口地区" value={filters.entryRegion} onChange={e => setFilter('entryRegion', e.target.value)}>
          <option value="">全部</option>
          {regionCodes.map(r => <option key={r} value={r}>{r}</option>)}
        </Select></label>
        <label><span>出口地区</span><Select aria-label="出口地区" value={filters.exitRegion} onChange={e => setFilter('exitRegion', e.target.value)}>
          <option value="">全部</option>
          {regionCodes.map(r => <option key={r} value={r}>{r}</option>)}
        </Select></label>
        <label><span>协议</span><Select aria-label="节点协议" value={filters.protocol} onChange={e => setFilter('protocol', e.target.value)}>
          <option value="">全部</option>
          {catalogProtocols.map(p => <option key={p} value={p}>{p}</option>)}
        </Select></label>
        <label><span>状态</span><Select aria-label="节点状态" value={filters.status} onChange={e => setFilter('status', e.target.value)}>
          <option value="">全部</option>
          {Object.entries(nodeStatusLabels).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
        </Select></label>
        <label><span>所属套餐</span><Select aria-label="所属套餐" value={filters.planID} onChange={e => setFilter('planID', Number(e.target.value))} disabled={filters.unassigned}>
          <option value={0}>全部</option>
          {plans.map((p: any) => <option key={p.id} value={p.id}>{p.name}</option>)}
        </Select></label>
        <label className="nw-switch-field"><Switch size="sm" checked={filters.unassigned} onChange={value => setFilters(current => ({ ...current, unassigned: value, planID: value ? 0 : current.planID }))} ariaLabel="仅看未加入套餐的节点" /><span>仅看未加入套餐的节点</span></label>
      </fieldset>
      <fieldset>
        <legend>排列</legend>
        <label><span>分组</span><Select aria-label="分组方式" value={filters.groupBy} onChange={e => setFilter('groupBy', e.target.value)}>
          <option value="">不分组</option>
          {Object.entries(groupByLabels).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
        </Select></label>
        <label><span>排序</span><Select aria-label="排序方式" value={filters.sort} onChange={e => setFilter('sort', e.target.value)}>
          {Object.entries(sortLabels).map(([v, l]) => <option key={v} value={v}>{l}</option>)}
        </Select></label>
      </fieldset>
    </div>}

    {anyFilter && <div className="nw-active-filters" aria-label="已应用的筛选">
      {query && <button type="button" className="nw-filter-chip" onClick={() => setQuery('')} aria-label={`移除搜索 ${query}`}>搜索：{query}<X size={12} aria-hidden="true" /></button>}
      {activeFilters.map(item => <button type="button" key={item.key} className="nw-filter-chip" onClick={() => setFilter(item.key, emptyFilters[item.key])} aria-label={`移除筛选 ${item.label}`}>{item.label}<X size={12} aria-hidden="true" /></button>)}
      <button type="button" className="ghost nw-filter-reset" onClick={resetFilters}><RotateCcw size={13} aria-hidden="true" />全部清除</button>
    </div>}

    {isAdmin && selectedCount > 0 && <div className="nw-batch-bar" role="region" aria-label="批量操作">
      <span>已选 <b>{selectedCount}</b> 个节点</span>
      <Button size="sm" onClick={() => setBatchOpen(true)}><ListChecks size={14} />批量调整套餐</Button>
      <Button variant="ghost" size="sm" onClick={() => setSelected({})}>清除选择</Button>
    </div>}

    {error && <div className="nw-alert" role="alert">{error}</div>}

    <div className={`nw-table-wrap nw-catalog-table-wrap${loading ? ' is-loading' : ''}`} aria-busy={loading || undefined}>
      <table className="nw-table nw-catalog-table">
        <colgroup>
          <col className="nw-col-check" />
          <col className="nw-col-node" />
          <col className="nw-col-entry" />
          <col className="nw-col-status" />
          <col className="nw-col-plans" />
          <col className="nw-col-users" />
          <col className="nw-col-actions" />
        </colgroup>
        <thead>
          <tr>
            <th><input type="checkbox" checked={allSelected} onChange={e => toggleAll(e.target.checked)} aria-label="全选本页" /></th>
            <th>节点</th>
            <th>入口</th>
            <th>状态</th>
            <th>所属套餐</th>
            <th>授权用户</th>
            <th><span className="sr-only">操作</span></th>
          </tr>
        </thead>
        <tbody>
          {nodes.map((node, index) => <React.Fragment key={node.key}>
            {groupStarts(index) && <tr className="nw-group-row"><th colSpan={7} scope="colgroup">{node.group || '未分组'}<span>{groupSize(node.group || '')}</span></th></tr>}
            <tr className={selected[node.key] ? 'is-selected' : undefined} onContextMenu={e => { e.preventDefault(); openScopeMenu(node, e.clientX, e.clientY) }}>
              <td><input type="checkbox" checked={Boolean(selected[node.key])} onChange={e => toggleNode(node, e.target.checked)} aria-label={`选择 ${node.name}`} /></td>
              <td><NodeIdentity node={node} showGroup={!grouped} /></td>
              <td><span className="nw-cell-title nw-cell-plain">{node.entry_server_name || '—'}</span><span className="nw-cell-sub nw-mono">{node.entry_protocol || '—'}{node.entry_port ? `:${node.entry_port}` : ''}</span></td>
              <td><Badge variant={nodeStatusVariant(node.status)}>{nodeStatusLabels[node.status] || node.status}</Badge></td>
              <td><PlanChips plans={node.plans} /></td>
              <td><UserCount node={node} /></td>
              <td>{rowActions(node)}</td>
            </tr>
          </React.Fragment>)}
          {nodes.length === 0 && !loading && <tr><td colSpan={7} className="nw-table-empty">{anyFilter ? '没有符合条件的节点' : '还没有可分配的节点。添加入口、代理拓扑或导入节点后会出现在这里。'}</td></tr>}
        </tbody>
      </table>
    </div>

    <div className="nw-card-list" aria-label="节点列表">
      {nodes.length > 0 && <label className="nw-card-select-all"><input type="checkbox" checked={allSelected} onChange={e => toggleAll(e.target.checked)} />全选本页<span>{selectedCount} 个已选</span></label>}
      {nodes.map((node, index) => <React.Fragment key={node.key}>
        {groupStarts(index) && <h4 className="nw-card-group">{node.group || '未分组'}<span>{groupSize(node.group || '')}</span></h4>}
        <article className={`nw-node-card${selected[node.key] ? ' is-selected' : ''}`}>
          <header>
            <input type="checkbox" checked={Boolean(selected[node.key])} onChange={e => toggleNode(node, e.target.checked)} aria-label={`选择 ${node.name}`} />
            <div><NodeIdentity node={node} showGroup={!grouped} /></div>
            <Badge variant={nodeStatusVariant(node.status)}>{nodeStatusLabels[node.status] || node.status}</Badge>
          </header>
          <dl className="nw-facts">
            <div><dt>入口</dt><dd>{node.entry_server_name || '—'} <span className="nw-mono">{node.entry_protocol || ''}{node.entry_port ? `:${node.entry_port}` : ''}</span></dd></div>
            <div><dt>授权用户</dt><dd><UserCount node={node} /></dd></div>
            <div><dt>所属套餐</dt><dd><PlanChips plans={node.plans} /></dd></div>
          </dl>
          {rowActions(node, true)}
        </article>
      </React.Fragment>)}
      {nodes.length === 0 && !loading && <div className="nw-empty"><span>{anyFilter ? '没有符合条件的节点' : '还没有可分配的节点'}</span></div>}
    </div>

    <footer className="nw-pager">
      <span className="nw-muted">第 {page} / {totalPages} 页</span>
      {!isAdmin && <span className="nw-muted">调整套餐、授权用户需要管理员权限。</span>}
      <div className="nw-pager-controls">
        <label className="nw-inline-field"><span>每页</span><Select aria-label="每页节点数" value={pageSize} onChange={e => setPageSize(Number(e.target.value))}>
          {[25, 50, 100, 200].map(size => <option key={size} value={size}>{size}</option>)}
        </Select></label>
        <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage(p => Math.max(1, p - 1))} aria-label="上一页"><ChevronLeft size={14} aria-hidden="true" />上一页</Button>
        <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={() => setPage(p => Math.min(totalPages, p + 1))} aria-label="下一页">下一页<ChevronRight size={14} aria-hidden="true" /></Button>
      </div>
    </footer>

    <NodeDetailDrawer detail={detail} onClose={() => { detailRead.current++; setDetail(null) }} />

    {menu && <NodeScopeMenu x={menu.x} y={menu.y} node={menu.node} onSelect={handleScopeSelect} onClose={() => setMenu(null)} />}

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

    <NodeRenameDialog node={renameNode} client={client} onClose={() => setRenameNode(null)} onSaved={async () => { await refresh(); notify?.('全局节点名称已更新', 'success') }} />

    <NodeBatchPlanDialog open={batchOpen} nodes={selectedNodes} plans={plans} client={client} notify={notify} onClose={() => setBatchOpen(false)} onDone={async () => { setSelected({}); setBatchOpen(false); await refresh() }} />
  </div>
}
