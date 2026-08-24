import React, { useEffect, useMemo, useState } from 'react'
import { Layers, Pencil, Plus, RotateCcw, Trash2 } from 'lucide-react'
import { useDialogs } from './ui/dialog-context'
import { Select } from './ui/select'
import { SettingsRow } from './settings/SettingsLayout'
import { SnellProfileCards, SnellProfileEditor, emptySnellDraft, snellDraftFromProfile, type SnellDraft, type SnellProfile } from './SnellProfilesPanel'

export interface NodePresetsPanelProps {
  data: any
  client: any
  load: (section?: string, options?: any) => Promise<void>
  notify: (message: string, tone?: 'success' | 'warning' | 'danger' | 'error' | 'info') => void
}

export type NodePreset = {
  id: number
  name: string
  protocol: string
  kind: string
  config_json: string | Record<string, any>
  default_port: number
  remark: string
  builtin: boolean
  enabled: boolean
  usage_count: number
}

type ProtocolFilter = 'all' | 'vless' | 'hy2' | 'anytls' | 'shadowsocks' | 'mieru' | 'socks' | 'snell'

type NodeDraft = {
  name: string
  kind: string
  config: Record<string, any>
  default_port: number
  remark: string
  enabled: boolean
}

const protocolFilters: Array<{ id: ProtocolFilter; label: string }> = [
  { id: 'all', label: '全部' },
  { id: 'vless', label: 'VLESS' },
  { id: 'hy2', label: 'HY2' },
  { id: 'anytls', label: 'AnyTLS' },
  { id: 'shadowsocks', label: 'SS' },
  { id: 'mieru', label: 'Mieru' },
  { id: 'socks', label: 'SOCKS5' },
  { id: 'snell', label: 'Snell' },
]

const protocolOrder = ['vless', 'hy2', 'anytls', 'shadowsocks', 'mieru', 'socks', 'snell'] as const

const protocolLabels: Record<string, string> = {
  vless: 'VLESS',
  hy2: 'Hysteria2',
  anytls: 'AnyTLS',
  shadowsocks: 'Shadowsocks',
  mieru: 'Mieru',
  socks: 'SOCKS5',
  snell: 'Snell',
}

const builtinRealityDomains = ['gateway.icloud.com', 'cdn.icloud-content.com', 'www.tesla.com', 'www.nvidia.com', 'www.sony.com', 'www.mozilla.org'] as const

const anyTLSBalancedPaddingScheme = ['stop=8', '0=64-128', '1=200-450', '2=450-650,c,700-1100,c,700-1100', '3=32-96,600-900', '4=450-850', '5=500-900', '6=550-950', '7=600-1000'] as const
const anyTLSLargePaddingScheme = ['stop=3', '0=900-1400', '1=900-1400', '2=900-1400'] as const

