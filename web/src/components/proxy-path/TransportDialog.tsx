import React, { useEffect, useMemo, useState } from 'react'
import { ArrowLeftRight, Eye, EyeOff, Shield, Workflow, X } from 'lucide-react'
import { MotionDialogPanel } from '../ui/motion'

export type TransportMode = 'singbox' | 'port_forward' | 'tunnel'
export type TunnelKind = 'ssh' | 'wireguard'
export type GeneratedChainProtocol = 'shadowsocks' | 'vless' | 'mieru'
export type BranchCopyMode = 'none' | 'all' | 'single'

export type ProxyPathReuseSource = { inbound_id?: number; step_id?: number }

export type ProxyPathReuseRequest = {
  sources: ProxyPathReuseSource[]
  target_server_id: number
  target_kind: 'generated' | 'existing'
  target_inbound_id?: number
  chain_protocol?: GeneratedChainProtocol
  chain_method?: string
  reality_handshake_server?: string
  reality_handshake_port?: number
  transport_mode: TransportMode
  tunnel_type?: TunnelKind
  ssh_port?: number
  persistent_keepalive?: number
  copy_mode: BranchCopyMode
  branch_path_id?: number
}

export type ProxyPathReuseTargetOption = {
  kind: 'generated' | 'existing'
  inbound_id?: number
  protocol: string
  label: string
  port?: number
  visibility: 'system_hidden' | 'existing_visible'
  active_reuse_count: number
  eligible: boolean
  reason?: string
  chain_method?: string
}

export type ProxyPathReuseBranchOption = {
  path_id: number
  name: string
  kind: 'chain' | 'direct'
  eligible: boolean
  reason?: string
  step_count: number
}

export type ProxyPathReusePreview = {
  target_options: ProxyPathReuseTargetOption[]
  branch_options: ProxyPathReuseBranchOption[]
  valid: boolean
  error?: string
  source_count?: number
  result_path_count?: number
  affected_server_ids?: number[]
}

export type TransportSelection = {
  transport_mode: TransportMode
  processing_role: false
  config_json: string
  target_kind?: 'generated' | 'existing'
  target_server_id?: number
  target_inbound_id?: number
  reuse_request?: ProxyPathReuseRequest
}

export type TransportDialogTarget = {
  sourceLabel: string
  targetLabel: string
  targetServerID?: number
  targetInboundID?: number
  sources?: ProxyPathReuseSource[]
  importedOnly?: boolean
  editing?: boolean
  staticTargetOptions?: ProxyPathReuseTargetOption[]
}

export type ChainMethodOption = { value: string; label: string }

const DEFAULT_CHAIN_METHOD = '2022-blake3-aes-128-gcm'
const DEFAULT_REALITY_SERVER = 'cdn.icloud-content.com'
const DEFAULT_REALITY_PORT = 443
const DEFAULT_KEEPALIVE = 25

