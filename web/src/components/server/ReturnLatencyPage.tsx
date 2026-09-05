import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Activity, History, Pencil, Plus, RefreshCw, Search, Settings2, Trash2 } from 'lucide-react'
import { Dialog } from '../ui/dialog'
import { Select } from '../ui/select'
import type { LatencyProbeAddress, LatencyProbeRegion, LatencyProbeTask, Server } from '../proxy-path/types'
import { ReturnLatencySettings } from './ReturnLatencySettings'
import { ReturnLatencyTaskForm, targetLabel } from './ReturnLatencyTaskForm'

type Client = { request: (path: string, init?: RequestInit) => Promise<any> }
type Notice = { kind: 'success' | 'error'; text: string }

const formatInterval = (seconds: number) => {
  if (!seconds) return '未设置'
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`
  return `${seconds} 秒`
}

export function ReturnLatencyPage({ servers, client, loading, canManage, onRefresh, renderHistory }: {
  servers: Server[]
  client: Client
  loading?: boolean
  canManage: boolean
  onRefresh: () => void | Promise<unknown>
  renderHistory: (server: Server, onClose: () => void) => ReactNode
}) {
  const [tasks, setTasks] = useState<LatencyProbeTask[]>([])
  const [tasksState, setTasksState] = useState<{ loading: boolean; error: string }>({ loading: true, error: '' })
  const [resource, setResource] = useState<{ regions: LatencyProbeRegion[]; targets: LatencyProbeAddress[]; loading: boolean; error: string }>({ regions: [], targets: [], loading: true, error: '' })
  const [resourceRevision, setResourceRevision] = useState(0)
  const [taskRevision, setTaskRevision] = useState(0)
  const [editing, setEditing] = useState<{ open: boolean; task: LatencyProbeTask | null }>({ open: false, task: null })
  const [settingsServer, setSettingsServer] = useState<Server | null>(null)
  const [historyID, setHistoryID] = useState<number | null>(null)
  const [view, setView] = useState<'targets' | 'nodes'>('targets')
  const [taskQuery, setTaskQuery] = useState('')
  const [methodFilter, setMethodFilter] = useState('all')
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState('all')
  const [notice, setNotice] = useState<Notice | null>(null)
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)

  const online = useCallback((server: Server) => Boolean(server.agent_id) && server.status === 'online', [])
  const historyServer = servers.find(server => server.id === historyID)
  const serverByID = useMemo(() => new Map(servers.map(server => [server.id, server])), [servers])

  useEffect(() => {
    const controller = new AbortController()
    setResource(current => ({ ...current, loading: true, error: '' }))
    void client.request('/latency-probe-resource', { signal: controller.signal }).then(result => {
      if (!controller.signal.aborted) setResource({ regions: result.regions || [], targets: result.targets || [], loading: false, error: '' })
    }).catch(error => {
      if (!controller.signal.aborted) setResource(current => ({ ...current, loading: false, error: error?.message || '目标资源加载失败' }))
    })
    return () => controller.abort()
  }, [client, resourceRevision])

  useEffect(() => {
    const controller = new AbortController()
    setTasksState({ loading: true, error: '' })
    void client.request('/latency-probe-tasks', { signal: controller.signal }).then(result => {
      if (controller.signal.aborted) return
      setTasks(result.latency_probe_tasks || [])
      setTasksState({ loading: false, error: '' })
    }).catch(error => {
      if (!controller.signal.aborted) setTasksState({ loading: false, error: error?.message || '探测任务加载失败' })
    })
    return () => controller.abort()
  }, [client, taskRevision])

  const reloadTasks = () => setTaskRevision(current => current + 1)
  const guard = async (action: () => Promise<void>) => {
    if (busyRef.current) return
    busyRef.current = true
    setBusy(true)
    try { await action() } finally { busyRef.current = false; setBusy(false) }
  }

  const saveTask = async (payload: Omit<LatencyProbeTask, 'id' | 'created_at' | 'updated_at'>) => {
    const target = editing.task
    await client.request(target ? `/latency-probe-tasks/${target.id}` : '/latency-probe-tasks', {
      method: target ? 'PATCH' : 'POST',
      body: JSON.stringify(payload),
    })
    setEditing({ open: false, task: null })
    setNotice({ kind: 'success', text: target ? '任务已更新，等待 Agent 同步。' : '任务已创建，等待 Agent 同步。' })
    reloadTasks()
  }

  const deleteTask = (task: LatencyProbeTask) => void guard(async () => {
    try {
      await client.request(`/latency-probe-tasks/${task.id}`, { method: 'DELETE' })
      setNotice({ kind: 'success', text: `已删除任务「${task.name}」。` })
      reloadTasks()
    } catch (error: any) {
      setNotice({ kind: 'error', text: error?.message || '删除失败，请重试' })
    }
  })

  const toggleTask = (task: LatencyProbeTask) => void guard(async () => {
    try {
      await client.request(`/latency-probe-tasks/${task.id}`, { method: 'PATCH', body: JSON.stringify({ enabled: !task.enabled }) })
      reloadTasks()
    } catch (error: any) {
      setNotice({ kind: 'error', text: error?.message || '操作失败，请重试' })
    }
  })

  const saveServerSettings = async (server: Server, patch: Partial<Server>) => {
    await client.request(`/servers/${server.id}`, { method: 'PATCH', body: JSON.stringify(patch) })
    setSettingsServer(null)
    setNotice({ kind: 'success', text: `已保存 ${server.name} 的探测参数。` })
    await onRefresh()
  }

  const probeNow = (server: Server) => void guard(async () => {
    try {
      const result = await client.request(`/servers/${server.id}/latency-probe`, { method: 'POST', body: JSON.stringify({}) })
      setNotice({ kind: 'success', text: `${server.name}：${result.existing ? '已有探测任务' : '探测已排队'} #${result.task_id}` })
    } catch (error: any) {
      setNotice({ kind: 'error', text: `${server.name}：${error?.message || '探测失败，请重试'}` })
    }
  })

  const runAll = () => void guard(async () => {
    const eligible = servers.filter(server => online(server) && server.latency_probe_enabled && tasks.some(task => task.enabled && task.server_ids.includes(server.id)))
    if (!eligible.length) {
      setNotice({ kind: 'error', text: '没有可立即执行的服务器：请确认 Agent 在线、已启用自动探测，并已被任务选中。' })
      return
    }
    let ok = 0
    let failed = 0
    for (const server of eligible) {
      try { await client.request(`/servers/${server.id}/latency-probe`, { method: 'POST', body: JSON.stringify({}) }); ok += 1 } catch { failed += 1 }
    }
    setNotice({ kind: failed ? 'error' : 'success', text: `已触发 ${ok} 台服务器的探测${failed ? `，${failed} 台失败` : ''}。` })
    await onRefresh()
  })

  const taskCountByServer = useMemo(() => {
    const counts = new Map<number, number>()
    tasks.forEach(task => task.server_ids.forEach(id => counts.set(id, (counts.get(id) || 0) + 1)))
    return counts
  }, [tasks])

  const visibleServers = servers.filter(server => {
    const matches = `${server.name} ${server.id} ${server.public_ipv4 || ''} ${server.public_ipv6 || ''}`.toLowerCase().includes(query.trim().toLowerCase())
    if (!matches) return false
    if (filter === 'online') return online(server)
    if (filter === 'enabled') return server.latency_probe_enabled
    if (filter === 'disabled') return !server.latency_probe_enabled
    if (filter === 'assigned') return (taskCountByServer.get(server.id) || 0) > 0
    return true
  })

  const visibleTasks = tasks.filter(task => (methodFilter === 'all' || task.method === methodFilter) && `${task.name} ${task.address} ${task.province} ${task.carrier}`.toLowerCase().includes(taskQuery.trim().toLowerCase()))
  return <section className="panel return-latency-page" aria-label="网络探测">
    <div className="panel-body">
      <div className="return-latency-toolbar">
        <button type="button" className="primary" disabled={!canManage} onClick={() => setEditing({ open: true, task: null })}><Plus size={15} aria-hidden="true" />创建探测任务</button>
        <div className="return-latency-toolbar-secondary">
          <button type="button" className="ghost" disabled={busy || !canManage || !tasks.length} onClick={runAll}><Activity size={15} aria-hidden="true" />立即探测</button>
          <button type="button" className="ghost" disabled={busy || loading} onClick={() => { void onRefresh(); reloadTasks() }}><RefreshCw size={15} aria-hidden="true" />刷新</button>
        </div>
      </div>
      {notice && <p className={notice.kind === 'error' ? 'danger-text' : 'muted'} role="status">{notice.text}</p>}
      {resource.error && <p className="muted" role="status">预设目标暂不可用，仍可手动创建任务。 <button type="button" className="ghost" disabled={resource.loading} onClick={() => setResourceRevision(current => current + 1)}>重新加载</button></p>}

      <div className="network-probe-tabs" role="tablist" aria-label="网络探测视图">
        {(['targets', 'nodes'] as const).map((tab, index) => <button key={tab} id={`probe-${tab}-tab`} type="button" role="tab" aria-selected={view === tab} aria-controls={`probe-${tab}-panel`} tabIndex={view === tab ? 0 : -1} onClick={() => setView(tab)} onKeyDown={event => {
          if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
          event.preventDefault()
          const next = event.key === 'Home' ? 'targets' : event.key === 'End' ? 'nodes' : index === 0 ? 'nodes' : 'targets'
          setView(next); document.getElementById(`probe-${next}-tab`)?.focus()
        }}>{tab === 'targets' ? '目标' : '节点'}<span>{tab === 'targets' ? tasks.length : servers.length}</span></button>)}
      </div>
      {view === 'targets' && <section id="probe-targets-panel" role="tabpanel" aria-labelledby="probe-targets-tab" className="return-latency-tasks">
        <div className="return-latency-filters">
          <div className="return-latency-search"><Search size={15} aria-hidden="true" /><input type="search" aria-label="搜索探测任务" placeholder="搜索名称或目标地址" value={taskQuery} onChange={event => setTaskQuery(event.target.value)} /></div>
          <Select aria-label="探测方式筛选" value={methodFilter} onChange={event => setMethodFilter(event.target.value)}><option value="all">全部方式</option><option value="tcp">TCP</option><option value="icmp">Ping</option><option value="http">HTTP</option></Select>
        </div>
        <header className="return-latency-section-head">
          <h2>探测任务</h2>
          <span className="muted">{tasks.length} 个任务 · 每个任务一个目标</span>
        </header>
        {tasksState.error && <p className="danger-text" role="alert">{tasksState.error}</p>}
        {tasksState.loading && !tasks.length ? <p className="muted" role="status">正在加载探测任务…</p> : !tasks.length ? <div className="latency-empty-state">
          <p>还没有探测任务</p>
          <p className="muted">填写目标地址，选择 TCP、Ping 或 HTTP，再指定执行节点和探测间隔。</p>
          <button type="button" className="primary" disabled={!canManage} onClick={() => setEditing({ open: true, task: null })}><Plus size={15} aria-hidden="true" />创建探测任务</button>
        </div> : !visibleTasks.length ? <div className="latency-empty-state"><p>没有匹配的探测任务</p><button type="button" className="ghost" onClick={() => { setTaskQuery(''); setMethodFilter('all') }}>清除筛选</button></div> : <div className="network-probe-table-scroll" tabIndex={0} role="region" aria-label="探测任务列表"><table className="network-probe-table">
          <thead><tr><th scope="col">名称 / 目标地址</th><th scope="col">方式</th><th scope="col">间隔</th><th scope="col">执行节点</th><th scope="col">任务状态</th><th scope="col" className="network-probe-actions-heading">操作</th></tr></thead>
          <tbody>{visibleTasks.map(task => {
            const assigned = task.server_ids.map(id => serverByID.get(id)).filter(Boolean) as Server[]
            const live = assigned.filter(server => online(server) && server.latency_probe_enabled).length
            return <tr key={task.id} className="probe-task-card">
              <td><strong>{task.name}</strong><span className="probe-task-target">{task.address ? `${task.address}${task.method === 'tcp' ? `:${task.port}` : ''}` : targetLabel(task.province, task.carrier)}</span></td>
              <td><span className="probe-task-badge">{task.method === 'http' ? 'HTTP' : task.method === 'icmp' ? 'Ping' : 'TCP'}</span></td>
              <td>{formatInterval(task.interval_seconds)}</td>
              <td title={assigned.map(server => server.name).join('、')}>{assigned.length} 台<small className="muted">{live} 台可执行</small></td>
              <td><span className={`probe-task-badge${task.enabled ? ' is-on' : ''}`}>{task.enabled ? assigned.length ? '已启用' : '待分配' : '已停用'}</span></td>
              <td><div className="probe-task-card-actions">
                <button type="button" className="ghost" aria-label={`编辑 ${task.name}`} disabled={!canManage || busy} onClick={() => setEditing({ open: true, task })}><Pencil size={14} aria-hidden="true" />编辑</button>
                <button type="button" className="ghost" disabled={!canManage || busy} onClick={() => toggleTask(task)}>{task.enabled ? '停用' : '启用'}</button>
                <button type="button" className="ghost danger" aria-label={`删除 ${task.name}`} disabled={!canManage || busy} onClick={() => deleteTask(task)}><Trash2 size={14} aria-hidden="true" />删除</button>
              </div></td>
            </tr>
          })}</tbody>
        </table></div>}
      </section>}

      {view === 'nodes' && <section id="probe-nodes-panel" role="tabpanel" aria-labelledby="probe-nodes-tab" className="return-latency-servers">
        <header className="return-latency-section-head">
          <h2>节点状态</h2>
          <span className="muted">{servers.length} 台</span>
        </header>
        <div className="return-latency-filters">
          <div className="return-latency-search">
            <Search size={15} aria-hidden="true" />
            <input type="search" aria-label="搜索服务器" placeholder="搜索名称、IP 或编号" value={query} onChange={event => setQuery(event.target.value)} />
          </div>
          <Select aria-label="服务器筛选" value={filter} onChange={event => setFilter(event.target.value)}>
            <option value="all">全部服务器</option>
            <option value="assigned">已分配任务</option>
            <option value="online">Agent 在线</option>
            <option value="enabled">已启用探测</option>
            <option value="disabled">未启用探测</option>
          </Select>
        </div>
        <div className="return-latency-server-list" aria-busy={loading}>
          {loading && !servers.length ? <p className="muted" role="status">正在加载服务器…</p> : !servers.length ? <p className="muted">暂无服务器，请先在服务器管理中添加。</p> : !visibleServers.length ? <div className="latency-empty-state">
            <p>没有匹配的服务器</p>
            <button type="button" className="ghost" onClick={() => { setQuery(''); setFilter('all') }}>清除筛选</button>
          </div> : <div className="network-probe-table-scroll" tabIndex={0} role="region" aria-label="节点状态列表"><table className="network-probe-table">
            <thead><tr><th scope="col">节点 / 地址</th><th scope="col">连接状态</th><th scope="col">公网探测</th><th scope="col">任务</th><th scope="col">自动探测</th><th scope="col" className="network-probe-actions-heading">操作</th></tr></thead>
            <tbody>{visibleServers.map(server => <tr className="return-latency-server" key={server.id}>
              <td><strong>{server.name}</strong><small className="muted">{server.public_ipv4 || server.public_ipv6 || `#${server.id}`}</small></td>
              <td><span className={`probe-task-badge${online(server) ? ' is-on' : ''}`}>{online(server) ? '在线' : server.agent_id ? '离线' : '未接入'}</span></td>
              <td>{!online(server) ? '—' : !server.latency_probe_enabled ? '未启用' : server.connectivity_status === 'available' ? `${server.connectivity_latency_ms ?? 0} ms` : server.connectivity_status === 'unavailable' ? '不可达' : '等待结果'}</td>
              <td>{taskCountByServer.get(server.id) || 0} 个</td>
              <td>{server.latency_probe_enabled ? '已启用' : '已关闭'}</td>
              <td><div className="return-latency-row-actions">
                <button type="button" className="ghost" disabled={!canManage || busy} onClick={() => setSettingsServer(server)}><Settings2 size={14} aria-hidden="true" />探测参数</button>
                <button type="button" className="ghost" disabled={!canManage || busy || !online(server) || !server.latency_probe_enabled} onClick={() => probeNow(server)}><Activity size={14} aria-hidden="true" />立即探测</button>
                <button type="button" className="ghost" onClick={() => setHistoryID(server.id)}><History size={14} aria-hidden="true" />历史</button>
              </div></td>
            </tr>)}</tbody>
          </table></div>}
        </div>
      </section>}
    </div>
    <Dialog isOpen={editing.open} onClose={() => setEditing({ open: false, task: null })} title={editing.task ? '编辑探测任务' : '创建探测任务'} size="lg" className="probe-task-dialog">
      <ReturnLatencyTaskForm
        key={editing.task?.id ?? 'new'}
        task={editing.task}
        regions={resource.regions}
        targets={resource.targets}
        servers={servers}
        loading={resource.loading}
        error={resource.error}
        disabled={!canManage}
        onSubmit={saveTask}
        onCancel={() => setEditing({ open: false, task: null })}
      />
    </Dialog>
    <Dialog isOpen={Boolean(settingsServer)} onClose={() => setSettingsServer(null)} title={settingsServer ? `${settingsServer.name} · 探测参数` : '探测参数'} size="lg">
      {settingsServer && <ReturnLatencySettings key={settingsServer.id} server={settingsServer} disabled={!canManage} onSave={patch => saveServerSettings(settingsServer, patch)} onCancel={() => setSettingsServer(null)} />}
    </Dialog>
    {historyServer && renderHistory(historyServer, () => setHistoryID(null))}
  </section>
}

