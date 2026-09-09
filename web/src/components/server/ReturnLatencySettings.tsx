import { useMemo, useState } from 'react'
import { Loader2 } from 'lucide-react'
import { FormField } from '../ui/form-field'
import { Select } from '../ui/select'
import { Switch } from '../ui/switch'
import type { Server, LatencyProbeMode, ConnectivityProbeTarget, LatencyProbeTask } from '../proxy-path/types'

const clamp = (value: number | '', min: number, max: number, fallback: number) => {
  if (value === '' || Number.isNaN(Number(value))) return fallback
  return Math.min(max, Math.max(min, Math.trunc(Number(value))))
}

const formatTaskInterval = (seconds: number) => {
  if (!seconds) return '未设置'
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`
  return `${seconds} 秒`
}

export type LatencyProbeTaskAssignmentChange = { addTaskIDs: number[]; removeTaskIDs: number[] }

// ReturnLatencySettings edits the probe parameters of a single server. Probe
// targets are owned by latency probe tasks, not by the server record; this
// dialog can flip the server's own membership in each task in the same save.
export function ReturnLatencySettings({ server, tasks = [], disabled, onSave, onCancel }: {
  server: Server
  tasks?: LatencyProbeTask[]
  disabled?: boolean
  onSave: (patch: Partial<Server>, taskChanges?: LatencyProbeTaskAssignmentChange) => void | Promise<void>
  onCancel?: () => void
}) {
  const [values, setValues] = useState({
    latency_probe_enabled: server.latency_probe_enabled !== false,
    latency_probe_mode: (server.latency_probe_mode || 'tcp') as LatencyProbeMode,
    latency_probe_public_target: (server.latency_probe_public_target || 'auto') as ConnectivityProbeTarget,
    latency_probe_interval_seconds: (server.latency_probe_interval_seconds || 120) as number | '',
    latency_probe_sample_count: (server.latency_probe_sample_count || 3) as number | '',
    latency_probe_max_targets: (server.latency_probe_max_targets || 64) as number | '',
  })
  const initialTaskIDs = useMemo(() => tasks.filter(task => task.server_ids.includes(server.id)).map(task => task.id), [tasks, server.id])
  const [selectedTaskIDs, setSelectedTaskIDs] = useState<number[]>(initialTaskIDs)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  const updateParam = (patch: Partial<typeof values>) => setValues(old => ({ ...old, ...patch }))
  const toggleTaskSelected = (taskID: number) => setSelectedTaskIDs(current => current.includes(taskID) ? current.filter(id => id !== taskID) : [...current, taskID])

  const invalidParams = !([
    [values.latency_probe_interval_seconds, 30, 86400],
    [values.latency_probe_sample_count, 1, 10],
    [values.latency_probe_max_targets, 1, 256],
  ] as [number | '', number, number][]).every(([value, min, max]) => Number.isInteger(value) && Number(value) >= min && Number(value) <= max)

  const save = async () => {
    if (saving || disabled || invalidParams) return
    setSaving(true)
    setSaveError('')
    try {
      const addTaskIDs = selectedTaskIDs.filter(id => !initialTaskIDs.includes(id))
      const removeTaskIDs = initialTaskIDs.filter(id => !selectedTaskIDs.includes(id))
      await onSave({
        latency_probe_enabled: values.latency_probe_enabled,
        latency_probe_mode: values.latency_probe_mode,
        latency_probe_public_target: values.latency_probe_public_target,
        latency_probe_interval_seconds: Number(values.latency_probe_interval_seconds),
        latency_probe_sample_count: Number(values.latency_probe_sample_count),
        latency_probe_max_targets: Number(values.latency_probe_max_targets),
      }, (addTaskIDs.length || removeTaskIDs.length) ? { addTaskIDs, removeTaskIDs } : undefined)
    } catch (error: any) {
      setSaveError(error?.message || '保存失败，请重试')
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="return-latency-editor" aria-label="服务器探测参数">
      <fieldset disabled={saving || disabled} className="return-latency-fields">
        <label className="return-latency-switch-row">
          <span>
            <strong>启用自动探测</strong>
            <small className="muted">关闭后该服务器不再执行任何探测任务，也停止公网连通性判断。</small>
          </span>
          <Switch checked={values.latency_probe_enabled} ariaLabel="启用自动探测" onChange={checked => updateParam({ latency_probe_enabled: checked })} />
        </label>
        <div className="latency-params-grid">
          <FormField label="公网探测方式" hint="仅用于公网连通性目标，各任务的探测方式独立设置。">
            <Select aria-label="延迟探测方式" variant="segmented" value={values.latency_probe_mode} onChange={event => updateParam({ latency_probe_mode: event.target.value as LatencyProbeMode })}>
              <option value="tcp">TCP Ping</option>
              <option value="icmp">ICMP Ping</option>
            </Select>
          </FormField>
          <FormField label="公网目标" hint="断线期间判断公网连通性，每轮探测都会附带。">
            <Select aria-label="延迟探测公网目标" value={values.latency_probe_public_target} onChange={event => updateParam({ latency_probe_public_target: event.target.value as ConnectivityProbeTarget })}>
              <option value="auto">自动（按服务器地区选择）</option>
              <option value="cloudflare">cp.cloudflare.com</option>
              <option value="12306">www.12306.cn</option>
              <option value="google">www.gstatic.com</option>
            </Select>
          </FormField>
          <FormField label="公网基准间隔（秒）" hint="公网目标的探测周期（30–86400 秒）。各任务的间隔单独设置。">
            <input aria-label="公网基准探测间隔（秒）" type="number" min={30} max={86400} placeholder="120" value={values.latency_probe_interval_seconds} onChange={event => updateParam({ latency_probe_interval_seconds: event.target.value === '' ? '' : Number(event.target.value) })} onBlur={event => updateParam({ latency_probe_interval_seconds: clamp(event.target.value === '' ? '' : Number(event.target.value), 30, 86400, 120) })} />
          </FormField>
          <FormField label="每个目标样本数" hint="连续探测样本数（1–10）。">
            <input aria-label="每个延迟目标样本数" type="number" min={1} max={10} placeholder="3" value={values.latency_probe_sample_count} onChange={event => updateParam({ latency_probe_sample_count: event.target.value === '' ? '' : Number(event.target.value) })} onBlur={event => updateParam({ latency_probe_sample_count: clamp(event.target.value === '' ? '' : Number(event.target.value), 1, 10, 3) })} />
          </FormField>
          <FormField label="单次最多目标数" hint="含 1 个公网目标（1–256）。超出上限的目标不会下发，请按任务数量调整。">
            <input aria-label="单次最多目标数" type="number" min={1} max={256} placeholder="64" value={values.latency_probe_max_targets} onChange={event => updateParam({ latency_probe_max_targets: event.target.value === '' ? '' : Number(event.target.value) })} onBlur={event => updateParam({ latency_probe_max_targets: clamp(event.target.value === '' ? '' : Number(event.target.value), 1, 256, 64) })} />
          </FormField>
        </div>
        {tasks.length > 0 && <section className="probe-task-servers" aria-label="执行的探测任务">
          <header className="return-latency-section-head">
            <h3>执行的探测任务</h3>
            <span className="muted">已选 {selectedTaskIDs.length} / {tasks.length}</span>
          </header>
          <p className="muted">勾选该服务器要执行的探测任务；未勾选的任务不会分配给它。</p>
          <div className="probe-task-server-list">
            {tasks.map(task => (
              <label key={task.id} className={`probe-task-server${selectedTaskIDs.includes(task.id) ? ' is-selected' : ''}`}>
                <input type="checkbox" aria-label={`执行任务 ${task.name}`} checked={selectedTaskIDs.includes(task.id)} onChange={() => toggleTaskSelected(task.id)} />
                <span className="probe-task-server-main">
                  <strong>{task.name}</strong>
                  <small className="muted">{task.method === 'http' ? 'HTTP' : task.method === 'icmp' ? 'Ping' : 'TCP'} · 每 {formatTaskInterval(task.interval_seconds)}</small>
                </span>
                <span className="probe-task-server-state muted">{task.enabled ? '已启用' : '已停用'}</span>
              </label>
            ))}
          </div>
        </section>}
        {saveError && <p className="danger-text" role="alert">{saveError}</p>}
        <div className="return-latency-form-actions">
          {onCancel && <button type="button" className="ghost" onClick={onCancel}>取消</button>}
          <button type="button" className="primary" disabled={saving || disabled || invalidParams} onClick={() => void save()}>
            {saving && <Loader2 size={15} className="spin" aria-hidden="true" />}保存参数
          </button>
        </div>
      </fieldset>
    </section>
  )
}

