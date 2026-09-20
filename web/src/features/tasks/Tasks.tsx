import { useEffect, useState } from 'react'
import { ChevronRight } from 'lucide-react'
import { TableSkeleton } from '../../components/ui/skeleton'
import { MotionList, MotionCard } from '../../components/ui/motion'
import { Panel } from '../../shared/Panel'
import { cell, formatTableTime, labelValue } from '../../shared/presentation'
import { parseJSONLoose } from '../../shared/parse-json'
import type { APIClient } from '../../shared/api-client'
import { deploymentStatusFromSummary, groupTasksForTimeline, splitTaskAttempts, taskCategories, taskCategory, latestDeploymentTasks, maxTaskTime, serverTaskStatusSummary, taskStatusSummary, type TaskGroup } from '../../task-groups'
import { redactTaskJSON, taskSummaryFromPayload } from './domain'
import { useTasks } from './use-tasks'
import { fetchTask, fetchTaskOperation, fetchTaskOperations, type TaskOperationRecord } from './api'
import type { Task, TaskServer, TasksProps } from './types'
type TaskData = { servers?: TaskServer[] }

export function Tasks({ tasks, servers, client, loading: pageLoading }: TasksProps) {
  const data = { servers }
  const { rows, category, setCategory, manualRefreshing, backgroundRefreshing, lastRefreshedAt, refreshFailed, hasActiveTasks } = useTasks(tasks, client)
  const busy = manualRefreshing || pageLoading
  const refreshing = manualRefreshing || backgroundRefreshing
  const refreshedTime = lastRefreshedAt?.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
  return <Panel title="任务与部署">
    <div className="section-toolbar">
      <div>
        <h3>任务中心</h3>
        <p className="muted">先看各服务器最新版本的执行结果，再按任务类型查看详情。历史重试默认折叠。</p>
      </div>
      <div className="section-actions">
        <div className={`live-refresh-status ${refreshFailed ? 'is-error' : 'is-active'}`} title={hasActiveTasks ? '进行中的任务通过 HTTP 每 3 秒更新' : '任务状态通过 HTTP 每 15 秒更新'}>
          <span className="live-refresh-dot" aria-hidden="true" />
          <span>{refreshFailed ? '自动刷新暂时失败' : refreshing ? '正在更新任务' : 'HTTP 自动刷新已开启'}</span>
          {refreshedTime ? <time dateTime={lastRefreshedAt?.toISOString()}>更新于 {refreshedTime}</time> : null}
        </div>
      </div>
    </div>
    <DeploymentTaskOverview rows={rows} data={data} />
    <TaskOperationHistory client={client} refreshedAt={lastRefreshedAt} data={data} />
    <div className="task-category-filter" role="group" aria-label="任务分类">
      {taskCategories.map(item => <button key={item.id} type="button" className={category === item.id ? '' : 'ghost'} aria-pressed={category === item.id} onClick={() => setCategory(item.id)}>{item.label}<span>{rows.filter(task => taskCategory(task) === item.id).length}</span></button>)}
    </div>
    {busy && !rows.length ? <TableSkeleton /> : <TaskTimeline rows={rows.filter(task => taskCategory(task) === category)} data={data} client={client} />}

  </Panel>
}

function TaskOperationHistory({ client, refreshedAt, data }: { client?: APIClient; refreshedAt: Date | null; data: TaskData }) {
  const [records, setRecords] = useState<TaskOperationRecord[]>([])
  const [before, setBefore] = useState<TaskOperationRecord>()
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    let active = true
    if (!client) return
    fetchTaskOperations(client, before).then(items => { if (active) { setRecords(items); setFailed(false) } }, () => { if (active) setFailed(true) })
    return () => { active = false }
  }, [client, before, refreshedAt])
  if (!records.length && !failed && !before) return null
  return <section className="task-attempt-history" aria-label="批量操作记录">
    <h3>批量操作记录</h3>
    {failed && <p className="muted">操作记录刷新失败，已显示的结果可能不是最新状态</p>}
    {records.map(op => <TaskOperationDetails key={op.id} op={op} client={client} data={data} refreshedAt={refreshedAt} />)}
    <div className="section-actions">
      {before && <button type="button" className="ghost" onClick={() => setBefore(undefined)}>返回最新</button>}
      {records.length === 25 && <button type="button" className="ghost" onClick={() => setBefore(records[records.length - 1])}>更早的操作</button>}
    </div>
  </section>
}

