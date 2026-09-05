import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Activity, RefreshCw, Search } from 'lucide-react'
import { Select } from '../ui/select'
import type { Server, LatencyProbeRegion } from '../proxy-path/types'
import { ReturnLatencySettings } from './ReturnLatencySettings'

type Client = { request: (path: string, init?: RequestInit) => Promise<any> }
type Outcome = { id: number; name: string; state: 'success' | 'error' | 'skipped'; message: string }

export function ReturnLatencyPage({ servers, client, loading, canManage, onRefresh, renderHistory }: {
  servers: Server[]
  client: Client
  loading?: boolean
  canManage: boolean
  onRefresh: () => void | Promise<unknown>
  renderHistory: (server: Server, onClose: () => void) => ReactNode
}) {
  const [selectedIDs, setSelectedIDs] = useState<Set<number>>(() => {
    const id = Number(new URLSearchParams(window.location.search).get('server'))
    return new Set(id > 0 ? [id] : [])
  })
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState('all')
  const [resource, setResource] = useState<{ regions: LatencyProbeRegion[]; loading: boolean; error: string }>({ regions: [], loading: true, error: '' })
  const [resourceRevision, setResourceRevision] = useState(0)
  const [seed, setSeed] = useState<{ draft: Partial<Server>; revision: number; label: string }>({ draft: {}, revision: 0, label: '新配置：启用自动测试，60 秒一次，默认仅公网目标。' })
  const [historyID, setHistoryID] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const [outcomes, setOutcomes] = useState<Outcome[]>([])
  const [refreshError, setRefreshError] = useState('')
  const selected = servers.filter(server => selectedIDs.has(server.id))
  const online = (server: Server) => Boolean(server.agent_id) && server.status === 'online'
  const eligible = selected.filter(server => online(server) && server.latency_probe_enabled)
  const visible = servers.filter(server => {
    const matches = `${server.name} ${server.id} ${server.public_ipv4 || ''} ${server.public_ipv6 || ''}`.toLowerCase().includes(query.trim().toLowerCase())
    return matches && (filter === 'all' || (filter === 'online' && online(server)) || (filter === 'enabled' && server.latency_probe_enabled) || (filter === 'disabled' && !server.latency_probe_enabled) || (filter === 'selected' && selectedIDs.has(server.id)))
  })
  const visibleSelected = visible.filter(server => selectedIDs.has(server.id)).length
  const allVisibleSelected = visible.length > 0 && visibleSelected === visible.length
  const historyServer = servers.find(server => server.id === historyID)

  useEffect(() => {
    const controller = new AbortController()
    setResource(current => ({ ...current, loading: true, error: '' }))
    void client.request('/latency-probe-resource', { signal: controller.signal }).then(result => {
      if (!controller.signal.aborted) setResource({ regions: result.regions || [], loading: false, error: '' })
    }).catch(error => {
      if (!controller.signal.aborted) setResource(current => ({ ...current, loading: false, error: error?.message || '目标资源加载失败' }))
    })
    return () => controller.abort()
  }, [client, resourceRevision])

  const refresh = async () => {
    setRefreshError('')
    try { await onRefresh() } catch (error: any) { setRefreshError(error?.message || '服务器列表刷新失败') }
  }
  const toggleVisible = () => setSelectedIDs(current => {
    const next = new Set(current)
    visible.forEach(server => allVisibleSelected ? next.delete(server.id) : next.add(server.id))
    return next
  })
  const run = async (kind: 'apply' | 'probe', patch?: Partial<Server>) => {
    if (busyRef.current || !canManage || !selected.length) return
    busyRef.current = true
    setBusy(true)
    setOutcomes([])
    const targets = [...selected]
    try {
      for (const server of targets) {
        let outcome: Outcome
        if (kind === 'probe' && (!online(server) || !server.latency_probe_enabled)) {
          outcome = { id: server.id, name: server.name, state: 'skipped', message: !online(server) ? '已跳过：Agent 未在线' : '已跳过：自动测试未启用' }
        } else {
          try {
            const result = await client.request(`/servers/${server.id}${kind === 'probe' ? '/latency-probe' : ''}`, { method: kind === 'probe' ? 'POST' : 'PATCH', body: JSON.stringify(kind === 'probe' ? {} : patch) })
            outcome = { id: server.id, name: server.name, state: 'success', message: kind === 'apply' ? '配置已保存，等待 Agent 同步' : `${result.existing ? '已有测试任务' : '测试已排队'} #${result.task_id}` }
          } catch (error: any) {
            outcome = { id: server.id, name: server.name, state: 'error', message: error?.message || '操作失败，请重试' }
          }
        }
        setOutcomes(current => [...current, outcome])
      }
      await refresh()
    } finally {
      busyRef.current = false
      setBusy(false)
    }
  }

  return <section className="panel return-latency-page" aria-label="回程延迟管理">
    <div className="panel-body">
      <header className="return-latency-page-head">
        <div><h2>回程延迟</h2><p className="muted">从服务器探测各省份与运营商，统一配置，按需查看历史。</p></div>
        <button type="button" className="ghost" disabled={busy || loading} onClick={() => void refresh()}><RefreshCw size={15} aria-hidden="true" />刷新服务器</button>
      </header>
      {refreshError && <p className="danger-text" role="alert">{refreshError}</p>}
      <div className="return-latency-layout">
        <section className="return-latency-servers" aria-label="选择服务器">
          <header className="return-latency-section-head"><h2>1. 选择服务器</h2><span className="muted">已选 {selected.length} / {servers.length} 台</span></header>
          <div className="return-latency-search"><Search size={15} aria-hidden="true" /><input type="search" aria-label="搜索回程测试服务器" placeholder="搜索名称、IP 或编号" value={query} onChange={event => setQuery(event.target.value)} /></div>
          <Select aria-label="回程测试服务器筛选" value={filter} onChange={event => setFilter(event.target.value)}>
            <option value="all">全部服务器</option><option value="online">Agent 在线</option><option value="enabled">已启用测试</option><option value="disabled">未启用测试</option><option value="selected">仅看已选</option>
          </Select>
          <div className="return-latency-selection">
            <label className="return-latency-check"><input type="checkbox" aria-label="选择当前服务器结果" checked={allVisibleSelected} ref={element => { if (element) element.indeterminate = visibleSelected > 0 && !allVisibleSelected }} disabled={busy || !visible.length || !canManage} onChange={toggleVisible} />当前结果 · {visible.length} 台</label>
            <button type="button" className="ghost" disabled={busy || !selected.length} onClick={() => setSelectedIDs(new Set())}>清空选择</button>
          </div>
          {selected.length > visibleSelected && <p className="muted" role="status">另有 {selected.length - visibleSelected} 台已选服务器不在当前筛选中，仍会参与应用。</p>}
          <div className="return-latency-server-list" aria-busy={loading}>
            {loading && !servers.length ? <p className="muted" role="status">正在加载服务器…</p> : !servers.length ? <p className="muted">暂无服务器，请先在服务器管理中添加。</p> : !visible.length ? <div className="latency-empty-state"><p>没有匹配的服务器</p><button type="button" className="ghost" onClick={() => { setQuery(''); setFilter('all') }}>清除筛选</button></div> : visible.map(server => <article className={`return-latency-server${selectedIDs.has(server.id) ? ' is-selected' : ''}`} key={server.id}>
              <label className="return-latency-server-choice"><input type="checkbox" aria-label={`选择 ${server.name}`} checked={selectedIDs.has(server.id)} disabled={busy || !canManage} onChange={event => setSelectedIDs(current => { const next = new Set(current); event.target.checked ? next.add(server.id) : next.delete(server.id); return next })} /><span><strong>{server.name}</strong><small>{server.public_ipv4 || server.public_ipv6 || `#${server.id}`}</small></span><span className="return-latency-state">{online(server) ? '在线' : server.agent_id ? '离线' : '未接入'}</span></label>
              <p className="muted">{server.latency_probe_enabled ? `${server.latency_probe_mode === 'icmp' ? 'ICMP' : 'TCP'} · ${server.latency_probe_interval_seconds || 60} 秒 · ${server.latency_probe_regions?.length || 0} 个回程目标` : '自动测试已关闭'}</p>
              <div className="return-latency-row-actions"><button type="button" className="ghost" disabled={busy || !canManage} onClick={() => setSeed(current => ({ draft: server, revision: current.revision + 1, label: `配置来源：${server.name}。修改后应用到所选服务器。` }))}>载入此配置</button><button type="button" className="ghost" onClick={() => setHistoryID(server.id)}>查看历史</button></div>
            </article>)}
          </div>
          <div className="return-latency-test-action">
            <button type="button" className="ghost" disabled={busy || !canManage || !eligible.length} onClick={() => void run('probe')}><Activity size={15} aria-hidden="true" />立即测试 {eligible.length} 台</button>
            <p className="muted">使用已保存配置。离线、未接入或未启用测试的服务器将跳过；结果在历史中查看。</p>
          </div>
        </section>
        <div className="return-latency-config">
          <p className="return-latency-source muted">{seed.label}</p>
          {resource.error && <button type="button" className="ghost" disabled={resource.loading} onClick={() => setResourceRevision(current => current + 1)}>重新加载回程目标</button>}
          <ReturnLatencySettings key={seed.revision} draft={seed.draft} regions={resource.regions} loading={resource.loading} error={resource.error} disabled={busy || !canManage} serverCount={selected.length} onSave={patch => run('apply', patch)} />
          {outcomes.length > 0 && <section className="return-latency-outcomes" aria-label="批量操作结果">
            <header className="return-latency-section-head"><h2>{busy ? '正在处理…' : '操作结果'}</h2><span role="status">成功 {outcomes.filter(row => row.state === 'success').length} · 失败 {outcomes.filter(row => row.state === 'error').length} · 跳过 {outcomes.filter(row => row.state === 'skipped').length}</span></header>
            <ul>{outcomes.map(row => <li key={row.id}><strong>{row.name}</strong><span className={row.state === 'error' ? 'danger-text' : 'muted'}>{row.message}</span></li>)}</ul>
            {!busy && outcomes.some(row => row.state === 'error') && <button type="button" className="ghost" onClick={() => { setSelectedIDs(new Set(outcomes.filter(row => row.state === 'error').map(row => row.id))); setFilter('selected') }}>仅选择失败服务器</button>}
          </section>}
        </div>
      </div>
    </div>
    {historyServer && renderHistory(historyServer, () => setHistoryID(null))}
  </section>
}
