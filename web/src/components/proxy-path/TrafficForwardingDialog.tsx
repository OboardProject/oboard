import React, { useMemo, useState } from 'react'
import {
  Activity,
  ArrowLeft,
  ArrowRight,
  Braces,
  Cable,
  CheckCircle2,
  CircleAlert,
  Edit3,
  Gauge,
  Network,
  Play,
  Plus,
  Search,
  Server as ServerIcon,
  Settings2,
  SlidersHorizontal,
  Trash2,
  X,
} from 'lucide-react'

import type { Server } from './types'
import { FormField } from '../ui/form-field'
import { MotionDialogPanel } from '../ui/motion'
import { Select } from '../ui/select'
import { Switch } from '../ui/switch'
import './TrafficForwardingDialog.css'

export type ForwardProtocol = 'tcp' | 'udp' | 'tcp_udp'
export type ForwardBackend = 'auto' | 'realm' | 'nft' | 'builtin'
export type ProbeMode = 'never' | 'apply' | 'periodic' | 'sampled' | 'periodic_sampled'

export type TrafficForward = {
  id: number
  name: string
  source_server_id: number
  target_server_id: number
  listen_ip: string
  listen_port: number
  target_address: string
  target_port: number
  protocol: ForwardProtocol
  backend: ForwardBackend
  probe_mode: ProbeMode
  probe_interval_seconds: number
  sample_rate: number
  priority: number
  config_json: string
  enabled: boolean
  created_at?: string
  updated_at?: string
}

export type TrafficForwardProbe = {
  id: number
  port_forward_id: number
  server_id: number
  mode: string
  available: boolean
  latency_ms: number
  sample_count: number
  error: string
  result_json: string
  created_at: string
}

export type TrafficForwardDraft = Omit<TrafficForward, 'id' | 'created_at' | 'updated_at'>

type Notice = { tone: 'success' | 'danger'; message: string }

const protocolOptions: Array<{ value: ForwardProtocol; label: string }> = [
  { value: 'tcp', label: 'TCP' },
  { value: 'udp', label: 'UDP' },
  { value: 'tcp_udp', label: 'TCP + UDP' },
]

const backendOptions: Array<{ value: ForwardBackend; label: string; description: string }> = [
  { value: 'auto', label: '自动选择', description: '按服务器能力选择最合适的实现。' },
  { value: 'realm', label: 'Realm', description: '适合常规 TCP / UDP 转发。' },
  { value: 'nft', label: 'nftables', description: '使用内核转发，适合高吞吐场景。' },
  { value: 'builtin', label: '内置转发', description: '支持真实连接采样，目前仅限 TCP。' },
]

const probeOptions: Array<{ value: ProbeMode; label: string; description: string }> = [
  { value: 'periodic', label: '周期检查', description: '部署时检查，并按间隔持续复检。' },
  { value: 'apply', label: '部署时检查', description: '仅在配置下发后检查一次。' },
  { value: 'sampled', label: '连接采样', description: '按采样率记录真实连接建立延迟。' },
  { value: 'periodic_sampled', label: '周期 + 采样', description: '同时启用周期检查和真实连接采样。' },
  { value: 'never', label: '关闭自动检查', description: '仍可从列表手动发起检查。' },
]