export function TransportDialog({
  target,
  current,
  currentMode,
  chainMethods,
  onPreview,
  onCancel,
  onSubmit,
}: {
  target: TransportDialogTarget
  current?: string
  currentMode?: TransportMode
  chainMethods: ChainMethodOption[]
  onPreview?: (request: ProxyPathReuseRequest) => Promise<ProxyPathReusePreview>
  onCancel: () => void
  onSubmit: (selection: TransportSelection) => Promise<void> | void
}) {
  const existing = useMemo(() => parseConfig(current), [current])
  const reuseEnabled = Boolean(target.targetServerID && target.sources?.length && !target.importedOnly && !target.editing)
  const previewAvailable = Boolean(onPreview)
  const initialProtocol = generatedProtocol(existing.chain_protocol)
  const [mode, setMode] = useState<TransportMode>(() => target.importedOnly ? 'singbox' : currentMode || 'singbox')
  const [targetKind, setTargetKind] = useState<'generated' | 'existing'>(() => target.targetInboundID ? 'existing' : 'generated')
  const [targetInboundID, setTargetInboundID] = useState<number>(() => target.targetInboundID || 0)
  const [chainProtocol, setChainProtocol] = useState<GeneratedChainProtocol>(initialProtocol)
  const [chainMethod, setChainMethod] = useState<string>(() => stringOr(existing.chain_method, DEFAULT_CHAIN_METHOD))
  const [realityServer, setRealityServer] = useState<string>(() => stringOr(existing.reality_handshake_server, DEFAULT_REALITY_SERVER))
  const [realityPort, setRealityPort] = useState<string>(() => String(numberOr(existing.reality_handshake_port, DEFAULT_REALITY_PORT)))
  const [tunnelKind, setTunnelKind] = useState<TunnelKind>(() => existing.type === 'wireguard' ? 'wireguard' : 'ssh')
  const [sshPort, setSSHPort] = useState<string>(() => {
    const stored = numberOr(existing.ssh_port, 0)
    return stored > 0 ? String(stored) : ''
  })
  const [keepalive, setKeepalive] = useState<string>(() => String(numberOr(existing.persistent_keepalive, DEFAULT_KEEPALIVE)))
  const [copyMode, setCopyMode] = useState<BranchCopyMode>('none')
  const [branchPathID, setBranchPathID] = useState(0)
  const [preview, setPreview] = useState<ProxyPathReusePreview | null>(null)
  const [previewLoading, setPreviewLoading] = useState(reuseEnabled)
  const [previewError, setPreviewError] = useState('')
  const [saving, setSaving] = useState(false)
  const previewHandlerRef = React.useRef(onPreview)
  previewHandlerRef.current = onPreview

  const sshPortValue = Number(sshPort)
  const keepaliveValue = Number(keepalive)
  const realityPortValue = Number(realityPort)
  const needsSSHPort = mode === 'tunnel' && tunnelKind === 'ssh'
  const sshPortInvalid = needsSSHPort && (!Number.isInteger(sshPortValue) || sshPortValue < 1 || sshPortValue > 65535)
  const keepaliveInvalid = mode === 'tunnel' && tunnelKind === 'wireguard' && (!Number.isInteger(keepaliveValue) || keepaliveValue < 0 || keepaliveValue > 65535)
  const realityInvalid = targetKind === 'generated' && chainProtocol === 'vless' && (realityServer.trim() === '' || !Number.isInteger(realityPortValue) || realityPortValue < 1 || realityPortValue > 65535)
  const branchInvalid = targetKind === 'existing' && copyMode === 'single' && branchPathID <= 0

  const reuseRequest = useMemo<ProxyPathReuseRequest | null>(() => {
    if (!reuseEnabled || !target.targetServerID || !target.sources) return null
    return buildProxyPathReuseRequest({
      sources: target.sources,
      targetServerID: target.targetServerID,
      targetKind,
      targetInboundID,
      chainProtocol,
      chainMethod,
      realityServer,
      realityPort: realityPortValue,
      mode,
      tunnelKind,
      sshPort: sshPortValue,
      keepalive: keepaliveValue,
      copyMode: targetKind === 'existing' ? copyMode : 'none',
      branchPathID,
    })
  }, [reuseEnabled, target.targetServerID, target.sources, targetKind, targetInboundID, chainProtocol, chainMethod, realityServer, realityPortValue, mode, tunnelKind, sshPortValue, keepaliveValue, copyMode, branchPathID])

  useEffect(() => {
    if (!reuseRequest || !previewAvailable || !previewHandlerRef.current || sshPortInvalid || keepaliveInvalid || realityInvalid || branchInvalid) {
      setPreview(null)
      setPreviewLoading(false)
      setPreviewError('')
      return
    }
    let active = true
    const runPreview = previewHandlerRef.current
    setPreviewLoading(true)
    setPreviewError('')
    const timer = window.setTimeout(() => {
      void runPreview(reuseRequest).then(result => {
        if (!active) return
        setPreview(result)
        setPreviewError(result.error || '')
      }).catch(error => {
        if (!active) return
        setPreview(null)
        setPreviewError(String(error?.message || error))
      }).finally(() => {
        if (active) setPreviewLoading(false)
      })
    }, 120)
    return () => {
      active = false
      window.clearTimeout(timer)
    }
  }, [reuseRequest, previewAvailable, sshPortInvalid, keepaliveInvalid, realityInvalid, branchInvalid])

  const targetOptions = preview?.target_options || target.staticTargetOptions || []
  const generatedOptions = targetOptions.filter(option => option.kind === 'generated')
  const existingOptions = targetOptions.filter(option => option.kind === 'existing')
  const branchOptions = preview?.branch_options || []
  const selectedTarget = targetOptions.find(option => targetOptionSelected(option, targetKind, targetInboundID, chainProtocol, chainMethod))
  const blocked = sshPortInvalid || keepaliveInvalid || realityInvalid || branchInvalid || (reuseEnabled && (previewLoading || !preview?.valid))

  const chooseTarget = (option: ProxyPathReuseTargetOption) => {
    if (!option.eligible) return
    if (option.kind === 'existing') {
      setTargetKind('existing')
      setTargetInboundID(option.inbound_id || 0)
      setCopyMode('none')
      setBranchPathID(0)
      return
    }
    setTargetKind('generated')
    setTargetInboundID(0)
    setChainProtocol(generatedProtocol(option.protocol))
    if (option.protocol === 'shadowsocks') setChainMethod(option.chain_method || DEFAULT_CHAIN_METHOD)
    setCopyMode('none')
    setBranchPathID(0)
  }

  const submit = async () => {
    if (blocked) return
    setSaving(true)
    try {
      const configJSON = buildTransportConfig({ targetKind, chainProtocol, chainMethod, realityServer, realityPort: realityPortValue, mode, tunnelKind, sshPort: sshPortValue, keepalive: keepaliveValue })
      await onSubmit({
        transport_mode: mode,
        processing_role: false,
        config_json: configJSON,
        target_kind: targetKind,
        target_server_id: target.targetServerID,
        target_inbound_id: targetKind === 'existing' ? targetInboundID : undefined,
        reuse_request: reuseRequest || undefined,
      })
    } finally {
      setSaving(false)
    }
  }

  const modeOptions: Array<{ value: TransportMode; label: string; hint: string; icon: React.ReactNode; disabled?: boolean }> = [
    { value: 'singbox', label: 'sing-box 出站链', hint: '由当前服务器建立协议出站，连接所选系统服务或已有入口。', icon: <Workflow size={14} /> },
    { value: 'port_forward', label: '透明端口转发', hint: target.importedOnly ? '导入节点不能作为端口转发目标。' : '原样传递客户端密文，只能位于链路开头。', icon: <ArrowLeftRight size={14} />, disabled: target.importedOnly },
    { value: 'tunnel', label: 'SSH / WireGuard 隧道', hint: target.importedOnly ? '导入节点不能作为隧道端点。' : '通过服务器间专用隧道连接所选入口。', icon: <Shield size={14} />, disabled: target.importedOnly },
  ]

  return (
    <MotionDialogPanel onCancel={onCancel} className="transport-dialog">
      <header className="dialog-head">
        <div><h2>{target.editing ? '编辑传递方式' : '选择传递方式'}</h2><p className="muted">{target.sourceLabel} → {target.targetLabel}</p></div>
        <button type="button" className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭"><X size={16} /></button>
      </header>
      <div className="dialog-body transport-dialog-body">
        <div className="transport-mode-options" role="radiogroup" aria-label="传递方式">
          {modeOptions.map(option => <button key={option.value} type="button" role="radio" aria-checked={mode === option.value} disabled={option.disabled} className={`transport-mode-option${mode === option.value ? ' is-active' : ''}`} onClick={() => setMode(option.value)}>
            <span className="transport-mode-head">{option.icon}<strong>{option.label}</strong></span><span className="transport-mode-hint">{option.hint}</span>
          </button>)}
        </div>

        {!target.importedOnly && targetOptions.length > 0 && <div className="transport-target-groups">
          <TargetGroup title="系统生成入口" options={generatedOptions} selected={selectedTarget} onChoose={chooseTarget} />
          <TargetGroup title="已有入口" options={existingOptions} selected={selectedTarget} onChoose={chooseTarget} />
        </div>}

        {targetKind === 'generated' && chainProtocol === 'vless' && <div className="transport-inline-fields">
          <label className="transport-field"><span>Reality 握手域名</span><input value={realityServer} onChange={event => setRealityServer(event.target.value)} aria-invalid={realityInvalid} /></label>
          <label className="transport-field"><span>握手端口</span><input type="number" min={1} max={65535} value={realityPort} onChange={event => setRealityPort(event.target.value)} aria-invalid={realityInvalid} /></label>
          {realityInvalid && <small className="transport-field-error">请输入有效域名和 1 到 65535 的端口。</small>}
        </div>}

        {mode === 'tunnel' && <label className="transport-field"><span>隧道类型</span><select value={tunnelKind} onChange={event => setTunnelKind(event.target.value as TunnelKind)}><option value="ssh">SSH 隧道</option><option value="wireguard">WireGuard 隧道</option></select></label>}
        {needsSSHPort && <label className="transport-field"><span>目标端隧道服务端口</span><input type="number" min={1} max={65535} value={sshPort} placeholder="1-65535" onChange={event => setSSHPort(event.target.value)} aria-invalid={sshPortInvalid} /><small className={sshPortInvalid ? 'transport-field-error' : 'muted'}>{sshPortInvalid ? '端口必须是 1 到 65535 的整数。' : '这是 OBoard 专用隧道服务端口，不是服务器登录端口。'}</small></label>}
        {mode === 'tunnel' && tunnelKind === 'wireguard' && <label className="transport-field"><span>保活间隔（秒）</span><input type="number" min={0} max={65535} value={keepalive} onChange={event => setKeepalive(event.target.value)} aria-invalid={keepaliveInvalid} /><small className={keepaliveInvalid ? 'transport-field-error' : 'muted'}>{keepaliveInvalid ? '保活间隔必须是 0 到 65535 的整数。' : '0 表示不发送保活包。'}</small></label>}

        {reuseEnabled && targetKind === 'existing' && <section className="transport-branch-copy">
          <div><strong>复制已有分支</strong><span className="muted">只复制启用分支，复制后独立保存。</span></div>
          <div className="transport-copy-modes" role="radiogroup" aria-label="复制已有分支">
            <button type="button" className={copyMode === 'none' ? 'is-active' : ''} onClick={() => { setCopyMode('none'); setBranchPathID(0) }}>不复制</button>
            <button type="button" className={copyMode === 'all' ? 'is-active' : ''} onClick={() => { setCopyMode('all'); setBranchPathID(0) }}>全部分支</button>
            <button type="button" className={copyMode === 'single' ? 'is-active' : ''} onClick={() => setCopyMode('single')}>单条分支</button>
          </div>
          {copyMode === 'single' && <div className="transport-branch-list">{branchOptions.map(branch => <label key={branch.path_id} className={!branch.eligible ? 'is-disabled' : ''}><input type="radio" name="branch-path" value={branch.path_id} checked={branchPathID === branch.path_id} disabled={!branch.eligible} onChange={() => setBranchPathID(branch.path_id)} /><span><strong>{branch.name}</strong><small>{branch.kind === 'direct' ? '直接出口' : `${branch.step_count} 个后续节点`}{branch.reason ? ` · ${branch.reason}` : ''}</small></span></label>)}</div>}
          {copyMode === 'all' && branchOptions.some(branch => !branch.eligible) && <div className="transport-invalid-branches">{branchOptions.filter(branch => !branch.eligible).map(branch => <span key={branch.path_id}><strong>{branch.name}</strong>{branch.reason || '无法复制'}</span>)}</div>}
        </section>}

        <div className="transport-preview">
          <span>本次变更</span>
          <strong>{describeSelection(target, mode, selectedTarget, tunnelKind, sshPortValue, preview)}</strong>
          {reuseEnabled && <div className="transport-preview-status" aria-live="polite">
            {previewLoading ? <small className="muted">正在检查拓扑...</small> : previewError ? <small className="transport-field-error">{previewError}</small> : preview?.valid ? <small className="muted">拓扑检查通过</small> : null}
          </div>}
        </div>
      </div>
      <footer className="dialog-actions"><button type="button" className="ghost" onClick={onCancel}>取消</button><button type="button" onClick={() => void submit()} disabled={saving || blocked}>{saving ? '保存中...' : '确定'}</button></footer>
    </MotionDialogPanel>
  )
}

