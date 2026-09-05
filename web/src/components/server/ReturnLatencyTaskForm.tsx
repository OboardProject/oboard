import { useMemo, useState } from 'react'
import { Loader2, Search } from 'lucide-react'
import { FormField } from '../ui/form-field'
import { Select } from '../ui/select'
import { Switch } from '../ui/switch'
import type { LatencyProbeRegion, LatencyProbeTask, Server } from '../proxy-path/types'

const INTERVAL_PRESETS = [60, 300, 900, 1800, 3600]
const CARRIER_PRIORITY = ['中国电信', '中国联通', '中国移动', '中国广电', '教育网']

export const targetLabel = (province: string, carrier: string) => {
  const left = province.trim()
  const right = carrier.trim()
  if (!left) return right
  if (!right) return left
  return `${left} · ${right}`
}

export function ReturnLatencyTaskForm({ task, regions, servers, loading, error, disabled, onSubmit, onCancel }: {
  task?: LatencyProbeTask | null
  regions: LatencyProbeRegion[]
  servers: Server[]
  loading?: boolean
  error?: string
  disabled?: boolean
  onSubmit: (payload: Omit<LatencyProbeTask, 'id' | 'created_at' | 'updated_at'>) => void | Promise<void>
  onCancel: () => void
}) {
  const [province, setProvince] = useState(task?.province || '')
  const [carrier, setCarrier] = useState(task?.carrier || '')
  const [name, setName] = useState(task?.name || '')
  const [nameTouched, setNameTouched] = useState(Boolean(task?.name))
  const [interval, setInterval] = useState<number | ''>(task?.interval_seconds || 300)
  const [enabled, setEnabled] = useState(task?.enabled !== false)
  const [serverIDs, setServerIDs] = useState<number[]>(task?.server_ids || [])
  const [serverQuery, setServerQuery] = useState('')
  const [saving, setSaving] = useState(false)
  const [submitError, setSubmitError] = useState('')

  const provinces = useMemo(() => Array.from(new Set(regions.map(region => region.province))).sort((a, b) => a.localeCompare(b, 'zh-CN')), [regions])
  const carriers = useMemo(() => {
    const pool = province ? regions.filter(region => region.province === province) : regions
    return Array.from(new Set(pool.map(region => region.carrier))).sort((a, b) => {
      const ia = CARRIER_PRIORITY.indexOf(a)
      const ib = CARRIER_PRIORITY.indexOf(b)
      if (ia !== -1 && ib !== -1) return ia - ib
      if (ia !== -1) return -1
      if (ib !== -1) return 1
      return a.localeCompare(b, 'zh-CN')
    })
  }, [regions, province])

  const effectiveName = nameTouched && name.trim() ? name.trim() : targetLabel(province, carrier)
  const online = (server: Server) => Boolean(server.agent_id) && server.status === 'online'
  const suggested = useMemo(() => servers.filter(server => online(server) && server.latency_probe_enabled), [servers])
  const visibleServers = useMemo(() => {
    const q = serverQuery.trim().toLowerCase()
    if (!q) return servers
    return servers.filter(server => `${server.name} ${server.id} ${server.public_ipv4 || ''} ${server.public_ipv6 || ''}`.toLowerCase().includes(q))
  }, [servers, serverQuery])

  const toggleServer = (id: number) => setServerIDs(current => current.includes(id) ? current.filter(item => item !== id) : [...current, id])
  const intervalInvalid = interval === '' || !Number.isInteger(Number(interval)) || Number(interval) < 30 || Number(interval) > 86400
  const canSubmit = Boolean(province && carrier) && !intervalInvalid && !saving && !disabled

  const submit = async () => {
    if (!canSubmit) return
    setSaving(true)
    setSubmitError('')
    try {
      await onSubmit({ name: effectiveName, province, carrier, interval_seconds: Number(interval), enabled, server_ids: serverIDs })
    } catch (err: any) {
      setSubmitError(err?.message || '保存失败，请重试')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="probe-task-form">
      {error && <p className="danger-text" role="alert">{error}</p>}
      <fieldset disabled={saving || disabled} className="return-latency-fields">
        <div className="probe-task-form-grid">
          <FormField label="探测目标：省份" required hint="一个任务只探测一个目标。">
            <Select aria-label="探测目标省份" value={province} placeholder={loading ? '正在加载…' : '选择省份'} onChange={event => { setProvince(event.target.value); setCarrier('') }}>
              {provinces.map(item => <option key={item} value={item}>{item}</option>)}
            </Select>
          </FormField>
          <FormField label="探测目标：运营商" required>
            <Select aria-label="探测目标运营商" value={carrier} placeholder={province ? '选择运营商' : '请先选择省份'} onChange={event => setCarrier(event.target.value)}>
              {carriers.map(item => <option key={item} value={item}>{item}</option>)}
            </Select>
          </FormField>
          <FormField label="任务名称" hint="留空自动使用「省份 · 运营商」。同名任务不可重复。">
            <input aria-label="任务名称" type="text" maxLength={60} placeholder={targetLabel(province, carrier) || '例如：广州电信'} value={name} onChange={event => { setName(event.target.value); setNameTouched(true) }} />
          </FormField>
          <FormField label="探测间隔（秒）" required hint="该任务多久执行一次（30–86400 秒）。">
            <div className="probe-task-interval">
              <input aria-label="探测间隔（秒）" type="number" min={30} max={86400} value={interval} onChange={event => setInterval(event.target.value === '' ? '' : Number(event.target.value))} />
              <div className="probe-task-interval-presets">
                {INTERVAL_PRESETS.map(preset => <button key={preset} type="button" className={`latency-pill-btn${Number(interval) === preset ? ' active' : ''}`} onClick={() => setInterval(preset)}>{preset >= 3600 ? `${preset / 3600} 小时` : preset >= 60 ? `${preset / 60} 分钟` : `${preset} 秒`}</button>)}
              </div>
            </div>
          </FormField>
        </div>
        <label className="return-latency-switch-row">
          <span><strong>启用该任务</strong><small className="muted">停用后保留配置，但不再下发给服务器。</small></span>
          <Switch checked={enabled} ariaLabel="启用该任务" onChange={setEnabled} />
        </label>
        <section className="probe-task-servers" aria-label="执行服务器">
          <header className="return-latency-section-head">
            <h3>执行服务器</h3>
            <span className="muted">已选 {serverIDs.length} 台</span>
          </header>
          <p className="muted">建议选择 Agent 在线且已启用自动探测的服务器；未选择服务器时任务不会执行。</p>
          <div className="probe-task-server-tools">
            <div className="return-latency-search">
              <Search size={15} aria-hidden="true" />
              <input type="search" aria-label="搜索执行服务器" placeholder="搜索名称、IP 或编号" value={serverQuery} onChange={event => setServerQuery(event.target.value)} />
            </div>
            <div className="probe-task-server-quick">
              <button type="button" className="latency-pill-btn" disabled={!suggested.length} onClick={() => setServerIDs(suggested.map(server => server.id))}>选择推荐 {suggested.length} 台</button>
              <button type="button" className="latency-pill-btn" disabled={!serverIDs.length} onClick={() => setServerIDs([])}>清空</button>
            </div>
          </div>
          <div className="probe-task-server-list">
            {!servers.length ? <p className="muted">暂无服务器，请先在服务器管理中添加。</p> : !visibleServers.length ? <p className="muted">没有匹配的服务器。</p> : visibleServers.map(server => (
              <label key={server.id} className={`probe-task-server${serverIDs.includes(server.id) ? ' is-selected' : ''}`}>
                <input type="checkbox" aria-label={`选择 ${server.name}`} checked={serverIDs.includes(server.id)} onChange={() => toggleServer(server.id)} />
                <span className="probe-task-server-main">
                  <strong>{server.name}</strong>
                  <small className="muted">{server.public_ipv4 || server.public_ipv6 || `#${server.id}`}</small>
                </span>
                <span className="probe-task-server-state muted">{online(server) ? (server.latency_probe_enabled ? '推荐' : '未启用探测') : server.agent_id ? '离线' : '未接入'}</span>
              </label>
            ))}
          </div>
        </section>
        {submitError && <p className="danger-text" role="alert">{submitError}</p>}
        <div className="return-latency-form-actions">
          <button type="button" className="ghost" onClick={onCancel}>取消</button>
          <button type="button" className="primary" disabled={!canSubmit} onClick={() => void submit()}>
            {saving && <Loader2 size={15} className="spin" aria-hidden="true" />}{task ? '保存任务' : '创建任务'}
          </button>
        </div>
      </fieldset>
    </div>
  )
}