export const nodePresetKinds = [
  { id: 'vless-reality', protocol: 'vless', label: 'VLESS Reality Vision', description: 'TCP + Reality + Vision，内置握手域名模板，默认 gateway.icloud.com', defaultPort: 443, config: { flow: 'xtls-rprx-vision', reality_domains: [...builtinRealityDomains], tls: { enabled: true, server_name: 'gateway.icloud.com', reality: { enabled: true, handshake: { server: 'gateway.icloud.com', server_port: 443 } } } } },
  { id: 'vless-tls-vision', protocol: 'vless', label: 'VLESS TLS Vision', description: 'TCP + TLS + Vision，需要证书', defaultPort: 443, config: { flow: 'xtls-rprx-vision', tls: { enabled: true } } },
  { id: 'vless-ws', protocol: 'vless', label: 'VLESS WebSocket', description: 'WebSocket + TLS，需要证书', defaultPort: 443, config: { tls: { enabled: true }, transport: { type: 'ws', path: '/vless', headers: {} } } },
  { id: 'vless-tcp', protocol: 'vless', label: 'VLESS TCP', description: '无 TLS，适合内网或测试', defaultPort: 443, config: {} },
  { id: 'hy2-tls', protocol: 'hy2', label: 'Hysteria2', description: 'HY2 标准配置，需要证书', defaultPort: 443, config: { tls: { enabled: true }, up_mbps: 100, down_mbps: 100 } },
  { id: 'anytls-basic', protocol: 'anytls', label: 'AnyTLS 均衡填充', description: 'OBoard 均衡填充，兼顾额外开销与包长变化，需要证书', defaultPort: 443, config: { tls: { enabled: true }, padding_scheme: [...anyTLSBalancedPaddingScheme] } },
  { id: 'anytls-large-padding', protocol: 'anytls', label: 'AnyTLS 大包填充', description: '前三次写入使用 900-1400 字节填充，需要证书', defaultPort: 443, config: { tls: { enabled: true }, padding_scheme: [...anyTLSLargePaddingScheme] } },
  { id: 'ss-aes-128-gcm', protocol: 'shadowsocks', label: 'SS 128', description: 'AES-128-GCM，单用户', defaultPort: 8388, config: { method: 'aes-128-gcm' } },
  { id: 'ss-aes-256-gcm', protocol: 'shadowsocks', label: 'SS 256', description: 'AES-256-GCM，单用户', defaultPort: 8388, config: { method: 'aes-256-gcm' } },
  { id: 'ss-2022-128', protocol: 'shadowsocks', label: 'SS 2022-128', description: 'AES-128-GCM，多用户', defaultPort: 8388, config: { method: '2022-blake3-aes-128-gcm' } },
  { id: 'ss-2022-256', protocol: 'shadowsocks', label: 'SS 2022-256', description: 'AES-256-GCM，多用户', defaultPort: 8388, config: { method: '2022-blake3-aes-256-gcm' } },
  { id: 'mieru-basic', protocol: 'mieru', label: 'Mieru', description: 'Mieru 多用户入口', defaultPort: 25250, config: { transport: 'TCP', multiplexing: 'MULTIPLEXING_DEFAULT', user_hint_is_mandatory: true } },
  { id: 'socks5-auth', protocol: 'socks', label: 'SOCKS5', description: '用户名密码认证，支持 TCP 与 UDP', defaultPort: 1080, config: { version: '5' } },
] as const

const shadowsocksMethods = [
  { value: 'aes-128-gcm', label: 'SS 128' },
  { value: 'aes-256-gcm', label: 'SS 256' },
  { value: '2022-blake3-aes-128-gcm', label: 'SS 2022-128' },
  { value: '2022-blake3-aes-256-gcm', label: 'SS 2022-256' },
]

function parsePresetConfig(raw: NodePreset['config_json']): Record<string, any> {
  if (raw && typeof raw === 'object' && !Array.isArray(raw)) return raw
  try {
    const parsed = JSON.parse(typeof raw === 'string' ? raw || '{}' : '{}')
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {}
  } catch {
    return {}
  }
}

function kindMeta(kind: string) {
  return nodePresetKinds.find(item => item.id === kind)
}

function anyTLSPaddingText(value: unknown) {
  if (Array.isArray(value)) return value.filter(item => typeof item === 'string').join('\n')
  return typeof value === 'string' ? value : ''
}

function emptyNodeDraft(kind = 'vless-reality'): NodeDraft {
  const meta = kindMeta(kind) || nodePresetKinds[0]
  return { name: '', kind: meta.id, config: structuredClone(meta.config) as Record<string, any>, default_port: meta.defaultPort, remark: '', enabled: true }
}

function draftFromPreset(preset: NodePreset): NodeDraft {
  const meta = kindMeta(preset.kind)
  return {
    name: preset.name,
    kind: preset.kind,
    config: { ...(meta ? structuredClone(meta.config) as Record<string, any> : {}), ...parsePresetConfig(preset.config_json) },
    default_port: preset.default_port || meta?.defaultPort || 443,
    remark: preset.remark || '',
    enabled: preset.enabled !== false,
  }
}

function objectConfig(value: any): Record<string, any> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value : {}
}

