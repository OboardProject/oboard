import { useState } from 'react'
import { Loader2 } from 'lucide-react'
import { FormField } from '../ui/form-field'
import { Select } from '../ui/select'
import { Switch } from '../ui/switch'
import type { Server, LatencyProbeMode, ConnectivityProbeTarget } from '../proxy-path/types'

const clamp = (value: number | '', min: number, max: number, fallback: number) => {
  if (value === '' || Number.isNaN(Number(value))) return fallback
  return Math.min(max, Math.max(min, Math.trunc(Number(value))))
}

// ReturnLatencySettings edits the probe parameters of a single server. Probe
// targets are owned by latency probe tasks, not by the server record.
export function ReturnLatencySettings({ server, disabled, onSave, onCancel }: {
  server: Server
  disabled?: boolean
  onSave: (patch: Partial<Server>) => void | Promise<void>
  onCancel?: () => void
}) {
  const [values, setValues] = useState({
    latency_probe_enabled: server.latency_probe_enabled !== false,
    latency_probe_mode: (server.latency_probe_mode || 'tcp') as LatencyProbeMode,
    latency_probe_public_target: (server.latency_probe_public_target || 'auto') as ConnectivityProbeTarget,
    latency_probe_interval_seconds: (server.latency_probe_interval_seconds || 60) as number | '',
    latency_probe_sample_count: (server.latency_probe_sample_count || 3) as number | '',
    latency_probe_max_targets: (server.latency_probe_max_targets || 64) as number | '',
  })
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  const updateParam = (patch: Partial<typeof values>) => setValues(old => ({ ...old, ...patch }))

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
      await onSave({
        latency_probe_enabled: values.latency_probe_enabled,
        latency_probe_mode: values.latency_probe_mode,
        latency_probe_public_target: values.latency_probe_public_target,
        latency_probe_interval_seconds: Number(values.latency_probe_interval_seconds),
        latency_probe_sample_count: Number(values.latency_probe_sample_count),
        latency_probe_max_targets: Number(values.latency_probe_max_targets),
      })
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
          <FormField label="探测方式" hint="TCP 测试端口连接；ICMP 测试 Echo。">
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
            <input aria-label="公网基准探测间隔（秒）" type="number" min={30} max={86400} placeholder="60" value={values.latency_probe_interval_seconds} onChange={event => updateParam({ latency_probe_interval_seconds: event.target.value === '' ? '' : Number(event.target.value) })} onBlur={event => updateParam({ latency_probe_interval_seconds: clamp(event.target.value === '' ? '' : Number(event.target.value), 30, 86400, 60) })} />
          </FormField>
          <FormField label="每个目标样本数" hint="连续探测样本数（1–10）。">
            <input aria-label="每个延迟目标样本数" type="number" min={1} max={10} placeholder="3" value={values.latency_probe_sample_count} onChange={event => updateParam({ latency_probe_sample_count: event.target.value === '' ? '' : Number(event.target.value) })} onBlur={event => updateParam({ latency_probe_sample_count: clamp(event.target.value === '' ? '' : Number(event.target.value), 1, 10, 3) })} />
          </FormField>
          <FormField label="单次最多目标数" hint="含 1 个公网目标（1–256）。超出的任务目标会顺延到下一轮。">
            <input aria-label="单次最多目标数" type="number" min={1} max={256} placeholder="64" value={values.latency_probe_max_targets} onChange={event => updateParam({ latency_probe_max_targets: event.target.value === '' ? '' : Number(event.target.value) })} onBlur={event => updateParam({ latency_probe_max_targets: clamp(event.target.value === '' ? '' : Number(event.target.value), 1, 256, 64) })} />
          </FormField>
        </div>
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

