import React, { useState } from 'react'
import { ArrowLeftRight, ArrowRight, Cable, CircleAlert, Network, X } from 'lucide-react'

import type { Server } from '../proxy-path/types'
import { FormField } from '../ui/form-field'
import { MotionDialogPanel } from '../ui/motion'
import { regionFlagEmoji, serverRegionCode } from '../ui/RegionFlag'
import { Select } from '../ui/select'
import {
  REDACTED,
  firstError,
  listenLabel,
  probeOptions,
  protocolLabel,
  protocolOptions,
  serverAddress,
  suggestedForwardName,
  suggestedTunnelName,
  trafficForwardFieldErrors,
  tunnelFieldErrors,
  type ForwardProtocol,
  type ForwardingKind,
  type ProbeMode,
  type TrafficForwardDraft,
  type TunnelDraft,
} from './model'

export type ForwardingEditorState =
  | { kind: 'forward'; id?: number; draft: TrafficForwardDraft }
  | { kind: 'tunnel'; id?: number; draft: TunnelDraft }

const kindChoices: Array<{ kind: ForwardingKind; title: string; description: string; icon: React.ReactNode }> = [
  { kind: 'forward', title: '端口转发', description: '入口服务器监听端口，把流量转到服务器或任意地址', icon: <Network size={18} aria-hidden="true" /> },
  { kind: 'tunnel', title: '服务器隧道', description: '在两台服务器之间建立 WireGuard 加密链路', icon: <Cable size={18} aria-hidden="true" /> },
]

function serverOptionLabel(server: Server) {
  const code = serverRegionCode(server)
  return `${code ? `${regionFlagEmoji(code)} ` : ''}${server.name}${server.status === 'online' ? '' : '（离线）'}`
}

function FieldError({ message }: { message?: string }) {
  if (!message) return null
  return <small className="tf-field-error" role="alert">{message}</small>
}

function numberInput(value: number, onChange: (value: number) => void, props: React.InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }) {
  const { invalid, ...rest } = props
  return <input type="number" inputMode="numeric" value={value || ''} aria-invalid={invalid || undefined} onChange={event => onChange(Number(event.target.value))} {...rest} />
}