function TargetGroup({ title, options, selected, onChoose }: { title: string; options: ProxyPathReuseTargetOption[]; selected?: ProxyPathReuseTargetOption; onChoose: (option: ProxyPathReuseTargetOption) => void }) {
  if (!options.length) return null
  return <section className="transport-target-group"><h3>{title}</h3><div>{options.map(option => {
    const active = selected ? targetOptionKey(selected) === targetOptionKey(option) : false
    return <button key={targetOptionKey(option)} type="button" className={active ? 'is-active' : ''} disabled={!option.eligible} onClick={() => onChoose(option)} title={option.reason || option.label}>
      <span className="transport-target-main"><strong>{option.label}</strong>{option.port ? <code>:{option.port}</code> : null}</span>
      <span className="transport-target-meta">{option.visibility === 'system_hidden' ? <><EyeOff size={12} /> 系统隐藏</> : <><Eye size={12} /> 普通入口</>}<small>复用 {option.active_reuse_count || 0}</small></span>
    </button>
  })}</div></section>
}

export function buildProxyPathReuseRequest(input: {
  sources: ProxyPathReuseSource[]; targetServerID: number; targetKind: 'generated' | 'existing'; targetInboundID: number
  chainProtocol: GeneratedChainProtocol; chainMethod: string; realityServer: string; realityPort: number
  mode: TransportMode; tunnelKind: TunnelKind; sshPort: number; keepalive: number; copyMode: BranchCopyMode; branchPathID: number
}): ProxyPathReuseRequest {
  const request: ProxyPathReuseRequest = {
    sources: input.sources,
    target_server_id: input.targetServerID,
    target_kind: input.targetKind,
    transport_mode: input.mode,
    copy_mode: input.targetKind === 'existing' ? input.copyMode : 'none',
  }
  if (input.targetKind === 'existing') request.target_inbound_id = input.targetInboundID
  else {
    request.chain_protocol = input.chainProtocol
    if (input.chainProtocol === 'shadowsocks') request.chain_method = input.chainMethod
    if (input.chainProtocol === 'vless') {
      request.reality_handshake_server = input.realityServer.trim()
      request.reality_handshake_port = input.realityPort
    }
  }
  if (input.mode === 'tunnel') {
    request.tunnel_type = input.tunnelKind
    if (input.tunnelKind === 'ssh') request.ssh_port = input.sshPort
    else request.persistent_keepalive = input.keepalive
  }
  if (request.copy_mode === 'single') request.branch_path_id = input.branchPathID
  return request
}