function presetMetaLine(preset: NodePreset) {
  const meta = kindMeta(preset.kind)
  const parts = [meta?.label || preset.kind, `端口 ${preset.default_port || meta?.defaultPort || 443}`]
  parts.push(preset.usage_count > 0 ? `${preset.usage_count} 个入口` : '未使用')
  return parts.join(' · ')
}

function NodePresetCards({ presets, editingID, onEdit, onDelete }: { presets: NodePreset[]; editingID?: number; onEdit: (preset: NodePreset) => void; onDelete: (preset: NodePreset) => void }) {
  return <div className="snell-profile-grid">
    {presets.map(preset => (
      <article className={`snell-profile-card${preset.builtin ? ' is-builtin' : ''}${preset.enabled === false ? ' is-disabled' : ''}${editingID === preset.id ? ' is-editing' : ''}`} key={preset.id}>
        <div className="snell-profile-main">
          <div className="snell-profile-identity">
            <strong className="snell-profile-name">{preset.name}</strong>
            {preset.builtin && <span className="badge neutral">内置</span>}
            {preset.enabled === false && <span className="badge neutral">已停用</span>}
          </div>
          <p className="snell-profile-meta">{presetMetaLine(preset)}</p>
          {preset.remark && <p className="snell-profile-remark">{preset.remark}</p>}
        </div>
        <div className="snell-profile-card-actions">
          <button type="button" className="ghost icon-button" onClick={() => onEdit(preset)} title={`编辑 ${preset.name}`} aria-label={`编辑 ${preset.name}`}><Pencil size={14} /></button>
          {!preset.builtin && <button type="button" className="ghost icon-button danger-text" onClick={() => onDelete(preset)} disabled={preset.usage_count > 0} title={preset.usage_count > 0 ? '仍有入口引用，请先解绑' : `删除 ${preset.name}`} aria-label={`删除 ${preset.name}`}><Trash2 size={14} /></button>}
        </div>
      </article>
    ))}
  </div>
}

