import { useEffect, useState } from 'react'
import { Dialog } from '../ui/dialog'
import { Select } from '../ui/select'
import type { LatencyProbeTask, Server } from '../proxy-path/types'

type Client = { request: (path: string, init?: RequestInit) => Promise<any> }

export function ServerMonitoringTargetDialog({ server, client, onClose, onSaved }: {
  server: Server
  client: Client
  onClose: () => void
  onSaved: (server: Server) => void
}) {
  const [target, setTarget] = useState(server.monitoring_target_task_id || 0)
  const [tasks, setTasks] = useState<LatencyProbeTask[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    void client.request(`/servers/${server.id}/latency-probe?limit=1`, { signal: controller.signal }).then(result => {
      if (!controller.signal.aborted) setTasks((result.tasks || []).filter((task: LatencyProbeTask) => task.enabled && task.server_ids.includes(server.id)))
    }).catch(error => {
      if (!controller.signal.aborted) setError(error?.message || '探测任务加载失败')
    }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [client, server.id, revision])
  const missing = target !== 0 && !tasks.some(task => task.id === target)
  const save = async () => {
    if (saving || loading || missing) return
    setSaving(true)
    setError('')
    try {
      const result = await client.request(`/servers/${server.id}`, { method: 'PATCH', body: JSON.stringify({ monitoring_target_task_id: target }) })
      onSaved(result.server)
      onClose()
    } catch (error: any) {
      setError(error?.message || '监控目标保存失败')
    } finally { setSaving(false) }
  }
  return <Dialog isOpen onClose={() => { if (!saving) onClose() }} title="选择监控目标" size="sm" footer={<>
    <button type="button" className="ghost" disabled={saving} onClick={onClose}>取消</button>
    <button type="button" disabled={loading || saving || missing} aria-busy={saving} onClick={() => void save()}>{saving ? '保存中…' : '保存为默认'}</button>
  </>}>
    <p className="muted">{server.name} 的延迟、丢包率和历史色带将使用同一目标。选择保存在服务端，下次打开仍然生效。</p>
    <label className="field">监控目标
      <Select aria-label="监控目标" aria-describedby="monitoring-target-help" value={String(target)} disabled={loading || saving} onChange={event => setTarget(Number(event.target.value))}>
        <option value="0">公网探测</option>
        {tasks.map(task => <option key={task.id} value={task.id}>{task.name} · {task.method.toUpperCase()}</option>)}
        {missing && <option value={target} disabled>原探测任务不可用</option>}
      </Select>
    </label>
    <p id="monitoring-target-help" className="muted">{loading ? '正在加载探测任务…' : missing ? '原任务已停用、删除或取消分配，请重新选择。' : '丢包率按所选目标最近 20 轮探测的实际样本计算。'}</p>
    {error && <div role="alert"><p className="danger-text">{error}</p><button type="button" className="ghost" disabled={saving} onClick={() => setRevision(value => value + 1)}>重新加载</button></div>}
  </Dialog>
}