function buildTransportConfig(input: { targetKind: 'generated' | 'existing'; chainProtocol: GeneratedChainProtocol; chainMethod: string; realityServer: string; realityPort: number; mode: TransportMode; tunnelKind: TunnelKind; sshPort: number; keepalive: number }) {
  if (input.mode === 'port_forward') return '{}'
  const config: Record<string, string | number> = {}
  if (input.targetKind === 'generated') {
    config.chain_protocol = input.chainProtocol
    if (input.chainProtocol === 'shadowsocks') config.chain_method = input.chainMethod
    if (input.chainProtocol === 'vless') {
      config.reality_handshake_server = input.realityServer.trim()
      config.reality_handshake_port = input.realityPort
    }
  }
  if (input.mode === 'tunnel') {
    config.type = input.tunnelKind
    if (input.tunnelKind === 'ssh') config.ssh_port = input.sshPort
    else config.persistent_keepalive = input.keepalive
  }
  return JSON.stringify(config)
}

function describeSelection(target: TransportDialogTarget, mode: TransportMode, selected: ProxyPathReuseTargetOption | undefined, tunnelKind: TunnelKind, sshPort: number, preview: ProxyPathReusePreview | null) {
  const service = selected?.kind === 'existing' ? `复用 ${selected.label}` : selected?.label || '系统生成入口'
  const transport = mode === 'port_forward' ? '透明端口转发' : mode === 'tunnel' ? `${tunnelKind === 'wireguard' ? 'WireGuard' : `SSH${sshPort > 0 ? ` :${sshPort}` : ''}`} 隧道` : 'sing-box 出站链'
  const count = preview?.result_path_count ? ` · 生成 ${preview.result_path_count} 条路径` : ''
  return `${target.sourceLabel} → ${target.targetLabel} · ${transport} · ${service}${count}`
}