export function TrafficForwardingDialog({
  servers,
  forwards,
  probes,
  initialServerID,
  onCancel,
  onSave,
  onDelete,
  onProbe,
  onOpenTunnel,
}: {
  servers: Server[]
  forwards: TrafficForward[]
  probes: TrafficForwardProbe[]
  initialServerID?: number
  onCancel: () => void
  onSave: (draft: TrafficForwardDraft, id?: number) => Promise<void>
  onDelete: (forward: TrafficForward) => Promise<boolean>
  onProbe: (forward: TrafficForward) => Promise<void>
  onOpenTunnel?: (sourceServerID: number) => void
}) {
  const initialSourceID = servers.some(server => server.id === initialServerID) ? Number(initialServerID) : Number(servers[0]?.id || 0)
  const [sourceServerID, setSourceServerID] = useState(initialSourceID)
  const [query, setQuery] = useState('')
  const [editor, setEditor] = useState<{ id?: number; draft: TrafficForwardDraft } | null>(null)
  const [notice, setNotice] = useState<Notice | null>(null)
  const [saving, setSaving] = useState(false)
  const [busyIDs, setBusyIDs] = useState<Set<number>>(() => new Set())

  const sourceServer = servers.find(server => server.id === sourceServerID)
  const sourceForwards = useMemo(
    () => forwards
      .filter(forward => forward.source_server_id === sourceServerID)
      .sort((left, right) => (left.priority - right.priority) || left.name.localeCompare(right.name, 'zh-CN')),
    [forwards, sourceServerID],
  )
  const visibleForwards = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase('zh-CN')
    if (!normalized) return sourceForwards
    return sourceForwards.filter(forward => {
      const target = servers.find(server => server.id === forward.target_server_id)
      return [forward.name, target?.name, forward.target_address, String(forward.listen_port), String(forward.target_port)]
        .some(value => String(value || '').toLocaleLowerCase('zh-CN').includes(normalized))
    })
  }, [query, servers, sourceForwards])
  const probeByForward = useMemo(() => latestProbes(probes, forwards), [probes, forwards])
  const enabledCount = sourceForwards.filter(forward => forward.enabled).length
  const healthyCount = sourceForwards.filter(forward => forward.enabled && probeByForward.get(forward.id)?.available).length
  const attentionCount = sourceForwards.filter(forward => forward.enabled && probeByForward.get(forward.id)?.available === false).length

  const chooseSourceServer = (serverID: number) => {
    if (editor) return
    setSourceServerID(serverID)
    setNotice(null)
  }

  const openCreate = () => {
    if (servers.length < 2 || !sourceServerID) return
    setNotice(null)
    setEditor({ draft: emptyTrafficForwardDraft(servers, forwards, sourceServerID) })
  }

  const openEdit = (forward: TrafficForward) => {
    setSourceServerID(forward.source_server_id)
    setNotice(null)
    setEditor({ id: forward.id, draft: trafficForwardDraftFromForward(forward) })
  }

  const save = async () => {
    if (!editor) return
    const issue = validateTrafficForwardDraft(editor.draft)
    if (issue) {
      setNotice({ tone: 'danger', message: issue })
      return
    }
    setSaving(true)
    setNotice(null)
    try {
      await onSave(trafficForwardPayload(editor.draft), editor.id)
      setSourceServerID(editor.draft.source_server_id)
      setEditor(null)
      setNotice({ tone: 'success', message: editor.id ? '转发配置已保存，系统正在自动同步。' : '流量转发已创建，系统正在自动同步。' })
    } catch (error: any) {
      setNotice({ tone: 'danger', message: String(error?.message || error) })
    } finally {
      setSaving(false)
    }
  }

  const removeForward = async (forward: TrafficForward) => {
    setBusyIDs(current => new Set(current).add(forward.id))
    setNotice(null)
    try {
      const deleted = await onDelete(forward)
      if (deleted) setNotice({ tone: 'success', message: `已删除「${forward.name}」。` })
    } catch (error: any) {
      setNotice({ tone: 'danger', message: String(error?.message || error) })
    } finally {
      setBusyIDs(current => { const next = new Set(current); next.delete(forward.id); return next })
    }
  }

  const probeForward = async (forward: TrafficForward) => {
    setBusyIDs(current => new Set(current).add(forward.id))
    setNotice(null)
    try {
      await onProbe(forward)
      setNotice({ tone: 'success', message: `已创建「${forward.name}」的检查任务，可稍后查看最新结果。` })
    } catch (error: any) {
      setNotice({ tone: 'danger', message: String(error?.message || error) })
    } finally {
      setBusyIDs(current => { const next = new Set(current); next.delete(forward.id); return next })
    }
  }

  const toggleForward = async (forward: TrafficForward, enabled: boolean) => {
    setBusyIDs(current => new Set(current).add(forward.id))
    setNotice(null)
    try {
      await onSave(trafficForwardPayload({ ...trafficForwardDraftFromForward(forward), enabled }), forward.id)
      setNotice({ tone: 'success', message: enabled ? `已启用「${forward.name}」。` : `已停用「${forward.name}」。` })
    } catch (error: any) {
      setNotice({ tone: 'danger', message: String(error?.message || error) })
    } finally {
      setBusyIDs(current => { const next = new Set(current); next.delete(forward.id); return next })
    }
  }

  return (
    <MotionDialogPanel onCancel={() => { if (!saving && busyIDs.size === 0) onCancel() }} className="traffic-forwarding-dialog" aria-labelledby="traffic-forwarding-title">
      <header className="traffic-forwarding-head">
        <div className="traffic-forwarding-title-block">
          <span className="traffic-forwarding-title-icon" aria-hidden="true"><Network size={18} /></span>
          <div>
            <h2 id="traffic-forwarding-title">流量转发</h2>
            <p>按第一层入口服务器管理独立端口转发，保存后自动同步到相关节点。</p>
          </div>
        </div>
        <div className="traffic-forwarding-head-actions">
          {onOpenTunnel && <button type="button" className="ghost" onClick={() => onOpenTunnel(sourceServerID)} disabled={Boolean(editor) || saving || busyIDs.size > 0}><Cable size={15} aria-hidden="true" />服务器隧道</button>}
          <button type="button" className="ghost icon-button dialog-close" onClick={onCancel} disabled={saving || busyIDs.size > 0} aria-label="关闭流量转发" title="关闭"><X size={17} /></button>
        </div>
      </header>

      <div className="traffic-forwarding-shell">
        <aside className="traffic-forwarding-sidebar" aria-label="入口服务器">
          <div className="traffic-forwarding-sidebar-head">
            <span>入口服务器</span>
            <small>第一层服务器</small>
          </div>
          <div className="traffic-forwarding-server-list">
            {servers.map(server => {
              const serverForwardCount = forwards.filter(forward => forward.source_server_id === server.id).length
              const isOnline = server.status === 'online'
              return (
                <button
                  type="button"
                  key={server.id}
                  className="traffic-forwarding-server"
                  data-selected={server.id === sourceServerID}
                  aria-pressed={server.id === sourceServerID}
                  aria-label={`选择入口服务器 ${server.name}`}
                  disabled={Boolean(editor)}
                  onClick={() => chooseSourceServer(server.id)}
                >
                  <span className="traffic-forwarding-server-icon" data-online={isOnline} aria-hidden="true"><ServerIcon size={15} /></span>
                  <span className="traffic-forwarding-server-copy">
                    <strong>{server.name}</strong>
                    <small>{serverAddress(server)} · {isOnline ? '在线' : '离线'}</small>
                  </span>
                  <span className="traffic-forwarding-server-count tabular-nums">{serverForwardCount}</span>
                </button>
              )
            })}
          </div>
          {!servers.length && <div className="traffic-forwarding-sidebar-empty">还没有可用服务器</div>}
          <div className="traffic-forwarding-sidebar-note">
            <CircleAlert size={14} aria-hidden="true" />
            <p>入口服务器负责公开监听；目标服务器负责接收被转发的流量。</p>
          </div>
        </aside>

        <main className="traffic-forwarding-main">
          {editor ? (
            <TrafficForwardEditor
              servers={servers}
              editor={editor}
              notice={notice}
              saving={saving}
              onBack={() => { setEditor(null); setNotice(null) }}
              onChange={draft => setEditor(current => current ? { ...current, draft } : current)}
              onSave={() => void save()}
            />
          ) : (
            <>
              <section className="traffic-forwarding-overview" aria-label="转发概况">
                <div>
                  <span>当前入口</span>
                  <strong>{sourceServer?.name || '未选择服务器'}</strong>
                  <small>{sourceServer ? `${serverAddress(sourceServer)} · ${sourceServer.status === 'online' ? 'Agent 在线' : 'Agent 离线，配置将在恢复后同步'}` : '请先添加服务器'}</small>
                </div>
                <div className="traffic-forwarding-stats">
                  <TrafficStat label="已启用" value={enabledCount} icon={<Activity size={14} />} tone="primary" />
                  <TrafficStat label="检查正常" value={healthyCount} icon={<CheckCircle2 size={14} />} tone="success" />
                  <TrafficStat label="需要处理" value={attentionCount} icon={<CircleAlert size={14} />} tone="danger" />
                </div>
              </section>

              <section className="traffic-forwarding-list-section">
                <div className="traffic-forwarding-toolbar">
                  <div>
                    <h3>转发列表</h3>
                    <p>{sourceForwards.length ? `共 ${sourceForwards.length} 条，按优先级执行` : '为当前入口建立第一条转发规则'}</p>
                  </div>
                  <div className="traffic-forwarding-toolbar-actions">
                    <label className="traffic-forwarding-search">
                      <Search size={14} aria-hidden="true" />
                      <input value={query} onChange={event => setQuery(event.target.value)} aria-label="搜索转发" placeholder="搜索名称、目标或端口" />
                    </label>
                    <button type="button" onClick={openCreate} disabled={servers.length < 2 || !sourceServerID}><Plus size={15} aria-hidden="true" />创建转发</button>
                  </div>
                </div>

                {notice && <InlineNotice notice={notice} />}

                <div className="traffic-forwarding-list" aria-live="polite">
                  {visibleForwards.map(forward => (
                    <TrafficForwardRow
                      key={forward.id}
                      forward={forward}
                      servers={servers}
                      probe={probeByForward.get(forward.id)}
                      busy={busyIDs.has(forward.id)}
                      onEdit={() => openEdit(forward)}
                      onProbe={() => void probeForward(forward)}
                      onDelete={() => void removeForward(forward)}
                      onToggle={enabled => void toggleForward(forward, enabled)}
                    />
                  ))}
                  {!visibleForwards.length && (
                    <div className="traffic-forwarding-empty">
                      <span aria-hidden="true"><Network size={22} /></span>
                      <strong>{query ? '没有匹配的转发' : servers.length < 2 ? '需要至少两台服务器' : '还没有流量转发'}</strong>
                      <p>{query ? '换一个名称、目标或端口试试。' : servers.length < 2 ? '添加第二台服务器后，即可配置入口与目标之间的流量转发。' : '创建规则后，客户端流量会从当前入口转发到目标服务器。'}</p>
                      {!query && servers.length >= 2 && <button type="button" onClick={openCreate}><Plus size={15} aria-hidden="true" />创建第一条转发</button>}
                    </div>
                  )}
                </div>
              </section>
            </>
          )}
        </main>
      </div>
    </MotionDialogPanel>
  )
}