function NodePresetEditor({ title, draft, setDraft, lockKind, onSave, onCancel, saving }: { title: string; draft: NodeDraft; setDraft: (draft: NodeDraft) => void; lockKind?: boolean; onSave: () => void; onCancel: () => void; saving: boolean }) {
  const meta = kindMeta(draft.kind) || nodePresetKinds[0]
  const tls = objectConfig(draft.config.tls)
  const transport = objectConfig(draft.config.transport)
  const headers = objectConfig(transport.headers)
  const reality = objectConfig(tls.reality)
  const handshake = objectConfig(reality.handshake)
  const changeKind = (kind: string) => {
    const next = kindMeta(kind) || nodePresetKinds[0]
    setDraft({ ...draft, kind: next.id, config: structuredClone(next.config) as Record<string, any>, default_port: next.defaultPort })
  }
  const updateConfig = (patch: Record<string, any>) => setDraft({ ...draft, config: { ...draft.config, ...patch } })
  const setTLS = (patch: Record<string, any>) => updateConfig({ tls: { ...tls, ...patch } })
  const setTransport = (patch: Record<string, any>) => updateConfig({ transport: { ...transport, ...patch } })
  const realityDomains: string[] = Array.isArray(draft.config.reality_domains) ? (draft.config.reality_domains as string[]).filter(item => typeof item === 'string' && item.trim()) : [...builtinRealityDomains]
  const selectedRealityDomain = String(tls.server_name || handshake.server || realityDomains[0] || builtinRealityDomains[0])
  const isCustomRealityDomain = selectedRealityDomain.trim() !== '' && !realityDomains.includes(selectedRealityDomain)
  const [realityCustomForced, setRealityCustomForced] = useState(false)
  useEffect(() => {
    if (isCustomRealityDomain) setRealityCustomForced(false)
  }, [selectedRealityDomain, isCustomRealityDomain])
  const showCustomReality = isCustomRealityDomain || realityCustomForced
  const setRealityDomain = (value: string) => {
    const trimmed = String(value || '').trim()
    const serverName = trimmed || builtinRealityDomains[0]
    if (realityDomains.includes(trimmed)) setRealityCustomForced(false)
    setTLS({ server_name: serverName, reality: { ...reality, enabled: true, handshake: { ...handshake, server: serverName, server_port: Number(handshake.server_port || 443) } } })
  }
  const setRealityDomains = (value: string) => {
    const domains = value.split(/[\n,]+/).map(item => item.trim()).filter(Boolean)
    const normalized = domains.length ? Array.from(new Set(domains)) : [...builtinRealityDomains]
    const current = String(tls.server_name || handshake.server || normalized[0] || builtinRealityDomains[0])
    const nextSelected = normalized.includes(current) ? current : normalized[0]
    if (!normalized.includes(nextSelected) && nextSelected.trim()) setRealityCustomForced(true)
    else if (normalized.includes(String(tls.server_name || handshake.server || ''))) setRealityCustomForced(false)
    updateConfig({ reality_domains: normalized, tls: { ...tls, server_name: nextSelected, reality: { ...reality, enabled: true, handshake: { ...handshake, server: nextSelected, server_port: Number(handshake.server_port || 443) } } } })
  }
  return <div className="snell-profile-editor">
    <h4>{title}</h4>
    <div className="form settings-form">
      <SettingsRow label="预设名称" description="用于在创建入口时识别这套默认配置。">
        <input value={draft.name} onChange={event => setDraft({ ...draft, name: event.target.value })} placeholder="例如 机房 Reality" />
      </SettingsRow>
      <SettingsRow label="配置类型" description={meta.description}>
        <Select value={draft.kind} onChange={event => changeKind(event.target.value)} disabled={lockKind} aria-label="配置类型">
          {nodePresetKinds.map(item => <option key={item.id} value={item.id}>{item.label}</option>)}
        </Select>
      </SettingsRow>
      <SettingsRow label="默认端口" description="新建入口时优先使用这个端口。">
        <input value={draft.default_port} onChange={event => setDraft({ ...draft, default_port: Number(event.target.value) || 0 })} inputMode="numeric" />
      </SettingsRow>
      {draft.kind === 'vless-reality' && <>
        <SettingsRow label="握手域名模板" description="预设内置 6 个候选域名，第一个为默认；创建入口时可直接选择或输入自定义域名。">
          <Select value={showCustomReality ? '__custom__' : selectedRealityDomain} onChange={event => {
            const value = event.target.value
            if (value === '__custom__') {
              setRealityCustomForced(true)
              return
            }
            setRealityCustomForced(false)
            setRealityDomain(value)
          }} aria-label="握手域名模板">
            {realityDomains.map(domain => <option key={domain} value={domain}>{domain}{domain === realityDomains[0] ? '（默认）' : ''}</option>)}
            <option value="__custom__">{isCustomRealityDomain ? `${selectedRealityDomain}（自定义）` : '自定义…'}</option>
          </Select>
        </SettingsRow>
        {showCustomReality && <SettingsRow label="自定义 SNI / 握手域名" description="输入任意域名（例如 www.example.com），不在模板列表中也将作为当前选中值保存。">
          <input value={selectedRealityDomain} onChange={event => {
            const next = event.target.value
            const trimmed = next.trim()
            if (!trimmed) setRealityCustomForced(false)
            else if (realityDomains.includes(trimmed)) setRealityCustomForced(false)
            else setRealityCustomForced(true)
            setRealityDomain(next)
          }} placeholder="gateway.icloud.com" autoCapitalize="none" spellCheck={false} />
        </SettingsRow>}
        <SettingsRow label="模板域名列表" description="逗号或换行分隔，第一个为默认。留空恢复系统默认模板。">
          <input value={realityDomains.join(', ')} onChange={event => setRealityDomains(event.target.value)} placeholder={builtinRealityDomains.join(', ')} />
        </SettingsRow>
        <SettingsRow label="握手端口" description="Reality 回源握手端口，通常为 443。">
          <input value={Number(handshake.server_port || 443)} onChange={event => setTLS({ reality: { ...reality, enabled: true, handshake: { ...handshake, server: handshake.server || tls.server_name || selectedRealityDomain, server_port: Number(event.target.value) } } })} inputMode="numeric" />
        </SettingsRow>
      </>}
      {draft.kind === 'vless-ws' && <>
        <SettingsRow label="WebSocket 路径" description="客户端连接使用的路径。">
          <input value={String(transport.path || '')} onChange={event => setTransport({ path: event.target.value, type: 'ws', headers })} placeholder="/vless" />
        </SettingsRow>
        <SettingsRow label="Host 头" description="可选伪装 Host。">
          <input value={String(headers.Host || '')} onChange={event => setTransport({ ...transport, type: 'ws', headers: { ...headers, Host: event.target.value } })} placeholder="example.com" />
        </SettingsRow>
      </>}
      {draft.kind === 'hy2-tls' && <>
        <SettingsRow label="上传带宽 Mbps" description="HY2 协商带宽上限。">
          <input value={Number(draft.config.up_mbps || 100)} onChange={event => updateConfig({ up_mbps: Number(event.target.value) })} inputMode="numeric" />
        </SettingsRow>
        <SettingsRow label="下载带宽 Mbps" description="HY2 协商带宽上限。">
          <input value={Number(draft.config.down_mbps || 100)} onChange={event => updateConfig({ down_mbps: Number(event.target.value) })} inputMode="numeric" />
        </SettingsRow>
      </>}
      {draft.kind.startsWith('anytls-') && <SettingsRow label="Padding 填充方案" description="每行一条规则：stop=N、序号=min-max；c 用于在数据耗尽时停止后续填充。">
        <textarea rows={6} value={anyTLSPaddingText(draft.config.padding_scheme)} onChange={event => updateConfig({ padding_scheme: event.target.value.replace(/\r\n/g, '\n').split('\n') })} spellCheck={false} />
      </SettingsRow>}
      {draft.kind.startsWith('ss-') && <SettingsRow label="加密方法" description="创建入口时会套用此方法；密钥仍由入口单独生成。">
        <Select value={String(draft.config.method || (meta.config as Record<string, any>).method || '')} onChange={event => updateConfig({ method: event.target.value })} aria-label="加密方法">
          {shadowsocksMethods.map(item => <option key={item.value} value={item.value}>{item.label}</option>)}
        </Select>
      </SettingsRow>}
      {draft.kind === 'mieru-basic' && <>
        <SettingsRow label="传输" description="Mieru 传输方式。">
          <Select value={String(draft.config.transport || 'TCP')} onChange={event => updateConfig({ transport: event.target.value })} aria-label="Mieru 传输">
            <option value="TCP">TCP</option>
            <option value="UDP">UDP</option>
          </Select>
        </SettingsRow>
        <SettingsRow label="多路复用" description="默认即可，按线路质量再调整。">
          <Select value={String(draft.config.multiplexing || 'MULTIPLEXING_DEFAULT')} onChange={event => updateConfig({ multiplexing: event.target.value })} aria-label="Mieru 多路复用">
            <option value="MULTIPLEXING_DEFAULT">默认</option>
            <option value="MULTIPLEXING_OFF">关闭</option>
            <option value="MULTIPLEXING_LOW">低</option>
            <option value="MULTIPLEXING_MIDDLE">中</option>
            <option value="MULTIPLEXING_HIGH">高</option>
          </Select>
        </SettingsRow>
      </>}
      <SettingsRow label="备注" description="可选说明，例如适用机房或用途。">
        <input value={draft.remark} onChange={event => setDraft({ ...draft, remark: event.target.value })} placeholder="可选" />
      </SettingsRow>
      <div className="settings-actions">
        <button onClick={onSave} disabled={saving || !draft.name.trim()}>{saving ? '保存中...' : '保存预设'}</button>
        <button type="button" className="ghost" onClick={onCancel}>取消</button>
      </div>
    </div>
  </div>
}

