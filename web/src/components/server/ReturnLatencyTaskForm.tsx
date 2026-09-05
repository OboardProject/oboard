import { useMemo, useState } from 'react'
import { Loader2, Search } from 'lucide-react'
import { FormField } from '../ui/form-field'
import { Select } from '../ui/select'
import { Switch } from '../ui/switch'
import type { LatencyProbeAddress, LatencyProbeRegion, LatencyProbeTask, NetworkProbeMethod, Server } from '../proxy-path/types'

const INTERVAL_PRESETS = [60, 300, 900, 1800, 3600]
const CARRIER_PRIORITY = ['中国电信', '中国联通', '中国移动', '中国广电', '教育网']

export const targetLabel = (province: string, carrier: string) => {
  const left = province.trim()
  const right = carrier.trim()
  if (!left) return right
  if (!right) return left
  return `${left} · ${right}`
}

export function ReturnLatencyTaskForm({ task, regions, targets = [], servers, loading, error, disabled, onSubmit, onCancel }: {
  task?: LatencyProbeTask | null
  regions: LatencyProbeRegion[]
  targets?: LatencyProbeAddress[]
  servers: Server[]
  loading?: boolean
  error?: string
  disabled?: boolean
  onSubmit: (payload: Omit<LatencyProbeTask, 'id' | 'created_at' | 'updated_at'>) => void | Promise<void>
  onCancel: () => void
}) {
  const [method, setMethod] = useState<NetworkProbeMethod>(task?.method || 'tcp')
  const [address, setAddress] = useState(task?.address || '')
  const [port, setPort] = useState<number | ''>(task?.port || 80)
  const [presetOpen, setPresetOpen] = useState(Boolean(task && !task.address))
  const [targetQuery, setTargetQuery] = useState('')
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

  const effectiveName = nameTouched && name.trim() ? name.trim() : Array.from(address.trim() || targetLabel(province, carrier)).slice(0, 60).join('')
  const online = (server: Server) => Boolean(server.agent_id) && server.status === 'online'
  const suggested = useMemo(() => servers.filter(server => online(server) && server.latency_probe_enabled), [servers])
  const visibleServers = useMemo(() => {
    const q = serverQuery.trim().toLowerCase()
    if (!q) return servers
    return servers.filter(server => `${server.name} ${server.id} ${server.public_ipv4 || ''} ${server.public_ipv6 || ''}`.toLowerCase().includes(q))
  }, [servers, serverQuery])

  const toggleServer = (id: number) => setServerIDs(current => current.includes(id) ? current.filter(item => item !== id) : [...current, id])
  const intervalInvalid = interval === '' || !Number.isInteger(Number(interval)) || Number(interval) < 30 || Number(interval) > 86400
  const legacyRegion = Boolean(task && !task.address && !address.trim() && province && carrier && method !== 'http')
  const addressError = (() => {
    const value = address.trim()
    if (!value) return legacyRegion ? '' : '请填写目标地址或从预设中选择。'
    if (method === 'http') {
      try { const url = new URL(value); if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.hash) return '请输入不含凭据或片段的 HTTP / HTTPS URL。' } catch { return '请输入完整 URL，例如 https://example.com/health。' }
    } else if (/[\s/:?#]/.test(value)) return '请只填写公网 IPv4 地址或域名，端口单独设置。'
    return ''
  })()
  const portInvalid = method === 'tcp' && (!Number.isInteger(port) || Number(port) < 1 || Number(port) > 65535)
  const canSubmit = !addressError && !portInvalid && !intervalInvalid && !saving && !disabled
  const visibleTargets = targets.filter(target => (!province || target.province === province) && (!carrier || target.carrier === carrier) && `${target.province} ${target.carrier} ${target.address}`.toLowerCase().includes(targetQuery.trim().toLowerCase()))
  const selectTarget = (target: LatencyProbeAddress) => {
    setAddress(method === 'http' ? `https://${target.address}` : target.address)
    if (!nameTouched) setName(targetLabel(target.province, target.carrier))
    setNameTouched(true)
    setPresetOpen(false)
  }

  const submit = async () => {
    if (!canSubmit) return
    setSaving(true)
    setSubmitError('')
    try {
      await onSubmit({ name: effectiveName, method, address: address.trim(), port: method === 'tcp' ? Number(port) : 0, province: legacyRegion ? province : '', carrier: legacyRegion ? carrier : '', interval_seconds: Number(interval), enabled, server_ids: serverIDs })
    } catch (err: any) {
      setSubmitError(err?.message || '保存失败，请重试')
    } finally {
      setSaving(false)
    }
  }

  return (
    <form className="probe-task-form" onSubmit={event => { event.preventDefault(); void submit() }}>
      <fieldset disabled={saving || disabled} className="return-latency-fields">
        <div className="probe-task-form-grid">
          <FormField label="探测方式" required full>
            <Select variant="segmented" aria-label="探测方式" value={method} onChange={event => { setMethod(event.target.value as NetworkProbeMethod) }}>
              <option value="tcp">TCP</option><option value="icmp">Ping</option><option value="http">HTTP</option>
            </Select>
            <p className="muted probe-method-hint">{method === 'tcp' ? '检测指定端口能否建立连接。' : method === 'icmp' ? '通过 ICMP 检测公网 IPv4 的可达性与延迟。' : '发送 GET 请求，跟随最多 3 次跳转，以 2xx 响应为成功。'}</p>
          </FormField>
          <FormField label={method === 'http' ? '目标 URL' : '目标地址'} required full={method !== 'tcp'}>
            <input aria-label="目标地址" required={!legacyRegion} aria-describedby="probe-address-hint" aria-invalid={Boolean(address && addressError)} type="text" maxLength={2048} placeholder={method === 'http' ? 'https://example.com/health' : '例如 example.com 或 1.1.1.1'} value={address} onChange={event => setAddress(event.target.value)} />
            <small id="probe-address-hint" className={address && addressError ? 'danger-text' : 'muted'}>{addressError || (legacyRegion ? `当前目标：${targetLabel(province, carrier)}。填写地址可改为指定目标。` : '由所选节点直接探测，目标须解析到公网 IPv4。')}</small>
          </FormField>
          {method === 'tcp' && <FormField label="端口" required>
            <input aria-label="目标端口" required aria-invalid={portInvalid} aria-describedby="probe-port-hint" type="number" min={1} max={65535} value={port} onChange={event => setPort(event.target.value === '' ? '' : Number(event.target.value))} />
            <small id="probe-port-hint" className={portInvalid ? 'danger-text' : 'muted'}>1–65535，例如 HTTPS 服务使用 443。</small>
          </FormField>}
          <div className="form-field-full probe-preset-picker">
            <button type="button" className="ghost" aria-expanded={presetOpen} aria-controls="probe-presets" onClick={() => setPresetOpen(!presetOpen)}>{presetOpen ? '收起预设目标' : '从预设中选择'}</button>
            {presetOpen && <section id="probe-presets" aria-label="预设目标" className="probe-preset-panel">
              <div className="probe-preset-filters">
                <Select aria-label="探测目标省份" value={province} onChange={event => { setProvince(event.target.value); setCarrier('') }}><option value="">全部省份</option>{provinces.map(item => <option key={item} value={item}>{item}</option>)}</Select>
                <Select aria-label="探测目标运营商" value={carrier} onChange={event => setCarrier(event.target.value)}><option value="">全部运营商</option>{carriers.map(item => <option key={item} value={item}>{item}</option>)}</Select>
              </div>
              <input type="search" aria-label="搜索预设目标" placeholder="搜索省份、运营商或地址" value={targetQuery} onChange={event => setTargetQuery(event.target.value)} />
              <div className="probe-preset-results">
                {loading ? <p className="muted" role="status">正在加载预设…</p> : !visibleTargets.length ? <p className="muted">{error ? '预设暂不可用，仍可手动填写地址。' : legacyRegion ? '当前任务使用所选地区的预设地址。填写地址可改为指定目标。' : '没有匹配的预设目标。'}</p> : visibleTargets.map(target => <button type="button" className="probe-preset-option" key={`${target.province}-${target.carrier}-${target.address}`} onClick={() => selectTarget(target)}><strong>{targetLabel(target.province, target.carrier)}</strong><span>{target.address}</span></button>)}
              </div>
            </section>}
          </div>
          <FormField label="任务名称" hint="留空自动使用目标地址。同名任务不可重复。">
            <input aria-label="任务名称" type="text" maxLength={60} placeholder={address || targetLabel(province, carrier) || '例如：网站可用性'} value={name} onChange={event => { setName(event.target.value); setNameTouched(true) }} />
          </FormField>
          <FormField label="探测间隔（秒）" required hint="该任务多久执行一次（30–86400 秒）。">
            <div className="probe-task-interval">
              <input aria-label="探测间隔（秒）" required aria-invalid={intervalInvalid} type="number" min={30} max={86400} value={interval} onChange={event => setInterval(event.target.value === '' ? '' : Number(event.target.value))} />
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
          <p className="muted">选择执行节点；未选择时任务不会执行。快捷选择应用于全部节点，不受搜索影响。</p>
          <div className="probe-task-server-tools">
            <div className="return-latency-search">
              <Search size={15} aria-hidden="true" />
              <input type="search" aria-label="搜索执行服务器" placeholder="搜索名称、IP 或编号" value={serverQuery} onChange={event => setServerQuery(event.target.value)} />
            </div>
            <div className="probe-task-server-quick">
              <button type="button" className="latency-pill-btn" disabled={!servers.length} onClick={() => setServerIDs(servers.map(server => server.id))}>选择全部</button>
              <button type="button" className="latency-pill-btn" disabled={!suggested.length} onClick={() => setServerIDs(suggested.map(server => server.id))}>选择在线 {suggested.length} 台</button>
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
          <button type="submit" className="primary" disabled={!canSubmit}>
            {saving && <Loader2 size={15} className="spin" aria-hidden="true" />}{task ? '保存任务' : '创建任务'}
          </button>
        </div>
      </fieldset>
    </form>
  )
}

