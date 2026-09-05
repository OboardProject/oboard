import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Activity, History, Pencil, Plus, RefreshCw, Search, Settings2, Trash2 } from 'lucide-react'
import { Dialog } from '../ui/dialog'
import { Select } from '../ui/select'
import type { LatencyProbeRegion, LatencyProbeTask, Server } from '../proxy-path/types'
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
  const [resource, setResource] = useState<{ regions: LatencyProbeRegion[]; loading: boolean; error: string }>({ regions: [], loading: true, error: '' })
  const [resourceRevision, setResourceRevision] = useState(0)
  const [taskRevision, setTaskRevision] = useState(0)
  const [editing, setEditing] = useState<{ open: boolean; task: LatencyProbeTask | null }>({ open: false, task: null })
  const [settingsServer, setSettingsServer] = useState<Server | null>(null)
  const [historyID, setHistoryID] = useState<number | null>(null)
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
      if (!controller.signal.aborted) setResource({ regions: result.regions || [], loading: false, error: '' })
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

  return <section className="panel return-latency-page" aria-label="回程延迟管理">
    <div className="panel-body">
      <div className="return-latency-toolbar">
        <button type="button" className="primary" disabled={!canManage || resource.loading} onClick={() => setEditing({ open: true, task: null })}><Plus size={15} aria-hidden="true" />创建探测任务</button>
        <div className="return-latency-toolbar-secondary">
          <button type="button" className="ghost" disabled={busy || !canManage || !tasks.length} onClick={runAll}><Activity size={15} aria-hidden="true" />立即探测</button>
          <button type="button" className="ghost" disabled={busy || loading} onClick={() => { void onRefresh(); reloadTasks() }}><RefreshCw size={15} aria-hidden="true" />刷新</button>
        </div>
      </div>
      {notice && <p className={notice.kind === 'error' ? 'danger-text' : 'muted'} role="status">{notice.text}</p>}
      {resource.error && <p className="danger-text" role="alert">{resource.error} <button type="button" className="ghost" disabled={resource.loading} onClick={() => setResourceRevision(current => current + 1)}>重新加载</button></p>}

      <section className="return-latency-tasks" aria-label="探测任务">
        <header className="return-latency-section-head">
          <h2>探测任务</h2>
          <span className="muted">{tasks.length} 个任务 · 每个任务一个目标</span>
        </header>
        {tasksState.error && <p className="danger-text" role="alert">{tasksState.error}</p>}
        {tasksState.loading && !tasks.length ? <p className="muted" role="status">正在加载探测任务…</p> : !tasks.length ? <div className="latency-empty-state">
          <p>还没有探测任务</p>
          <p className="muted">创建一个任务，指定一个目标（例如 广东 · 中国电信），选择执行的服务器和探测间隔。</p>
          <button type="button" className="primary" disabled={!canManage} onClick={() => setEditing({ open: true, task: null })}><Plus size={15} aria-hidden="true" />创建探测任务</button>
        </div> : <ul className="probe-task-cards">
          {tasks.map(task => {
            const assigned = task.server_ids.map(id => serverByID.get(id)).filter(Boolean) as Server[]
            const live = assigned.filter(server => online(server) && server.latency_probe_enabled).length
            return <li key={task.id} className={`probe-task-card${task.enabled ? '' : ' is-disabled'}`}>
              <div className="probe-task-card-head">
                <div className="probe-task-card-title">
                  <strong>{task.name}</strong>
                  <span className="probe-task-target">{targetLabel(task.province, task.carrier)}</span>
                </div>
                <span className={`probe-task-badge${task.enabled ? ' is-on' : ''}`}>{task.enabled ? '运行中' : '已停用'}</span>
              </div>
              <dl className="probe-task-meta">
                <div><dt>探测间隔</dt><dd>{formatInterval(task.interval_seconds)}</dd></div>
                <div><dt>执行服务器</dt><dd>{assigned.length ? `${assigned.length} 台` : '未分配'}</dd></div>
                <div><dt>当前可执行</dt><dd>{live} 台</dd></div>
              </dl>
              {assigned.length > 0 && <p className="probe-task-servers-preview muted">{assigned.slice(0, 4).map(server => server.name).join('、')}{assigned.length > 4 ? ` 等 ${assigned.length} 台` : ''}</p>}
              {!assigned.length && <p className="muted">尚未选择执行服务器，该任务不会下发。</p>}
              <div className="probe-task-card-actions">
                <button type="button" className="ghost" disabled={!canManage || busy} onClick={() => setEditing({ open: true, task })}><Pencil size={14} aria-hidden="true" />编辑</button>
                <button type="button" className="ghost" disabled={!canManage || busy} onClick={() => toggleTask(task)}>{task.enabled ? '停用' : '启用'}</button>
                <button type="button" className="ghost danger" disabled={!canManage || busy} onClick={() => deleteTask(task)}><Trash2 size={14} aria-hidden="true" />删除</button>
              </div>
            </li>
          })}
        </ul>}
      </section>

      <section className="return-latency-servers" aria-label="执行服务器">
        <header className="return-latency-section-head">
          <h2>执行服务器</h2>
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
          </div> : visibleServers.map(server => <article className="return-latency-server" key={server.id}>
            <div className="return-latency-server-head">
              <span className="return-latency-server-name">
                <strong>{server.name}</strong>
                <small className="muted">{server.public_ipv4 || server.public_ipv6 || `#${server.id}`}</small>
              </span>
              <span className="return-latency-state">{online(server) ? '在线' : server.agent_id ? '离线' : '未接入'}</span>
            </div>
            <p className="muted">{server.latency_probe_enabled ? `${server.latency_probe_mode === 'icmp' ? 'ICMP' : 'TCP'} · 公网基准 ${server.latency_probe_interval_seconds || 60} 秒 · ${taskCountByServer.get(server.id) || 0} 个任务` : '自动探测已关闭'}</p>
            <div className="return-latency-row-actions">
              <button type="button" className="ghost" disabled={!canManage || busy} onClick={() => setSettingsServer(server)}><Settings2 size={14} aria-hidden="true" />探测参数</button>
              <button type="button" className="ghost" disabled={!canManage || busy || !online(server) || !server.latency_probe_enabled} onClick={() => probeNow(server)}><Activity size={14} aria-hidden="true" />立即探测</button>
              <button type="button" className="ghost" onClick={() => setHistoryID(server.id)}><History size={14} aria-hidden="true" />历史</button>
            </div>
          </article>)}
        </div>
      </section>
    </div>
    <Dialog isOpen={editing.open} onClose={() => setEditing({ open: false, task: null })} title={editing.task ? '编辑探测任务' : '创建探测任务'} size="lg" className="probe-task-dialog">
      <ReturnLatencyTaskForm
        key={editing.task?.id ?? 'new'}
        task={editing.task}
        regions={resource.regions}
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