export function ForwardingEditorDialog({
  servers,
  initial,
  emptyDraft,
  onCancel,
  onSaveForward,
  onSaveTunnel,
}: {
  servers: Server[]
  initial: ForwardingEditorState
  emptyDraft: (kind: ForwardingKind, sourceServerID: number) => ForwardingEditorState
  onCancel: () => void
  onSaveForward: (draft: TrafficForwardDraft, id?: number) => Promise<void>
  onSaveTunnel: (draft: TunnelDraft, id?: number) => Promise<void>
}) {
  const [editor, setEditor] = useState<ForwardingEditorState>(initial)
  const [saving, setSaving] = useState(false)
  const [submitted, setSubmitted] = useState(false)
  const [error, setError] = useState('')
  const creating = !editor.id
  const errors: Partial<Record<string, string>> = editor.kind === 'forward' ? trafficForwardFieldErrors(editor.draft) : tunnelFieldErrors(editor.draft)
  const issue = firstError(errors)
  const shownErrors = submitted ? errors : {}

  const chooseKind = (kind: ForwardingKind) => {
    if (!creating || kind === editor.kind) return
    setEditor(emptyDraft(kind, editor.draft.source_server_id))
    setSubmitted(false)
    setError('')
  }

  const save = async () => {
    setSubmitted(true)
    if (issue || saving) return
    setSaving(true)
    setError('')
    try {
      if (editor.kind === 'forward') await onSaveForward(editor.draft, editor.id)
      else await onSaveTunnel(editor.draft, editor.id)
    } catch (cause: any) {
      setError(String(cause?.message || cause))
      setSaving(false)
    }
  }

  const title = creating ? '新建转发' : editor.kind === 'forward' ? '编辑端口转发' : '编辑服务器隧道'

  return (
    <MotionDialogPanel onCancel={() => { if (!saving) onCancel() }} className="tf-editor-dialog" aria-labelledby="tf-editor-title">
      <form className="tf-editor" aria-busy={saving} onSubmit={event => { event.preventDefault(); void save() }}>
        <header className="tf-editor-head">
          <div>
            <h2 id="tf-editor-title">{title}</h2>
            <p>保存后自动同步到相关服务器，离线服务器会在重新连接后同步。</p>
          </div>
          <button type="button" className="ghost icon-button dialog-close" onClick={onCancel} disabled={saving} aria-label="关闭" title="关闭"><X size={17} /></button>
        </header>

        <fieldset className="tf-editor-body" disabled={saving}>
          {creating && (
            <div className="tf-kind-choice" role="radiogroup" aria-label="转发类型">
              {kindChoices.map(choice => (
                <button
                  key={choice.kind}
                  type="button"
                  role="radio"
                  aria-checked={editor.kind === choice.kind}
                  className="tf-kind-option"
                  data-selected={editor.kind === choice.kind}
                  disabled={choice.kind === 'tunnel' && servers.length < 2}
                  title={choice.kind === 'tunnel' && servers.length < 2 ? '至少需要两台服务器' : undefined}
                  onClick={() => chooseKind(choice.kind)}
                >
                  <span className="tf-kind-icon">{choice.icon}</span>
                  <span><strong>{choice.title}</strong><small>{choice.description}</small></span>
                </button>
              ))}
            </div>
          )}

          {editor.kind === 'forward'
            ? <ForwardFields servers={servers} draft={editor.draft} errors={shownErrors} onChange={draft => setEditor({ ...editor, draft })} />
            : <TunnelFields servers={servers} draft={editor.draft} editing={!creating} errors={shownErrors} onChange={draft => setEditor({ ...editor, draft })} />}
        </fieldset>

        <footer className="tf-editor-foot">
          <div className="tf-editor-status" role={error || (submitted && issue) ? 'alert' : 'status'}>
            {error ? <><CircleAlert size={14} aria-hidden="true" />{error}</> : submitted && issue ? <><CircleAlert size={14} aria-hidden="true" />{issue}</> : null}
          </div>
          <div className="tf-editor-actions">
            <button type="button" className="ghost" onClick={onCancel} disabled={saving}>取消</button>
            <button type="submit" disabled={saving}>{saving ? '保存中…' : creating ? '创建并启用' : '保存'}</button>
          </div>
        </footer>
      </form>
    </MotionDialogPanel>
  )
}

function RoutePreview({ from, fromDetail, via, to, toDetail, bidirectional = false }: { from: string; fromDetail: string; via: string; to: string; toDetail: string; bidirectional?: boolean }) {
  return (
    <div className="tf-route-preview" aria-label="链路预览">
      <span><strong>{from}</strong><small>{fromDetail}</small></span>
      <span className="tf-route-via">{bidirectional ? <ArrowLeftRight size={15} aria-hidden="true" /> : <ArrowRight size={15} aria-hidden="true" />}<small>{via}</small></span>
      <span><strong>{to}</strong><small>{toDetail}</small></span>
    </div>
  )
}

