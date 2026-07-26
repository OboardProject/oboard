import React, { useMemo, useState } from 'react'
import { ArrowLeftRight, Shield, Workflow, X } from 'lucide-react'
import { MotionDialogPanel } from '../ui/motion'

export type TransportMode = 'singbox' | 'port_forward' | 'tunnel'
export type TunnelKind = 'ssh' | 'wireguard'

export type TransportSelection = {
  transport_mode: TransportMode
  processing_role: false
  config_json: string
}

export type TransportDialogTarget = {
  /** Label of the hop this selection applies to, shown in the preview. */
  sourceLabel: string
  targetLabel: string
  /** Set when the operator picked an existing inbound instead of a server. */
  targetInboundLabel?: string
  /** The target server's stored SSH port, 0 when unset. */
  targetSSHPort?: number
  /** An imported node can only be reached through a sing-box outbound. */
  importedOnly?: boolean
}

export type ChainMethodOption = { value: string; label: string }

const DEFAULT_CHAIN_METHOD = '2022-blake3-aes-128-gcm'
const DEFAULT_KEEPALIVE = 25

// One panel replaces the chained prompts this flow used to run. Every parameter
// stays visible and revisable while the operator decides, and the preview line
// spells out the hop that will be created.
export function TransportDialog({
  target,
  current,
  chainMethods,
  onCancel,
  onSubmit,
}: {
  target: TransportDialogTarget
  current?: string
  chainMethods: ChainMethodOption[]
  onCancel: () => void
  onSubmit: (selection: TransportSelection) => Promise<void> | void
}) {
  const existing = useMemo(() => parseConfig(current), [current])
  const usesExistingInbound = Boolean(target.targetInboundLabel)
  const [mode, setMode] = useState<TransportMode>(() => {
    if (target.importedOnly) return 'singbox'
    const stored = existing.__mode
    return stored === 'port_forward' || stored === 'tunnel' ? stored : 'singbox'
  })
  const [chainMethod, setChainMethod] = useState<string>(() => stringOr(existing.chain_method, DEFAULT_CHAIN_METHOD))
  const [tunnelKind, setTunnelKind] = useState<TunnelKind>(() => (existing.type === 'wireguard' ? 'wireguard' : 'ssh'))
  const [sshPort, setSSHPort] = useState<string>(() => {
    const stored = numberOr(existing.ssh_port, 0)
    if (stored > 0) return String(stored)
    return target.targetSSHPort ? String(target.targetSSHPort) : ''
  })
  const [keepalive, setKeepalive] = useState<string>(() => String(numberOr(existing.persistent_keepalive, DEFAULT_KEEPALIVE)))
  const [saving, setSaving] = useState(false)

  // A generated hop dials the shared Shadowsocks service, so the method matters.
  // An explicitly chosen inbound keeps its own protocol and port instead.
  const needsChainMethod = !usesExistingInbound && mode !== 'port_forward'
  const needsSSHPort = mode === 'tunnel' && tunnelKind === 'ssh'
  const sshPortValue = Number(sshPort)
  const sshPortInvalid = needsSSHPort && (!Number.isInteger(sshPortValue) || sshPortValue < 1 || sshPortValue > 65535)
  const keepaliveValue = Number(keepalive)
  const keepaliveInvalid = mode === 'tunnel' && tunnelKind === 'wireguard' && (!Number.isInteger(keepaliveValue) || keepaliveValue < 0 || keepaliveValue > 65535)
  const blocked = sshPortInvalid || keepaliveInvalid

  const submit = async () => {
    if (blocked) return
    setSaving(true)
    try {
      await onSubmit({ transport_mode: mode, processing_role: false, config_json: buildConfigJSON() })
    } finally {
      setSaving(false)
    }
  }

  const buildConfigJSON = () => {
    if (mode === 'port_forward') return '{}'
    const config: Record<string, string | number> = {}
    if (needsChainMethod) config.chain_method = chainMethod
    if (mode === 'tunnel') {
      config.type = tunnelKind
      if (tunnelKind === 'ssh' && sshPortValue > 0) config.ssh_port = sshPortValue
      if (tunnelKind === 'wireguard') config.persistent_keepalive = keepaliveValue
    }
    return JSON.stringify(config)
  }

  const modeOptions: Array<{ value: TransportMode; label: string; hint: string; icon: React.ReactNode; disabled?: boolean }> = [
    {
      value: 'singbox',
      label: 'sing-box 出站链',
      hint: usesExistingInbound
        ? `直接连接 ${target.targetInboundLabel}，保留该入口原有协议与端口。`
        : '连接目标服务器上的共享 Shadowsocks 服务。相同目标与加密方法复用同一端口。',
      icon: <Workflow size={14} />,
    },
    {
      value: 'port_forward',
      label: '透明端口转发',
      hint: target.importedOnly
        ? '导入节点不能作为端口转发目标。'
        : '原样搬运客户端密文，由后端服务器解密。只能出现在链路开头。',
      icon: <ArrowLeftRight size={14} />,
      disabled: target.importedOnly,
    },
    {
      value: 'tunnel',
      label: 'SSH / WireGuard 隧道',
      hint: target.importedOnly ? '导入节点不能作为隧道端点。' : '通过服务器间隧道到达下一跳，凭据由系统自动生成。',
      icon: <Shield size={14} />,
      disabled: target.importedOnly,
    },
  ]

  return (
    <MotionDialogPanel onCancel={onCancel} className="transport-dialog">
      <header className="dialog-head">
        <div>
          <h2>选择传递方式</h2>
          <p className="muted">{target.sourceLabel} → {target.targetLabel}</p>
        </div>
        <button type="button" className="ghost dialog-close icon-button" onClick={onCancel} aria-label="关闭" title="关闭">
          <X size={16} />
        </button>
      </header>
      <div className="dialog-body transport-dialog-body">
        <div className="transport-mode-options" role="radiogroup" aria-label="传递方式">
          {modeOptions.map(option => (
            <button
              key={option.value}
              type="button"
              role="radio"
              aria-checked={mode === option.value}
              disabled={option.disabled}
              className={`transport-mode-option${mode === option.value ? ' is-active' : ''}`}
              onClick={() => setMode(option.value)}
            >
              <span className="transport-mode-head">{option.icon}<strong>{option.label}</strong></span>
              <span className="transport-mode-hint">{option.hint}</span>
            </button>
          ))}
        </div>

        {needsChainMethod && (
          <label className="transport-field">
            <span>链路加密方法</span>
            <select value={chainMethod} onChange={event => setChainMethod(event.target.value)}>
              {chainMethods.map(item => <option key={item.value} value={item.value}>{item.label}</option>)}
            </select>
            <small className="muted">相同目标服务器与方法共用一个监听端口；换用另一种方法会在目标上新增一个端口。</small>
          </label>
        )}

        {mode === 'tunnel' && (
          <label className="transport-field">
            <span>隧道类型</span>
            <select value={tunnelKind} onChange={event => setTunnelKind(event.target.value as TunnelKind)}>
              <option value="ssh">SSH 隧道</option>
              <option value="wireguard">WireGuard 隧道</option>
            </select>
            <small className="muted">
              {tunnelKind === 'ssh'
                ? '目标上创建受限专用账户，密钥仅允许转发到该服务端口。'
                : '自动生成双端密钥与点对点地址，同一对服务器复用一个网络。'}
            </small>
          </label>
        )}

        {needsSSHPort && (
          <label className="transport-field">
            <span>目标 SSH 端口</span>
            <input
              type="number"
              min={1}
              max={65535}
              value={sshPort}
              placeholder="1-65535"
              onChange={event => setSSHPort(event.target.value)}
              aria-invalid={sshPortInvalid}
            />
            <small className={sshPortInvalid ? 'transport-field-error' : 'muted'}>
              {sshPortInvalid
                ? 'SSH 端口必须是 1 到 65535 的整数。'
                : target.targetSSHPort
                  ? `留空则使用目标服务器已保存的 ${target.targetSSHPort}。`
                  : '目标服务器未保存 SSH 端口，请填写本次隧道使用的端口。'}
            </small>
          </label>
        )}

        {mode === 'tunnel' && tunnelKind === 'wireguard' && (
          <label className="transport-field">
            <span>保活间隔（秒）</span>
            <input
              type="number"
              min={0}
              max={65535}
              value={keepalive}
              onChange={event => setKeepalive(event.target.value)}
              aria-invalid={keepaliveInvalid}
            />
            <small className={keepaliveInvalid ? 'transport-field-error' : 'muted'}>
              {keepaliveInvalid ? '保活间隔必须是 0 到 65535 的整数。' : '0 表示不发送保活包。调整该值不会更换隧道密钥。'}
            </small>
          </label>
        )}

        <div className="transport-preview">
          <span>这一跳</span>
          <strong>{describeSelection(target, mode, usesExistingInbound, chainMethod, chainMethods, tunnelKind, sshPortValue, target.targetSSHPort)}</strong>
        </div>
      </div>
      <footer className="dialog-actions">
        <button type="button" className="ghost" onClick={onCancel}>取消</button>
        <button type="button" onClick={() => void submit()} disabled={saving || blocked}>{saving ? '保存中...' : '确定'}</button>
      </footer>
    </MotionDialogPanel>
  )
}

function describeSelection(
  target: TransportDialogTarget,
  mode: TransportMode,
  usesExistingInbound: boolean,
  chainMethod: string,
  chainMethods: ChainMethodOption[],
  tunnelKind: TunnelKind,
  sshPort: number,
  storedSSHPort?: number,
) {
  const route = `${target.sourceLabel} → ${target.targetLabel}`
  const methodLabel = chainMethods.find(item => item.value === chainMethod)?.label || chainMethod
  if (mode === 'port_forward') return `${route} · 透明转发原始密文`
  const service = usesExistingInbound ? `复用 ${target.targetInboundLabel}` : `共享 ${methodLabel}`
  if (mode === 'tunnel') {
    if (tunnelKind === 'wireguard') return `${route} · WireGuard 隧道 · ${service}`
    const port = sshPort > 0 ? sshPort : storedSSHPort
    return `${route} · SSH 隧道${port ? ` (:${port})` : ''} · ${service}`
  }
  return `${route} · ${service}`
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
  return Number.isInteger(parsed) && parsed >= 0 ? parsed : fallback
}