function targetOptionSelected(option: ProxyPathReuseTargetOption, kind: 'generated' | 'existing', inboundID: number, protocol: GeneratedChainProtocol, method: string) {
  if (option.kind !== kind) return false
  if (kind === 'existing') return option.inbound_id === inboundID
  if (option.protocol !== protocol) return false
  return protocol !== 'shadowsocks' || (option.chain_method || DEFAULT_CHAIN_METHOD) === method
}

function targetOptionKey(option: ProxyPathReuseTargetOption) {
  return option.kind === 'existing' ? `existing:${option.inbound_id}` : `generated:${option.protocol}:${option.chain_method || ''}`
}

function generatedProtocol(value: any): GeneratedChainProtocol {
  return value === 'vless' || value === 'mieru' ? value : 'shadowsocks'
}

function parseConfig(raw?: string): Record<string, any> {
  if (!raw) return {}
  try {
    const parsed = JSON.parse(raw)
    return parsed && typeof parsed === 'object' ? parsed : {}
  } catch {
    return {}
  }
}

function stringOr(value: any, fallback: string) {
  const text = typeof value === 'string' ? value.trim() : ''
  return text === '' ? fallback : text
}

function numberOr(value: any, fallback: number) {
  const parsed = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(parsed) ? parsed : fallback
}