function TaskOperationDetails({ op, client, data, refreshedAt }: { op: TaskOperationRecord; client?: APIClient; data: TaskData; refreshedAt: Date | null }) {
  const [open, setOpen] = useState(false)
  const [detail, setDetail] = useState<TaskOperationRecord>()
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    let active = true
    setDetail(undefined)
    setFailed(false)
    if (!open || !client) return
    fetchTaskOperation(client, op.id).then(value => { if (active) setDetail(value) }, () => { if (active) setFailed(true) })
    return () => { active = false }
  }, [open, client, op.id, refreshedAt])
  const current = detail || op
  return <details onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>{op.kind === 'servers.update' ? '服务器配置变更' : op.kind === 'servers.dns_test_batch' ? 'DNS 测试' : '操作记录'} · {formatTableTime(op.created_at)} · {current.targets.length} 台 · {current.targets.filter(t => ['failed', 'rollback_failed'].includes(t.state)).length} 失败</summary>
    {current.targets.map(target => <p key={`${target.target_type}:${target.target_id}`}>{taskServerLabel(data, Number(target.target_id))} · {target.state === 'no_change' ? '已满足，无需变更' : target.state === 'evidence_insufficient' ? '尚未确认生效' : target.state === 'superseded' ? '已被后续变更替代' : labelValue(target.state)}{op.kind === 'servers.update' ? (target.cause_code === 'local_saved' ? ' · 已保存；本地设置不以部署结果确认' : target.cause_code === 'partial_scope' ? ' · 仅追踪部分配置字段，不代表整次修改已生效' : '') : target.cause_code ? ' · 准备失败，未创建任务；请检查服务器 DNS 策略后重新测试' : ''}</p>)}
    {open && failed && <p className="muted">执行明细读取失败</p>}
    {open && client && !detail && !failed && <p className="muted">正在读取执行明细…</p>}
    {detail?.attempts?.map(attempt => <p className="muted" key={`${attempt.target_type}:${attempt.target_id}:${attempt.attempt}`}>
      {taskServerLabel(data, Number(attempt.target_id))} · 第 {attempt.attempt} 次 · {attempt.task_id == null ? '任务已清理' : `任务 #${attempt.task_id}`}
      {attempt.config_version != null ? ` · 交付版本 ${attempt.config_version}` : ''}{attempt.actual_revision != null ? ` · 配置修订 ${attempt.actual_revision}` : ''}
      {' · '}{attempt.execution_kind === 'later_modified' ? '使用后续修改后的配置' : attempt.execution_kind === 'original_retry' ? '原意图重试' : attempt.execution_kind === 'original_apply' ? '首次执行' : '执行来源未确认'} · {attempt.state === 'superseded' ? '已被替代' : labelValue(attempt.state)}
    </p>)}
  </details>
}

function DeploymentTaskOverview({ rows, data }: { rows: Task[]; data: TaskData }) {
  const tasks = latestDeploymentTasks(rows)
  const serverIDs = Array.from(new Set(tasks.map(task => Number(task.server_id || 0)))).filter(Boolean)
  if (!serverIDs.length) return null
  return <section className="task-deployment-overview" aria-label="最新部署状态">
    <div><h3>最新部署状态</h3><p className="muted">按当前已加载记录中，各服务器最新配置版本汇总；执行成功不代表实时在线。</p></div>
    <div className="task-deployment-servers">{serverIDs.map(id => {
      const current = tasks.filter(task => Number(task.server_id) === id)
      const status = deploymentStatusFromSummary(taskStatusSummary(current))
      return <div className="task-deployment-server" key={id}><strong>{taskServerLabel(data, id)}</strong><span className="muted">版本 {current[0]?.config_version || '未标记'}</span>{cell(status, 'status')}</div>
    })}</div>
  </section>
}

function TaskTimeline({ rows, data, client }: { rows: Task[]; data: TaskData; client?: APIClient }) {
  const [statusFilter, setStatusFilter] = useState('all')
  const groups = groupTasksForTimeline(rows, labelValue)
  if (!groups.length) return <p className="muted">暂无任务</p>
  const filtered = groups.filter(group => {
    if (statusFilter === 'all') return true
    const summary = group.operation || serverTaskStatusSummary(group.tasks)
    return statusFilter === 'active' ? summary.pending + summary.running > 0 : summary.failed > 0
  })
  const visible = filtered.filter((group, index) => index < 5 || group.tasks.some(task => ['pending', 'running'].includes(task.status)))
  const history = filtered.filter(group => !visible.includes(group))
  return <>
    <div className="task-category-filter" role="group" aria-label="任务状态筛选">
      {[['all', '全部状态'], ['active', '进行中'], ['failed', '含失败结果']].map(([value, label]) => <button key={value} type="button" className="ghost" aria-pressed={statusFilter === value} onClick={() => setStatusFilter(value)}>{label}</button>)}
    </div>
    {!filtered.length && <p className="muted">暂无符合条件的任务</p>}
    <MotionList className="task-card-list">{visible.map(group => (
      <TaskGroupCard key={`${group.kind}-${group.id}`} group={group} data={data} client={client} />
    ))}</MotionList>
    {history.length > 0 && <details className="task-attempt-history task-group-history">
      <summary>更早的任务 · {history.length} 组</summary>
      <div className="task-card-list">{history.map(group => <TaskGroupCard key={`${group.kind}-${group.id}`} group={group} data={data} client={client} />)}</div>
    </details>}
  </>
}