export function NodePresetsPanel({ data, client, load, notify }: NodePresetsPanelProps) {
  const dialogs = useDialogs()
  const presets: NodePreset[] = data.node_presets || []
  const snellProfiles: SnellProfile[] = data.snell_profiles || []
  const [filter, setFilter] = useState<ProtocolFilter>('all')
  const [editingNode, setEditingNode] = useState<null | { id?: number; builtin?: boolean; draft: NodeDraft }>(null)
  const [editingSnell, setEditingSnell] = useState<null | { id?: number; draft: SnellDraft }>(null)
  const [saving, setSaving] = useState(false)

  const groupedPresets = useMemo(() => {
    const visible = filter === 'all' ? presets : presets.filter(item => item.protocol === filter)
    const groups = new Map<string, NodePreset[]>()
    visible.forEach(preset => {
      const key = preset.protocol
      groups.set(key, [...(groups.get(key) || []), preset])
    })
    return protocolOrder.filter(protocol => protocol !== 'snell' && (filter === 'all' || filter === protocol)).map(protocol => ({
      protocol,
      label: protocolLabels[protocol],
      items: groups.get(protocol) || [],
    })).filter(group => filter !== 'all' || group.items.length > 0)
  }, [filter, presets])

  const showSnell = filter === 'all' || filter === 'snell'
  const totalCount = presets.length + snellProfiles.length
  const builtinCount = presets.filter(item => item.builtin).length + snellProfiles.filter(item => item.builtin).length

  const saveNode = async () => {
    if (!editingNode) return
    setSaving(true)
    try {
      const meta = kindMeta(editingNode.draft.kind) || nodePresetKinds[0]
      const body = {
        name: editingNode.draft.name.trim(),
        protocol: meta.protocol,
        kind: editingNode.draft.kind,
        config_json: JSON.stringify(editingNode.draft.config || {}),
        default_port: editingNode.draft.default_port,
        remark: editingNode.draft.remark,
        enabled: editingNode.draft.enabled,
      }
      if (editingNode.id) {
        await client.request(`/node-presets/${editingNode.id}`, { method: 'PUT', body: JSON.stringify(body) })
        notify('预设已更新，新入口会使用这套默认配置', 'success')
      } else {
        await client.request('/node-presets', { method: 'POST', body: JSON.stringify(body) })
        notify('预设已创建，可在入口表单中套用', 'success')
      }
      setEditingNode(null)
      await load()
    } catch (error: any) {
      notify(String(error?.message || error || '保存失败'), 'error')
    } finally {
      setSaving(false)
    }
  }

  const saveSnell = async () => {
    if (!editingSnell) return
    setSaving(true)
    try {
      if (editingSnell.id) {
        await client.request(`/snell-profiles/${editingSnell.id}`, { method: 'PUT', body: JSON.stringify(editingSnell.draft) })
        notify('预设已更新，引用入口将在下次部署时生效', 'success')
      } else {
        await client.request('/snell-profiles', { method: 'POST', body: JSON.stringify(editingSnell.draft) })
        notify('预设已创建，可在入口表单中套用', 'success')
      }
      setEditingSnell(null)
      await load()
    } catch (error: any) {
      notify(String(error?.message || error || '保存失败'), 'error')
    } finally {
      setSaving(false)
    }
  }

  const deleteNode = async (preset: NodePreset) => {
    if (!await dialogs.confirm({ title: `删除预设「${preset.name}」？`, message: '删除后无法恢复。', confirmText: '删除', tone: 'danger' })) return
    try {
      await client.request(`/node-presets/${preset.id}`, { method: 'DELETE' })
      notify('预设已删除', 'success')
      await load()
    } catch (error: any) {
      notify(String(error?.message || error || '删除失败'), 'error')
    }
  }

  const deleteSnell = async (profile: SnellProfile) => {
    if (!await dialogs.confirm({ title: `删除预设「${profile.name}」？`, message: '删除后无法恢复。', confirmText: '删除', tone: 'danger' })) return
    try {
      await client.request(`/snell-profiles/${profile.id}`, { method: 'DELETE' })
      notify('预设已删除', 'success')
      await load()
    } catch (error: any) {
      notify(String(error?.message || error || '删除失败'), 'error')
    }
  }

  const startCreate = () => {
    if (filter === 'snell') {
      setEditingNode(null)
      setEditingSnell({ draft: emptySnellDraft(4) })
      return
    }
    const firstKind = nodePresetKinds.find(item => filter === 'all' || item.protocol === filter) || nodePresetKinds[0]
    setEditingSnell(null)
    setEditingNode({ draft: emptyNodeDraft(firstKind.id) })
  }

  const restoreSystem = async () => {
    if (!await dialogs.confirm({
      title: '恢复全部系统模板？',
      message: '此操作会覆盖内置模板的自定义修改，但不会影响自定义预设与入口引用。',
      confirmText: '恢复模板',
      tone: 'danger',
    })) return
    setSaving(true)
    try {
      await client.request('/node-presets/restore-system', { method: 'POST', body: '{}' })
      notify('已恢复系统模板', 'success')
      await load()
    } catch (error: any) {
      notify(String(error?.message || error || '恢复失败'), 'error')
    } finally {
      setSaving(false)
    }
  }

  return <section id="settings-panel-presets" role="tabpanel" className="settings-card">
    <div className="settings-group">
      <div className="settings-group-body" style={{ paddingTop: 18 }}>
        <div className="node-presets-toolbar">
        <div className="node-preset-filters" role="tablist" aria-label="按协议筛选">
          {protocolFilters.map(item => (
            <button key={item.id} type="button" className={filter === item.id ? 'active' : ''} role="tab" aria-selected={filter === item.id} onClick={() => setFilter(item.id)}>{item.label}</button>
          ))}
        </div>
        <div className="node-presets-toolbar-actions">
          <span className="muted">共 {totalCount} 套，内置 {builtinCount} 套</span>
          <button type="button" className="ghost" onClick={restoreSystem} disabled={saving} title="将全部内置节点预设恢复为系统模板"><RotateCcw size={14} />恢复系统模板</button>
          <button type="button" onClick={startCreate}><Plus size={14} />新建预设</button>
        </div>
      </div>
      {editingNode && <NodePresetEditor title={editingNode.id ? '编辑节点预设' : '新建节点预设'} draft={editingNode.draft} setDraft={draft => setEditingNode({ id: editingNode.id, builtin: editingNode.builtin, draft })} lockKind={Boolean(editingNode.builtin)} onSave={saveNode} onCancel={() => setEditingNode(null)} saving={saving} />}
      {editingSnell && <SnellProfileEditor title={editingSnell.id ? '编辑 Snell 预设' : '新建 Snell 预设'} draft={editingSnell.draft} setDraft={draft => setEditingSnell({ id: editingSnell.id, draft })} onSave={saveSnell} onCancel={() => setEditingSnell(null)} saving={saving} />}
      {groupedPresets.map(group => (
        <div className="node-preset-group" key={group.protocol}>
          <div className="node-preset-group-head">
            <h4>{group.label}</h4>
            <span className="muted">{group.items.length} 套</span>
          </div>
          {group.items.length ? <NodePresetCards presets={group.items} editingID={editingNode?.id} onEdit={preset => { setEditingSnell(null); setEditingNode({ id: preset.id, builtin: preset.builtin, draft: draftFromPreset(preset) }) }} onDelete={deleteNode} /> : <div className="snell-profiles-empty"><Layers size={18} /><span>该协议还没有预设</span></div>}
        </div>
      ))}
      {showSnell && <div className="node-preset-group">
        <div className="node-preset-group-head">
          <h4>Snell</h4>
          <span className="muted">{snellProfiles.length} 套</span>
        </div>
        {snellProfiles.length ? <SnellProfileCards profiles={snellProfiles} editingID={editingSnell?.id} onEdit={profile => { setEditingNode(null); setEditingSnell({ id: profile.id, draft: snellDraftFromProfile(profile) }) }} onDelete={deleteSnell} /> : <div className="snell-profiles-empty"><Layers size={18} /><span>还没有 Snell 参数预设</span></div>}
      </div>}
      </div>
    </div>
  </section>
}