function ForwardFields({ servers, draft, errors, onChange }: { servers: Server[]; draft: TrafficForwardDraft; errors: Partial<Record<string, string>>; onChange: (draft: TrafficForwardDraft) => void }) {
  const update = (patch: Partial<TrafficForwardDraft>) => onChange({ ...draft, ...patch })
  const source = servers.find(server => server.id === draft.source_server_id)
  const target = servers.find(server => server.id === draft.target_server_id)
  const [targetMode, setTargetMode] = useState<'server' | 'address'>(draft.target_server_id ? 'server' : 'address')
  const changeTargetMode = (mode: 'server' | 'address') => {
    setTargetMode(mode)
    if (mode === 'address') update({ target_server_id: 0 })
    else update({ target_server_id: servers.find(server => server.id !== draft.source_server_id)?.id || 0, target_address: '' })
  }
  const probe = probeOptions.find(option => option.value === draft.probe_mode)

  return (
    <>
      <RoutePreview
        from={source?.name || '入口服务器'}
        fromDetail={`${listenLabel(draft.listen_ip)}:${draft.listen_port || '—'}`}
        via={protocolLabel(draft.protocol)}
        to={target?.name || draft.target_address.trim() || '目标'}
        toDetail={`${draft.target_address.trim() || (target ? serverAddress(target) || '自动解析' : '—')}:${draft.target_port || '—'}`}
      />

      <section className="tf-form-section">
        <h3>从哪里进</h3>
        <div className="tf-form-grid">
          <FormField label="入口服务器" required>
            <><Select required value={draft.source_server_id} aria-label="入口服务器" onChange={event => {
              const sourceID = Number(event.target.value)
              update({ source_server_id: sourceID, target_server_id: sourceID === draft.target_server_id ? 0 : draft.target_server_id })
            }}>
              {servers.map(server => <option key={server.id} value={server.id}>{serverOptionLabel(server)}</option>)}
            </Select><FieldError message={errors.source_server_id} /></>
          </FormField>
          <FormField label="监听端口" required hint={source ? `推荐范围 ${source.port_range_start || 1}–${source.port_range_end || 65535}` : undefined}>
            <>{numberInput(draft.listen_port, listen_port => update({ listen_port }), { min: 1, max: 65535, required: true, 'aria-label': '监听端口', invalid: Boolean(errors.listen_port) })}<FieldError message={errors.listen_port} /></>
          </FormField>
        </div>
      </section>

      <section className="tf-form-section">
        <h3>转到哪里</h3>
        <Select variant="segmented" value={targetMode} aria-label="目标类型" onChange={event => changeTargetMode(event.target.value as 'server' | 'address')}>
          <option value="address">任意地址</option>
          <option value="server">受管服务器</option>
        </Select>
        <div className="tf-form-grid">
          {targetMode === 'server'
            ? <FormField label="目标服务器" required hint="目标地址按服务器地址和入口 IP 栈自动解析。">
              <><Select required value={draft.target_server_id} aria-label="目标服务器" onChange={event => update({ target_server_id: Number(event.target.value) })}>
                <option value={0}>选择服务器</option>
                {servers.filter(server => server.id !== draft.source_server_id).map(server => <option key={server.id} value={server.id}>{serverOptionLabel(server)}</option>)}
              </Select><FieldError message={errors.target_server_id || errors.target_address} /></>
            </FormField>
            : <FormField label="目标地址" required hint="IPv4、IPv6 或域名。">
              <><input required aria-label="目标地址" aria-invalid={Boolean(errors.target_address) || undefined} value={draft.target_address} onChange={event => update({ target_address: event.target.value })} placeholder="例如 203.0.113.10" /><FieldError message={errors.target_address} /></>
            </FormField>}
          <FormField label="目标端口" required>
            <>{numberInput(draft.target_port, target_port => update({ target_port }), { min: 1, max: 65535, required: true, 'aria-label': '目标端口', invalid: Boolean(errors.target_port) })}<FieldError message={errors.target_port} /></>
          </FormField>
          <FormField label="协议" full>
            <Select variant="segmented" value={draft.protocol} aria-label="协议" onChange={event => update({ protocol: event.target.value as ForwardProtocol })}>
              {protocolOptions.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
            </Select>
          </FormField>
          <FormField label="名称" full hint="留空时按入口和目标自动命名。">
            <><input aria-label="名称" maxLength={128} value={draft.name} onChange={event => update({ name: event.target.value })} placeholder={suggestedForwardName(draft, servers)} /><FieldError message={errors.name} /></>
          </FormField>
        </div>
      </section>

      <details className="tf-advanced" open={Boolean(errors.priority || errors.probe_interval_seconds || errors.config_json) || undefined}>
        <summary>高级设置<small>监听地址、健康检查、优先级</small></summary>
        <div className="tf-form-grid">
          <FormField label="监听 IP" hint="留空按入口服务器的监听模式自动选择。">
            <input aria-label="监听 IP" value={draft.listen_ip} onChange={event => update({ listen_ip: event.target.value })} placeholder="自动" />
          </FormField>
          {targetMode === 'server' && <FormField label="覆盖目标地址" hint="可选，填写后不再自动解析目标服务器地址。">
            <input aria-label="覆盖目标地址" value={draft.target_address} onChange={event => update({ target_address: event.target.value })} placeholder="自动解析" />
          </FormField>}
          <FormField label="健康检查" hint={probe?.description}>
            <Select value={draft.probe_mode} aria-label="健康检查" onChange={event => update({ probe_mode: event.target.value as ProbeMode })}>
              {probeOptions.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
            </Select>
          </FormField>
          {draft.probe_mode === 'periodic' && <FormField label="检查间隔（秒）" hint="最短 300 秒。">
            <>{numberInput(draft.probe_interval_seconds, probe_interval_seconds => update({ probe_interval_seconds }), { min: 300, step: 60, 'aria-label': '检查间隔', invalid: Boolean(errors.probe_interval_seconds) })}<FieldError message={errors.probe_interval_seconds} /></>
          </FormField>}
          <FormField label="优先级" hint="数值越小越先生效。">
            <>{numberInput(draft.priority, priority => update({ priority }), { min: 1, step: 1, 'aria-label': '优先级', invalid: Boolean(errors.priority) })}<FieldError message={errors.priority} /></>
          </FormField>
          <FormField label="JSON 配置" full hint="仅用于 OBoard 已支持的参数，通常保持 {}。转发由入口服务器上随 Agent 安装的 OBoard Realm 执行。">
            <><textarea aria-label="JSON 配置" rows={3} spellCheck={false} value={draft.config_json} onChange={event => update({ config_json: event.target.value })} /><FieldError message={errors.config_json} /></>
          </FormField>
        </div>
      </details>
    </>
  )
}

function SecretInput({ label, value, editing, invalid, onChange }: { label: string; value: string; editing: boolean; invalid: boolean; onChange: (value: string) => void }) {
  const kept = editing && value === REDACTED
  return <input
    type="password"
    autoComplete="off"
    aria-label={label}
    aria-invalid={invalid || undefined}
    value={kept ? '' : value}
    placeholder={kept ? '已保存，留空保持不变' : undefined}
    onChange={event => onChange(event.target.value || (editing ? REDACTED : ''))}
  />
}

function TunnelFields({ servers, draft, editing, errors, onChange }: { servers: Server[]; draft: TunnelDraft; editing: boolean; errors: Partial<Record<string, string>>; onChange: (draft: TunnelDraft) => void }) {
  const update = (patch: Partial<TunnelDraft>) => onChange({ ...draft, ...patch })
  const source = servers.find(server => server.id === draft.source_server_id)
  const target = servers.find(server => server.id === draft.target_server_id)
  const wireguard = draft.type === 'wireguard'
  const swap = () => update({ source_server_id: draft.target_server_id, target_server_id: draft.source_server_id })

  return (
    <>
      <RoutePreview
        from={source?.name || '源服务器'}
        fromDetail={draft.local_address.trim() || '隧道本端'}
        via={wireguard ? 'WireGuard' : 'SSH'}
        to={target?.name || '目标服务器'}
        toDetail={`${draft.target_endpoint.trim() || serverAddress(target) || '自动'}:${draft.target_port || '自动'}`}
        bidirectional
      />

      <section className="tf-form-section">
        <h3>连接哪两台服务器</h3>
        <div className="tf-form-grid tf-pair-grid">
          <FormField label="源服务器" required>
            <><Select required value={draft.source_server_id} aria-label="源服务器" onChange={event => {
              const sourceID = Number(event.target.value)
              update({ source_server_id: sourceID, target_server_id: sourceID === draft.target_server_id ? (servers.find(server => server.id !== sourceID)?.id || 0) : draft.target_server_id })
            }}>
              {servers.map(server => <option key={server.id} value={server.id}>{serverOptionLabel(server)}</option>)}
            </Select><FieldError message={errors.source_server_id} /></>
          </FormField>
          <button type="button" className="ghost icon-button tf-swap" onClick={swap} aria-label="交换源和目标" title="交换源和目标"><ArrowLeftRight size={15} /></button>
          <FormField label="目标服务器" required>
            <><Select required value={draft.target_server_id} aria-label="目标服务器" onChange={event => update({ target_server_id: Number(event.target.value) })}>
              {servers.filter(server => server.id !== draft.source_server_id).map(server => <option key={server.id} value={server.id}>{serverOptionLabel(server)}</option>)}
            </Select><FieldError message={errors.target_server_id} /></>
          </FormField>
        </div>
      </section>

      {wireguard && <section className="tf-form-section">
        <h3>密钥</h3>
        <div className="tf-form-grid">
          <FormField label="本机私钥" required hint="保存后不会再显示。">
            <><SecretInput label="本机私钥" value={draft.private_key} editing={editing} invalid={Boolean(errors.private_key)} onChange={private_key => update({ private_key })} /><FieldError message={errors.private_key} /></>
          </FormField>
          <FormField label="对端公钥" required>
            <><SecretInput label="对端公钥" value={draft.peer_public_key} editing={editing} invalid={Boolean(errors.peer_public_key)} onChange={peer_public_key => update({ peer_public_key })} /><FieldError message={errors.peer_public_key} /></>
          </FormField>
          <FormField label="名称" full hint="留空时按两端服务器自动命名。">
            <><input aria-label="名称" maxLength={128} value={draft.name} onChange={event => update({ name: event.target.value })} placeholder={suggestedTunnelName(draft, servers)} /><FieldError message={errors.name} /></>
          </FormField>
        </div>
      </section>}

      {!wireguard && <section className="tf-form-section">
        <div className="tf-form-grid">
          <FormField label="名称" full>
            <><input aria-label="名称" maxLength={128} value={draft.name} onChange={event => update({ name: event.target.value })} placeholder={suggestedTunnelName(draft, servers)} /><FieldError message={errors.name} /></>
          </FormField>
        </div>
      </section>}

      <details className="tf-advanced" open={Boolean(errors.listen_port || errors.target_port || errors.priority || errors.persistent_keepalive) || undefined}>
        <summary>高级设置<small>地址、端口、保活、优先级</small></summary>
        <div className="tf-form-grid">
          {wireguard && <FormField label="允许 IP" full hint="多个 CIDR 用逗号分隔。">
            <input aria-label="允许 IP" value={draft.allowed_ips} onChange={event => update({ allowed_ips: event.target.value })} placeholder="例如 10.10.0.0/30" />
          </FormField>}
          <FormField label="本端地址" hint="可选 CIDR，例如 10.10.0.1/30。">
            <input aria-label="本端地址" value={draft.local_address} onChange={event => update({ local_address: event.target.value })} />
          </FormField>
          <FormField label="对端地址" hint="可选 CIDR，例如 10.10.0.2/30。">
            <input aria-label="对端地址" value={draft.peer_address} onChange={event => update({ peer_address: event.target.value })} />
          </FormField>
          <FormField label="监听端口" hint="留空自动选择。">
            <>{numberInput(draft.listen_port, listen_port => update({ listen_port }), { min: 0, max: 65535, placeholder: '自动', 'aria-label': '隧道监听端口', invalid: Boolean(errors.listen_port) })}<FieldError message={errors.listen_port} /></>
          </FormField>
          <FormField label="目标端点" hint="可选主机或 IP，留空使用目标服务器地址。">
            <input aria-label="目标端点" value={draft.target_endpoint} onChange={event => update({ target_endpoint: event.target.value })} placeholder="自动" />
          </FormField>
          <FormField label="目标端口" hint="留空自动选择。">
            <>{numberInput(draft.target_port, target_port => update({ target_port }), { min: 0, max: 65535, placeholder: '自动', 'aria-label': '隧道目标端口', invalid: Boolean(errors.target_port) })}<FieldError message={errors.target_port} /></>
          </FormField>
          {wireguard && <FormField label="保活间隔（秒）" hint="0 表示关闭。">
            <><input type="number" min={0} max={65535} aria-label="保活间隔" value={draft.persistent_keepalive} onChange={event => update({ persistent_keepalive: Number(event.target.value) })} /><FieldError message={errors.persistent_keepalive} /></>
          </FormField>}
          <FormField label="优先级" hint="数值越小越先生效。">
            <>{numberInput(draft.priority, priority => update({ priority }), { min: 1, step: 1, 'aria-label': '隧道优先级', invalid: Boolean(errors.priority) })}<FieldError message={errors.priority} /></>
          </FormField>
        </div>
      </details>
    </>
  )
}