function TrafficForwardEditor({
  servers,
  editor,
  notice,
  saving,
  onBack,
  onChange,
  onSave,
}: {
  servers: Server[]
  editor: { id?: number; draft: TrafficForwardDraft }
  notice: Notice | null
  saving: boolean
  onBack: () => void
  onChange: (draft: TrafficForwardDraft) => void
  onSave: () => void
}) {
  const draft = editor.draft
  const update = (patch: Partial<TrafficForwardDraft>) => onChange({ ...draft, ...patch })
  const source = servers.find(server => server.id === draft.source_server_id)
  const target = servers.find(server => server.id === draft.target_server_id)
  const backend = backendOptions.find(option => option.value === draft.backend)
  const probe = probeOptions.find(option => option.value === draft.probe_mode)
  const sampled = draft.probe_mode === 'sampled' || draft.probe_mode === 'periodic_sampled'
  const periodic = draft.probe_mode === 'periodic' || draft.probe_mode === 'periodic_sampled'
  const fieldErrors = trafficForwardFieldErrors(draft)
  const validationIssue = validateTrafficForwardDraft(draft)

  const changeSource = (serverID: number) => {
    const nextTargetID = serverID === draft.target_server_id
      ? Number(servers.find(server => server.id !== serverID)?.id || 0)
      : draft.target_server_id
    update({ source_server_id: serverID, target_server_id: nextTargetID })
  }

  return (
    <form className="traffic-forwarding-editor" aria-busy={saving} onSubmit={event => { event.preventDefault(); onSave() }}>
      <div className="traffic-forwarding-editor-head">
        <button type="button" className="ghost" onClick={onBack} disabled={saving}><ArrowLeft size={15} aria-hidden="true" />返回列表</button>
        <div>
          <h3>{editor.id ? '编辑流量转发' : '创建流量转发'}</h3>
          <p>{editor.id ? '调整完整规则参数，保存后自动同步。' : '配置从第一层入口到目标服务器的监听与转发策略。'}</p>
        </div>
      </div>

      <fieldset className="traffic-forwarding-editor-scroll" disabled={saving}>
        <section className="traffic-forwarding-route-preview" aria-label="转发路径预览">
          <RoutePoint eyebrow="第一层入口" title={source?.name || '选择入口服务器'} detail={`${effectiveListenLabel(draft.listen_ip)}:${draft.listen_port || '—'}`} />
          <span className="traffic-forwarding-route-arrow" aria-hidden="true"><ArrowRight size={17} /></span>
          <RoutePoint eyebrow="流量转发" title={protocolLabel(draft.protocol)} detail={backend?.label || draft.backend} active />
          <span className="traffic-forwarding-route-arrow" aria-hidden="true"><ArrowRight size={17} /></span>
          <RoutePoint eyebrow="目标服务器" title={target?.name || '选择目标服务器'} detail={`${draft.target_address.trim() || '自动解析'}:${draft.target_port || '—'}`} />
        </section>

        <section className="traffic-forwarding-form-section">
          <div className="traffic-forwarding-section-head"><Settings2 size={16} aria-hidden="true" /><div><h4>基本信息</h4><p>用于识别规则和控制运行状态。</p></div></div>
          <div className="traffic-forwarding-form-grid">
            <FormField label="转发名称" required full><><input autoFocus required aria-label="转发名称" aria-invalid={Boolean(fieldErrors.name)} aria-describedby={fieldErrorDescription(fieldErrors, 'name')} value={draft.name} maxLength={128} onChange={event => update({ name: event.target.value })} placeholder="例如 香港入口 → 东京应用" /><ForwardFieldError errors={fieldErrors} field="name" /></></FormField>
            <FormField label="优先级" required hint="数值越小越先执行；建议从 100 开始。"><><input type="number" min={1} step={1} required aria-label="优先级" aria-invalid={Boolean(fieldErrors.priority)} aria-describedby={fieldErrorDescription(fieldErrors, 'priority')} value={draft.priority} onChange={event => update({ priority: Number(event.target.value) })} /><ForwardFieldError errors={fieldErrors} field="priority" /></></FormField>
            <FormField label="运行状态" hint={editor.id ? '停用后保留配置，但不再监听端口。' : '新建规则会立即启用。'}>
              <div className="traffic-forwarding-switch-field"><span>{editor.id ? (draft.enabled ? '已启用' : '已停用') : '创建后立即启用'}</span><Switch checked={editor.id ? draft.enabled : true} disabled={!editor.id} onChange={enabled => update({ enabled })} ariaLabel="转发运行状态" /></div>
            </FormField>
          </div>
        </section>

        <section className="traffic-forwarding-form-section">
          <div className="traffic-forwarding-section-head"><Network size={16} aria-hidden="true" /><div><h4>转发端点</h4><p>入口服务器公开监听，目标服务器接收转发流量。</p></div></div>
          <div className="traffic-forwarding-form-grid">
            <FormField label="入口服务器（第一层）" required>
              <><Select required value={draft.source_server_id} onChange={event => changeSource(Number(event.target.value))} aria-label="入口服务器" aria-describedby={fieldErrorDescription(fieldErrors, 'source_server_id')}>
                {servers.map(server => <option value={server.id} key={server.id}>{server.name}{server.status === 'online' ? '' : '（离线）'}</option>)}
              </Select><ForwardFieldError errors={fieldErrors} field="source_server_id" /></>
            </FormField>
            <FormField label="目标服务器" required>
              <><Select required value={draft.target_server_id} onChange={event => update({ target_server_id: Number(event.target.value) })} aria-label="目标服务器" aria-describedby={fieldErrorDescription(fieldErrors, 'target_server_id')}>
                {servers.filter(server => server.id !== draft.source_server_id).map(server => <option value={server.id} key={server.id}>{server.name}{server.status === 'online' ? '' : '（离线）'}</option>)}
              </Select><ForwardFieldError errors={fieldErrors} field="target_server_id" /></>
            </FormField>
            <FormField label="监听 IP" hint="留空或填 0.0.0.0 时，按入口服务器的监听模式自动选择 IPv4 或双栈地址。"><input aria-label="监听 IP" value={draft.listen_ip} onChange={event => update({ listen_ip: event.target.value })} placeholder="自动（推荐）" /></FormField>
            <FormField label="监听端口" required hint={source ? `该服务器推荐公网端口范围 ${source.port_range_start || 1}–${source.port_range_end || 65535}，手工转发也可使用范围外空闲端口。` : undefined}><><input type="number" min={1} max={65535} inputMode="numeric" required aria-label="监听端口" aria-invalid={Boolean(fieldErrors.listen_port)} aria-describedby={fieldErrorDescription(fieldErrors, 'listen_port')} value={draft.listen_port || ''} onChange={event => update({ listen_port: Number(event.target.value) })} /><ForwardFieldError errors={fieldErrors} field="listen_port" /></></FormField>
            <FormField label="目标地址" hint="可选。留空时按目标服务器地址和入口服务器 IP 栈自动解析。"><><input aria-label="目标地址" aria-invalid={Boolean(fieldErrors.target_address)} aria-describedby={fieldErrorDescription(fieldErrors, 'target_address')} value={draft.target_address} onChange={event => update({ target_address: event.target.value })} placeholder="自动解析目标服务器" /><ForwardFieldError errors={fieldErrors} field="target_address" /></></FormField>
            <FormField label="目标端口" required><><input type="number" min={1} max={65535} inputMode="numeric" required aria-label="目标端口" aria-invalid={Boolean(fieldErrors.target_port)} aria-describedby={fieldErrorDescription(fieldErrors, 'target_port')} value={draft.target_port || ''} onChange={event => update({ target_port: Number(event.target.value) })} /><ForwardFieldError errors={fieldErrors} field="target_port" /></></FormField>
          </div>
          {(source?.status !== 'online' || target?.status !== 'online') && <p className="traffic-forwarding-form-note"><CircleAlert size={14} aria-hidden="true" />离线服务器仍可保存配置；Agent 恢复连接后会自动同步最新状态。</p>}
        </section>

        <section className="traffic-forwarding-form-section">
          <div className="traffic-forwarding-section-head"><SlidersHorizontal size={16} aria-hidden="true" /><div><h4>转发策略</h4><p>选择传输协议、执行后端和健康检查方式。</p></div></div>
          <div className="traffic-forwarding-form-grid">
            <FormField label="传输协议" required full>
              <Select required variant="segmented" value={draft.protocol} onChange={event => {
                const protocol = event.target.value as ForwardProtocol
                update({ protocol, backend: draft.backend === 'builtin' && protocol !== 'tcp' ? 'auto' : draft.backend })
              }} aria-label="传输协议">
                {protocolOptions.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
              </Select>
            </FormField>
            <FormField label="转发后端" required hint={backend?.description}>
              <><Select required value={draft.backend} onChange={event => update({ backend: event.target.value as ForwardBackend })} aria-label="转发后端" aria-describedby={fieldErrorDescription(fieldErrors, 'backend')}>
                {backendOptions.map(option => <option key={option.value} value={option.value} disabled={option.value === 'builtin' && draft.protocol !== 'tcp'}>{option.label}</option>)}
              </Select><ForwardFieldError errors={fieldErrors} field="backend" /></>
            </FormField>
            <FormField label="检查方式" required hint={probe?.description}>
              <Select required value={draft.probe_mode} onChange={event => update({ probe_mode: event.target.value as ProbeMode })} aria-label="检查方式">
                {probeOptions.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
              </Select>
            </FormField>
            {periodic && <FormField label="检查间隔（秒）" required hint="最短 300 秒。"><><input type="number" min={300} step={60} inputMode="numeric" required aria-label="检查间隔" aria-invalid={Boolean(fieldErrors.probe_interval_seconds)} aria-describedby={fieldErrorDescription(fieldErrors, 'probe_interval_seconds')} value={draft.probe_interval_seconds || ''} onChange={event => update({ probe_interval_seconds: Number(event.target.value) })} /><ForwardFieldError errors={fieldErrors} field="probe_interval_seconds" /></></FormField>}
            {sampled && <FormField label="真实连接采样率" required hint="填写 0 到 1，例如 0.1 表示抽取 10% 的连接。"><><input type="number" min={0.01} max={1} step={0.01} inputMode="decimal" required aria-label="真实连接采样率" aria-invalid={Boolean(fieldErrors.sample_rate)} aria-describedby={fieldErrorDescription(fieldErrors, 'sample_rate')} value={draft.sample_rate || ''} onChange={event => update({ sample_rate: Number(event.target.value) })} /><ForwardFieldError errors={fieldErrors} field="sample_rate" /></></FormField>}
          </div>
        </section>

        <details className="traffic-forwarding-advanced">
          <summary><span><Braces size={15} aria-hidden="true" /><strong>高级 JSON 配置</strong></span><small>通常保持为 {'{}'}</small></summary>
          <div>
            <p>仅用于 OBoard 已支持的高级参数；未知字段不会被透明传递给底层内核。</p>
            <textarea aria-label="高级 JSON 配置" aria-invalid={Boolean(fieldErrors.config_json)} aria-describedby={fieldErrorDescription(fieldErrors, 'config_json')} rows={7} spellCheck={false} value={draft.config_json} onChange={event => update({ config_json: event.target.value })} />
            <ForwardFieldError errors={fieldErrors} field="config_json" />
          </div>
        </details>
      </fieldset>

      <footer className="traffic-forwarding-editor-footer">
        <div>{notice ? <InlineNotice notice={notice} compact /> : validationIssue ? <span className="traffic-forwarding-validation" role="alert" aria-live="polite"><CircleAlert size={14} aria-hidden="true" />{validationIssue}</span> : <span className="traffic-forwarding-ready" role="status"><CheckCircle2 size={14} aria-hidden="true" />参数完整，可以保存</span>}</div>
        <div><button type="button" className="ghost" onClick={onBack} disabled={saving}>取消</button><button type="submit" disabled={saving}>{saving ? '保存中…' : editor.id ? '保存修改' : '创建并启用'}</button></div>
      </footer>
    </form>
  )
}

function TrafficForwardRow({
  forward,
  servers,
  probe,
  busy,
  onEdit,
  onProbe,
  onDelete,
  onToggle,
}: {
  forward: TrafficForward
  servers: Server[]
  probe?: TrafficForwardProbe
  busy: boolean
  onEdit: () => void
  onProbe: () => void
  onDelete: () => void
  onToggle: (enabled: boolean) => void
}) {
  const source = servers.find(server => server.id === forward.source_server_id)
  const target = servers.find(server => server.id === forward.target_server_id)
  const probeDetails = parseProbeDetails(probe)
  const health = forwardHealth(forward, probe)
  return (
    <article className="traffic-forwarding-row" data-enabled={forward.enabled}>
      <div className="traffic-forwarding-row-primary">
        <span className="traffic-forwarding-protocol">{protocolLabel(forward.protocol)}</span>
        <div><strong title={forward.name}>{forward.name}</strong><small>优先级 <span className="tabular-nums">{forward.priority}</span> · {backendLabel(forward.backend)}</small></div>
      </div>
      <div className="traffic-forwarding-row-route">
        <span><small>{source?.name || `服务器 ${forward.source_server_id}`}</small><strong>{effectiveListenLabel(forward.listen_ip)}:{forward.listen_port}</strong></span>
        <ArrowRight size={15} aria-hidden="true" />
        <span><small>{target?.name || `服务器 ${forward.target_server_id}`}</small><strong>{forward.target_address || '自动解析'}:{forward.target_port}</strong></span>
      </div>
      <div className="traffic-forwarding-row-health" data-tone={health.tone}>
        <span aria-hidden="true">{health.tone === 'success' ? <CheckCircle2 size={14} /> : health.tone === 'danger' ? <CircleAlert size={14} /> : <Gauge size={14} />}</span>
        <div><strong>{health.label}</strong><small>{health.detail}</small></div>
      </div>
      <div className="traffic-forwarding-row-sampling">
        <span>{probeModeLabel(forward.probe_mode)}</span>
        <small>{probe ? `${probeDetails.successCount}/${probe.sample_count} 次成功` : forward.probe_mode === 'never' ? '仅手动检查' : '等待首次结果'}</small>
      </div>
      <div className="traffic-forwarding-row-actions">
        <Switch checked={forward.enabled} disabled={busy} onChange={onToggle} ariaLabel={`${forward.enabled ? '停用' : '启用'} ${forward.name}`} size="sm" />
        <button type="button" className="ghost" onClick={onProbe} disabled={busy || !forward.enabled} aria-label={`立即检查 ${forward.name}`} title="立即检查"><Play size={14} aria-hidden="true" /></button>
        <button type="button" className="ghost" onClick={onEdit} disabled={busy} aria-label={`编辑 ${forward.name}`} title="编辑"><Edit3 size={14} aria-hidden="true" /></button>
        <button type="button" className="ghost danger-text" onClick={onDelete} disabled={busy} aria-label={`删除 ${forward.name}`} title="删除"><Trash2 size={14} aria-hidden="true" /></button>
      </div>
    </article>
  )
}

function TrafficStat({ label, value, icon, tone }: { label: string; value: number; icon: React.ReactNode; tone: 'primary' | 'success' | 'danger' }) {
  return <div className="traffic-forwarding-stat" data-tone={tone} aria-label={`${label} ${value}`}><span aria-hidden="true">{icon}</span><div><strong className="tabular-nums">{value}</strong><small>{label}</small></div></div>
}

function RoutePoint({ eyebrow, title, detail, active = false }: { eyebrow: string; title: string; detail: string; active?: boolean }) {
  return <div className="traffic-forwarding-route-point" data-active={active}><small>{eyebrow}</small><strong>{title}</strong><span>{detail}</span></div>
}

function InlineNotice({ notice, compact = false }: { notice: Notice; compact?: boolean }) {
  return <div className="traffic-forwarding-notice" data-tone={notice.tone} data-compact={compact} role={notice.tone === 'danger' ? 'alert' : 'status'}>{notice.tone === 'success' ? <CheckCircle2 size={15} aria-hidden="true" /> : <CircleAlert size={15} aria-hidden="true" />}<span>{notice.message}</span></div>
}

function fieldErrorID(field: TrafficForwardField) {
  return `traffic-forwarding-${field}-error`
}

function fieldErrorDescription(errors: Partial<Record<TrafficForwardField, string>>, field: TrafficForwardField) {
  return errors[field] ? fieldErrorID(field) : undefined
}

function ForwardFieldError({ errors, field }: { errors: Partial<Record<TrafficForwardField, string>>; field: TrafficForwardField }) {
  if (!errors[field]) return null
  return <small id={fieldErrorID(field)} className="traffic-forwarding-field-error" role="alert">{errors[field]}</small>
}

export function emptyTrafficForwardDraft(servers: Server[], forwards: TrafficForward[], sourceServerID?: number): TrafficForwardDraft {
  const source = servers.find(server => server.id === sourceServerID) || servers[0]
  const target = servers.find(server => server.id !== source?.id)
  const listenPort = nextListenPort(source, forwards)
  return {
    name: source && target ? `${source.name} → ${target.name}` : '新建流量转发',
    source_server_id: Number(source?.id || 0),
    target_server_id: Number(target?.id || 0),
    listen_ip: '',
    listen_port: listenPort,
    target_address: '',
    target_port: 443,
    protocol: 'tcp',
    backend: 'auto',
    probe_mode: 'periodic',
    probe_interval_seconds: 300,
    sample_rate: 0,
    priority: 100,
    config_json: '{}',
    enabled: true,
  }
}

export function trafficForwardDraftFromForward(forward: TrafficForward): TrafficForwardDraft {
  const { id: _id, created_at: _createdAt, updated_at: _updatedAt, ...draft } = forward
  return { ...draft }
}

export function trafficForwardPayload(draft: TrafficForwardDraft): TrafficForwardDraft {
  return {
    ...draft,
    name: draft.name.trim(),
    listen_ip: draft.listen_ip.trim(),
    target_address: draft.target_address.trim(),
    config_json: draft.config_json.trim() || '{}',
  }
}

export function validateTrafficForwardDraft(draft: TrafficForwardDraft): string {
  return Object.values(trafficForwardFieldErrors(draft))[0] || ''
}

type TrafficForwardField = 'name' | 'source_server_id' | 'target_server_id' | 'listen_port' | 'target_address' | 'target_port' | 'priority' | 'backend' | 'probe_interval_seconds' | 'sample_rate' | 'config_json'

export function trafficForwardFieldErrors(draft: TrafficForwardDraft): Partial<Record<TrafficForwardField, string>> {
  const errors: Partial<Record<TrafficForwardField, string>> = {}
  if (!draft.name.trim()) errors.name = '请填写转发名称。'
  else if (draft.name.trim().length > 128) errors.name = '转发名称不能超过 128 个字符。'
  if (!draft.source_server_id) errors.source_server_id = '请选择入口服务器。'
  if (!draft.target_server_id) errors.target_server_id = '请选择目标服务器。'
  else if (draft.source_server_id === draft.target_server_id) errors.target_server_id = '入口服务器和目标服务器不能相同。'
  if (!validPort(draft.listen_port)) errors.listen_port = '监听端口必须是 1 到 65535 的整数。'
  if (!validPort(draft.target_port)) errors.target_port = '目标端口必须是 1 到 65535 的整数。'
  if (!Number.isInteger(draft.priority) || draft.priority < 1) errors.priority = '优先级必须是大于 0 的整数。'
  if (draft.backend === 'builtin' && draft.protocol !== 'tcp') errors.backend = '内置转发后端目前只支持 TCP。'
  if (!Number.isInteger(draft.probe_interval_seconds) || draft.probe_interval_seconds < 300) errors.probe_interval_seconds = '检查间隔不能少于 300 秒。'
  if (!Number.isFinite(draft.sample_rate) || draft.sample_rate < 0 || draft.sample_rate > 1) errors.sample_rate = '真实连接采样率必须在 0 到 1 之间。'
  else if ((draft.probe_mode === 'sampled' || draft.probe_mode === 'periodic_sampled') && draft.sample_rate <= 0) errors.sample_rate = '连接采样模式需要填写大于 0 的采样率。'
  if (/\s/.test(draft.target_address.trim())) errors.target_address = '目标地址不能包含空格。'
  try {
    const parsed = JSON.parse(draft.config_json.trim() || '{}')
    if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') errors.config_json = '高级配置必须是 JSON 对象。'
  } catch {
    errors.config_json = '高级配置不是有效的 JSON。'
  }
  return errors
}

function nextListenPort(server: Server | undefined, forwards: TrafficForward[]) {
  if (!server) return 10000
  const used = new Set(forwards.filter(forward => forward.source_server_id === server.id).map(forward => forward.listen_port))
  const preferred = server.port_range_start > 0 ? server.port_range_start : 10000
  for (let port = Math.max(1, preferred); port <= 65535; port++) if (!used.has(port)) return port
  for (let port = 10000; port < preferred; port++) if (!used.has(port)) return port
  return 10000
}

function latestProbes(probes: TrafficForwardProbe[], forwards: TrafficForward[]) {
  const result = new Map<number, TrafficForwardProbe>()
  const forwardByID = new Map(forwards.map(forward => [forward.id, forward]))
  const updatedAt = new Map(forwards.map(forward => [forward.id, timestamp(forward.updated_at)]))
  probes.forEach(probe => {
    const probeAt = timestamp(probe.created_at)
    const forwardUpdatedAt = updatedAt.get(probe.port_forward_id) || 0
    const reportedUpdatedAt = timestamp(parseProbeDetails(probe).forwardUpdatedAt)
    if (reportedUpdatedAt && forwardUpdatedAt && reportedUpdatedAt !== forwardUpdatedAt) return
    if (reportedUpdatedAt && !forwardByID.has(probe.port_forward_id)) return
    if (forwardUpdatedAt && (!probeAt || probeAt < forwardUpdatedAt)) return
    const current = result.get(probe.port_forward_id)
    if (!current || probeAt > timestamp(current.created_at)) result.set(probe.port_forward_id, probe)
  })
  return result
}

function timestamp(value?: string) {
  const parsed = value ? new Date(value).getTime() : 0
  return Number.isFinite(parsed) ? parsed : 0
}

function parseProbeDetails(probe?: TrafficForwardProbe) {
  if (!probe) return { successCount: 0, p95: 0, jitter: 0, forwardUpdatedAt: '' }
  try {
    const raw = JSON.parse(probe.result_json || '{}')
    return {
      successCount: Number(raw.success_count ?? (probe.available ? probe.sample_count : 0)),
      p95: Number(raw.p95_latency_ms || 0),
      jitter: Number(raw.jitter_ms || 0),
      forwardUpdatedAt: String(raw.forward_updated_at || ''),
    }
  } catch {
    return { successCount: probe.available ? probe.sample_count : 0, p95: 0, jitter: 0, forwardUpdatedAt: '' }
  }
}

function forwardHealth(forward: TrafficForward, probe?: TrafficForwardProbe) {
  if (!forward.enabled) return { tone: 'neutral' as const, label: '已停用', detail: '不再监听端口' }
  if (!probe) return { tone: 'neutral' as const, label: '等待检查', detail: forward.probe_mode === 'never' ? '可手动检查' : '同步后生成结果' }
  if (!probe.available) return { tone: 'danger' as const, label: '转发异常', detail: probe.error || '目标不可达' }
  const details = parseProbeDetails(probe)
  return { tone: 'success' as const, label: `${probe.latency_ms || 0} ms`, detail: details.p95 ? `P95 ${details.p95} ms · 抖动 ${details.jitter} ms` : '最近检查正常' }
}

function serverAddress(server: Server) {
  return server.entry_address || server.public_ipv4 || server.public_ipv6 || server.interface_ipv6 || '等待地址'
}

function effectiveListenLabel(value: string) {
  return value.trim() && value.trim() !== '0.0.0.0' ? value.trim() : '自动监听'
}

function validPort(value: number) {
  return Number.isInteger(value) && value >= 1 && value <= 65535
}

function protocolLabel(value: ForwardProtocol) {
  return protocolOptions.find(option => option.value === value)?.label || value
}

function backendLabel(value: ForwardBackend) {
  return backendOptions.find(option => option.value === value)?.label || value
}

function probeModeLabel(value: ProbeMode) {
  return probeOptions.find(option => option.value === value)?.label || value
}