export function taskServerLabel(data: TaskData, serverID: number) {
  const server = (data?.servers || []).find((s: TaskServer) => Number(s.id) === Number(serverID))
  return server?.name || `服务器 #${serverID}`
}

function TaskGroupCard({ group, data, client }: { group: TaskGroup; data: TaskData; client?: APIClient }) {
  const [expanded, setExpanded] = useState(false)
  const [openServerID, setOpenServerID] = useState<number | null>(null)
  const summary = group.operation ? { ...group.operation, skipped: 0 } : serverTaskStatusSummary(group.tasks)
  const status = group.operation?.unknown && !summary.failed ? 'unknown' : deploymentStatusFromSummary(summary)
  const serverIDs = Array.from(new Set(group.tasks.map(t => Number(t.server_id || 0)))).filter(Boolean).sort((a, b) => a - b)
  const createdAt = String(group.tasks.map(t => t.created_at).filter(Boolean).sort()[0] || '')

  const byServer = new Map<number, Task[]>()
  group.tasks.forEach(task => {
    const sid = Number(task.server_id || 0)
    byServer.set(sid, [...(byServer.get(sid) || []), task])
  })

  const metaBits = [
    group.subtitle,
    group.operation ? `${group.operation.total} 台服务器` : serverIDs.length ? `${serverIDs.length} 台服务器` : '',
    `${splitTaskAttempts(group.tasks).current.length} 项当前任务`,
  ].filter(Boolean)

  // Single-server single-task groups can open details directly without an extra empty layer.
  const isFlatSingle = group.kind === 'single' && group.tasks.length === 1

  return <MotionCard tag="article" className="task-card task-group-card">
    <button type="button" className="task-group-toggle" onClick={() => setExpanded(v => !v)} aria-expanded={expanded}>
      <div className="task-group-title-block">
        <strong>{group.title}</strong>
        <span>{metaBits.join(' · ')}</span>
      </div>
      <div className="task-summary">
        {summary.succeeded > 0 && <span className="task-stat"><em>{summary.succeeded}</em> 成功</span>}
        {summary.pending > 0 && <span className="task-stat"><em>{summary.pending}</em> 等待</span>}
        {summary.running > 0 && <span className="task-stat"><em>{summary.running}</em> 执行中</span>}
        {summary.failed > 0 && <span className="task-stat is-fail"><em>{summary.failed}</em> 失败</span>}
        {group.operation?.unknown ? <span className="task-stat"><em>{group.operation.unknown}</em> 未确认</span> : null}
        {summary.skipped ? <span className="task-stat"><em>{summary.skipped}</em> 跳过</span> : null}
      </div>
      <div className="task-group-head-right">
        {cell(status, 'status')}
        <ChevronRight size={16} className={expanded ? 'task-chevron open' : 'task-chevron'} />
      </div>
      <div className="task-meta">
        <span>创建 {formatTableTime(createdAt)}</span>
        <span>更新 {formatTableTime(maxTaskTime(group.tasks))}</span>
      </div>
    </button>

    {expanded && (
      <div className="task-group-body">
        {isFlatSingle ? (
          <TaskDetailList tasks={group.tasks} data={data} client={client} />
        ) : (
          <div className="task-server-list">
            {serverIDs.map(serverID => {
              const tasks = byServer.get(serverID) || []
              const serverSummary = taskStatusSummary(tasks)
              const serverStatus = deploymentStatusFromSummary(serverSummary)
              const open = openServerID === serverID
              return <div key={serverID} className={`task-server-row ${open ? 'open' : ''}`}>
                <button type="button" className="task-server-toggle" onClick={() => setOpenServerID(open ? null : serverID)} aria-expanded={open}>
                  <div className="task-group-title-block">
                    <strong>{taskServerLabel(data, serverID)}</strong>
                    <span>{tasks.length > 1 ? `当前 ${serverSummary.total} 项 · 历史 ${splitTaskAttempts(tasks).history.length} 次 · ` : ''}成功 {serverSummary.succeeded} · 失败 {serverSummary.failed} · 进行中 {serverSummary.pending + serverSummary.running}</span>
                  </div>
                  <div className="task-group-head-right">
                    {cell(serverStatus, 'status')}
                    <ChevronRight size={15} className={open ? 'task-chevron open' : 'task-chevron'} />
                  </div>
                </button>
                {open && <TaskDetailList tasks={tasks} data={data} client={client} />}
              </div>
            })}
            {(byServer.get(0) || []).length > 0 && (
              <div className="task-server-row open">
                <div className="task-server-toggle static">
                  <div><strong>未绑定服务器</strong><span>{(byServer.get(0) || []).length} 项</span></div>
                </div>
                <TaskDetailList tasks={byServer.get(0) || []} data={data} client={client} />
              </div>
            )}
          </div>
        )}
      </div>
    )}
  </MotionCard>
}

