import { useEffect, useId, useState } from 'react'
import { FormField } from '../ui/form-field'
import { SettingsGroup, SettingsSwitchRow } from './SettingsLayout'

export type StealthTransportConfig = {
  enabled: boolean
  listen_address: string
  public_address: string
}
type Status = Partial<StealthTransportConfig> & { active?: boolean; error?: string; source?: string }

export function StealthTransportSettings({ value, onSave }: {
  value?: Status
  onSave: (config: StealthTransportConfig) => Promise<void>
}) {
  const enabled = Boolean(value?.enabled)
  const listen = value?.listen_address || '0.0.0.0:24443'
  const address = value?.public_address || ''
  const [draft, setDraft] = useState<StealthTransportConfig>({ enabled, listen_address: listen, public_address: address })
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const id = useId()
  useEffect(() => {
    setDraft({ enabled, listen_address: listen, public_address: address })
  }, [enabled, listen, address])
  const update = (changes: Partial<StealthTransportConfig>) => {
    setDraft(current => ({ ...current, ...changes }))
    setError('')
    setSaved(false)
  }
  const save = async (event: React.FormEvent) => {
    event.preventDefault()
    if (saving) return
    setSaving(true)
    setError('')
    setSaved(false)
    try {
      await onSave({ ...draft, listen_address: draft.listen_address.trim(), public_address: draft.public_address.trim() })
      setSaved(true)
    } catch (error: any) {
      setError(error?.message || String(error))
    } finally { setSaving(false) }
  }
  return <SettingsGroup title="安全进程传输" description="安全进程 Agent 使用独立 TCP 端口连接主控。"
    actions={<span className={`status-pill ${value?.active ? 'ok' : value?.enabled ? 'warning' : ''}`}>{value?.active ? '监听中' : value?.enabled ? '未生效' : '未启用'}</span>}>
    <form className="form settings-form single-field" onSubmit={save} aria-busy={saving}>
      <SettingsSwitchRow label="启用安全进程传输" checked={draft.enabled} ariaLabel="启用安全进程传输" onChange={enabled => update({ enabled })} disabled={saving} />
      <FormField label="监听地址" required>
        <input aria-label="安全进程监听地址" aria-describedby={`${id}-listen ${id}-error`} aria-invalid={Boolean(error)}
          value={draft.listen_address} onChange={event => update({ listen_address: event.target.value })} disabled={saving} required placeholder="0.0.0.0:24443" />
      </FormField>
      <small id={`${id}-listen`} className="muted">主控本机 IP:端口。0.0.0.0 监听全部 IPv4 网卡；[::] 监听 IPv6。端口范围 1–65535。</small>
      <FormField label="Agent 连接地址" required={draft.enabled}>
        <input aria-label="安全进程 Agent 连接地址" aria-describedby={`${id}-public ${id}-error`} aria-invalid={Boolean(error)}
          value={draft.public_address} onChange={event => update({ public_address: event.target.value })} disabled={saving} required={draft.enabled} placeholder="agent.example.com:24443" />
      </FormField>
      <small id={`${id}-public`} className="muted">Agent 可访问的域名或 IP:端口，不含 https:// 或面板路径。IPv6 使用 [地址]:端口。请放行对应 TCP 端口；NAT 环境需配置端口转发。</small>
      <p className="muted">保存后立即生效，证书指纹自动写入接入命令。已有服务器使用安全进程时，须先关闭其安全进程并确认恢复普通连接，才能改址或关闭此端口。</p>
      {value?.source === 'environment' && value.enabled && <p className="muted">当前配置来自主控环境变量；保存后以面板配置为准。</p>}
      {(error || value?.error) && <p id={`${id}-error`} role="alert">{error || value?.error}</p>}
      {saved && <p role="status">安全进程传输设置已保存并生效。</p>}
      <div className="settings-actions"><button type="submit" disabled={saving}>{saving ? '保存中...' : '保存安全传输设置'}</button></div>
    </form>
  </SettingsGroup>
}