function TaskDetailList({ tasks, data, client }: { tasks: Task[]; data: TaskData; client?: APIClient }) {
  const { current, history } = splitTaskAttempts(tasks)
  const failures = history.filter(task => ['failed', 'timeout'].includes(task.status)).length
  return <div className="task-detail-list">
    {current.map(task => <TaskDetailCard key={task.id} task={task} data={data} client={client} />)}
    {history.length > 0 && <details className="task-attempt-history">
      <summary>历史执行 · {history.length} 次{failures ? ` · 曾失败 ${failures} 次` : ''}</summary>
      <div className="task-detail-list">{history.map(task => <TaskDetailCard key={task.id} task={task} data={data} client={client} />)}</div>
    </details>}
  </div>
}

function taskHasDetailBody(task: Task) {
  return Boolean(String(task?.payload_json || '').trim() || String(task?.result_json || '').trim())
}

function TaskDetailCard({ task, data, client }: { task: Task; data?: TaskData; client?: APIClient }) {
  const [open, setOpen] = useState(false)
  const [detail, setDetail] = useState(task)
  const [loadingDetail, setLoadingDetail] = useState(false)
  useEffect(() => {
    setDetail((current: Task) => {
      if (Number(current?.id) === Number(task?.id) && taskHasDetailBody(current) && !taskHasDetailBody(task)) {
        return { ...task, payload_json: current.payload_json, result_json: current.result_json, nonce: current.nonce }
      }
      return task
    })
  }, [task])
  const result = parseJSONLoose(detail.result_json)
  const payload = parseJSONLoose(detail.payload_json)
  const error = String(result?.error || '')
  const message = String(result?.message || '')
  const status = result?.timeout ? 'timeout' : detail.status
  const summary = error || message || taskSummaryFromPayload(detail.type, payload)
  const loadDetail = async () => {
    const next = !open
    setOpen(next)
    if (!next || !client || !task?.id || taskHasDetailBody(detail)) return
    setLoadingDetail(true)
    try {
      const detail = await fetchTask(client, task.id)
      if (detail) setDetail({ ...task, ...detail })
    } catch (error) {
      console.warn('Task detail load failed:', error)
    } finally {
      setLoadingDetail(false)
    }
  }
  return <article className="task-detail-card">
    <button type="button" className="task-detail-toggle" onClick={() => { void loadDetail() }} aria-expanded={open}>
      <div>
        <strong>{task.type === 'remote_exec' ? '远程命令' : task.type === 'remote_operation' ? '远程操作' : labelValue(task.type || 'task')}</strong>
        <span className={error ? 'error-text' : ''}>{summary}</span>
      </div>
      <div className="task-group-head-right">
        {cell(status, 'status')}
        <ChevronRight size={14} className={open ? 'task-chevron open' : 'task-chevron'} />
      </div>
    </button>
    {open && (
      <div className="task-detail-body">
        <div className="task-meta">
          <span>任务 #{task.id}</span>
          <span>创建 {formatTableTime(String(task.created_at || ''))}</span>
          <span>更新 {formatTableTime(String(task.updated_at || ''))}</span>
          {task.completed_at && <span>完成 {formatTableTime(String(task.completed_at))}</span>}
          {task.config_version ? <span>版本 {task.config_version}</span> : null}
        </div>
        {loadingDetail ? <p className="muted">正在加载任务详情…</p> : null}
        {task.type === 'apply_deployment' && Array.isArray(result?.steps) ? (
          <div className="deployment-step-list">
            {result.steps.map((step: any, index: number) => (
              <div className="deployment-step-row" key={`${step?.key || 'step'}-${index}`}>
                <div>
                  <strong>{String(step?.label || step?.key || `步骤 ${index + 1}`)}</strong>
                  <span className={step?.error ? 'error-text' : ''}>{String(step?.error || step?.message || '')}</span>
                </div>
                <div className="task-group-head-right">
                  {cell(step?.status || 'succeeded', 'status')}
                  <span className="muted">{Number(step?.duration_ms || 0)} ms</span>
                </div>
              </div>
            ))}
          </div>
        ) : null}
        <details className="task-attempt-history"><summary>查看原始数据</summary><pre>{JSON.stringify({ payload: redactTaskJSON(payload), result: redactTaskJSON(result) }, null, 2)}</pre></details>
      </div>
    )}
  </article>
}
