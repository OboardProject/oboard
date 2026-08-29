import React, { useMemo, useState } from 'react'
import { GitBranch, Layers, Plus, Trash2, X } from 'lucide-react'
import { MotionDialogPanel } from '../ui/motion'
import { FormField } from '../ui/form-field'
import { Select } from '../ui/select'
import { useDialogs } from '../ui/dialog-context'
import { NetworkInterfacePicker } from '../NetworkInterfacePicker'
import { TransportDialog, type TransportMode, type TransportSelection, type ChainMethodOption } from './TransportDialog'
import type { ExternalOutbound, FamilySplitTemplate, ProxyPath, ProxyPathStep, Server } from './types'

type EditorClient = {
  request: (path: string, init?: RequestInit) => Promise<any>
}

type Props = {
  data: any
  client: EditorClient
  load: () => Promise<void>
  chainMethods: ChainMethodOption[]
  initialTemplateID?: number
  onClose: () => void
  onSelect?: (templateID: number) => void
}

type HopKind = 'server' | 'warp' | 'imported'
type BindMode = 'none' | 'interface' | 'source_prefix'

function parseConfig(raw?: string): Record<string, any> {
  if (!raw) return {}
  try {
    const parsed = JSON.parse(raw)
    return parsed && typeof parsed === 'object' ? parsed : {}
  } catch {
    return {}
  }
}

function mergeBindConfig(raw: string | undefined, bind: BindMode, interfaceName: string, sourcePrefix: string) {
  const cfg = parseConfig(raw)
  delete cfg.interface_name
  delete cfg.source_prefix
  if (bind === 'interface' && interfaceName.trim()) cfg.interface_name = interfaceName.trim()
  if (bind === 'source_prefix' && sourcePrefix.trim()) cfg.source_prefix = sourcePrefix.trim()
  return JSON.stringify(cfg)
}

function hopLabel(step: ProxyPathStep, servers: Server[], imported: ExternalOutbound[]) {
  if (step.node_type === 'warp') return 'WARP'
  if (step.node_type === 'imported') {
    const node = imported.find(item => item.id === Number(step.external_outbound_id || 0))
    return node?.name || `导入节点 ${step.external_outbound_id}`
  }
  const inboundServer = servers.find(server => server.id === Number(step.server_id || 0))
  return inboundServer?.name || `服务器 ${step.server_id || '?'}`
}

function transportLabel(mode?: string) {
  if (mode === 'tunnel') return '隧道'
  if (mode === 'port_forward') return '透明转发'
  return 'sing-box'
}

export function FamilySplitTemplateEditor({ data, client, load, chainMethods, initialTemplateID = 0, onClose, onSelect }: Props) {
  const dialogs = useDialogs()
  const templates: FamilySplitTemplate[] = data.family_split_templates || []
  const paths: ProxyPath[] = data.proxy_paths || []
  const steps: ProxyPathStep[] = data.proxy_path_steps || []
  const servers: Server[] = (data.servers || []).filter((item: Server) => item.status !== 'deleted')
  const imported: ExternalOutbound[] = (data.external_outbounds || []).filter((item: ExternalOutbound) => item.enabled !== false)
  const [selectedID, setSelectedID] = useState(initialTemplateID || templates[0]?.id || 0)
  const [nameDraft, setNameDraft] = useState('')
  const [creating, setCreating] = useState(!templates.length)
  const [hopFamily, setHopFamily] = useState<'ipv4' | 'ipv6'>('ipv4')
  const [hopKind, setHopKind] = useState<HopKind>('server')
  const [hopServerID, setHopServerID] = useState(0)
  const [hopImportedID, setHopImportedID] = useState(0)
  const [transport, setTransport] = useState<{ family: 'ipv4' | 'ipv6'; target: { sourceLabel: string; targetLabel: string; targetServerID?: number; importedOnly?: boolean } } | null>(null)
  const [saving, setSaving] = useState(false)

  const template = templates.find(item => item.id === selectedID)
  const ipv4Path = paths.find(path => path.id === template?.ipv4_path_id)
  const ipv6Path = paths.find(path => path.id === template?.ipv6_path_id)
  const hopsFor = (pathID?: number) => steps.filter(step => step.path_id === pathID).sort((a, b) => a.position - b.position || a.id - b.id)
  const ipv4Hops = hopsFor(template?.ipv4_path_id)
  const ipv6Hops = hopsFor(template?.ipv6_path_id)

  const lastHop = (family: 'ipv4' | 'ipv6') => {
    const hops = family === 'ipv4' ? ipv4Hops : ipv6Hops
    return hops[hops.length - 1]
  }
  const bindServerID = (family: 'ipv4' | 'ipv6') => {
    const list = family === 'ipv4' ? ipv4Hops : ipv6Hops
    for (let index = list.length - 1; index >= 0; index--) {
      if (list[index].node_type === 'server_inbound' && Number(list[index].server_id || 0) > 0) return Number(list[index].server_id)
    }
    return 0
  }
  const bindOf = (family: 'ipv4' | 'ipv6'): { mode: BindMode; interface_name: string; source_prefix: string } => {
    const hop = lastHop(family)
    const cfg = parseConfig(hop?.config_json)
    if (String(cfg.interface_name || '').trim()) return { mode: 'interface', interface_name: String(cfg.interface_name), source_prefix: '' }
    if (String(cfg.source_prefix || '').trim()) return { mode: 'source_prefix', interface_name: '', source_prefix: String(cfg.source_prefix) }
    return { mode: 'none', interface_name: '', source_prefix: '' }
  }

  const alertError = async (title: string, error: any) => {
    await dialogs.alert({ title, message: String(error?.message || error) })
  }

  const createTemplate = async () => {
    const name = nameDraft.trim()
    if (!name) return
    setSaving(true)
    try {
      const result = await client.request('/family-split-templates', { method: 'POST', body: JSON.stringify({ name }) }) as { family_split_template?: FamilySplitTemplate }
      await load()
      const id = Number(result.family_split_template?.id || 0)
      if (id) {
        setSelectedID(id)
        setCreating(false)
        setNameDraft('')
      }
    } catch (error: any) {
      await alertError('创建双栈模板失败', error)
    } finally {
      setSaving(false)
    }
  }

  const renameTemplate = async () => {
    if (!template) return
    const name = nameDraft.trim() || template.name
    setSaving(true)
    try {
      await client.request(`/family-split-templates/${template.id}`, { method: 'PATCH', body: JSON.stringify({ name }) })
      await load()
      setNameDraft('')
    } catch (error: any) {
      await alertError('重命名失败', error)
    } finally {
      setSaving(false)
    }
  }

  const deleteTemplate = async () => {
    if (!template) return
    const ok = await dialogs.confirm({ title: '删除双栈模板', message: `删除「${template.name}」及其 IPv4 / IPv6 分支？被分流规则引用时无法删除。`, confirmText: '删除', tone: 'danger' })
    if (!ok) return
    setSaving(true)
    try {
      await client.request(`/family-split-templates/${template.id}`, { method: 'DELETE' })
      await load()
      setSelectedID(0)
      setCreating(true)
    } catch (error: any) {
      await alertError('删除失败', error)
    } finally {
      setSaving(false)
    }
  }

  const beginAddHop = (family: 'ipv4' | 'ipv6') => {
    const hops = family === 'ipv4' ? ipv4Hops : ipv6Hops
    if (hops[hops.length - 1]?.node_type === 'warp') return void dialogs.alert({ title: '无法继续添加', message: 'WARP 必须是该分支的最后一个节点。' })
    setHopFamily(family)
    if (hopKind === 'server') {
      const server = servers.find(item => item.id === hopServerID) || servers[0]
      if (!server) return void dialogs.alert({ title: '无法添加节点', message: '请先添加一台受控服务器。' })
      setHopServerID(server.id)
      setTransport({
        family,
        target: {
          sourceLabel: '流量入口',
          targetLabel: server.name,
          targetServerID: server.id,
        },
      })
      return
    }
    void submitHop(family, hopKind === 'warp'
      ? { node_type: 'warp', transport_mode: 'singbox', config_json: '{}' }
      : { node_type: 'imported', external_outbound_id: hopImportedID, transport_mode: 'singbox', config_json: '{}', importedOnly: true })
  }

  const submitHop = async (family: 'ipv4' | 'ipv6', payload: Record<string, any>, selection?: TransportSelection) => {
    const path = family === 'ipv4' ? ipv4Path : ipv6Path
    if (!path) return
    const hops = family === 'ipv4' ? ipv4Hops : ipv6Hops
    if (payload.node_type === 'imported' && !Number(payload.external_outbound_id || hopImportedID)) {
      await dialogs.alert({ title: '无法添加节点', message: '请选择导入节点。' })
      return
    }
    setSaving(true)
    try {
      await client.request('/proxy-path-steps', {
        method: 'POST',
        body: JSON.stringify({
          path_id: path.id,
          position: hops.length + 1,
          node_type: payload.node_type,
          server_id: payload.node_type === 'server_inbound' ? hopServerID : undefined,
          external_outbound_id: payload.node_type === 'imported' ? hopImportedID : undefined,
          transport_mode: selection?.transport_mode || payload.transport_mode || 'singbox',
          config_json: selection?.config_json || payload.config_json || '{}',
        }),
      })
      await load()
    } catch (error: any) {
      await alertError('添加节点失败', error)
    } finally {
      setSaving(false)
      setTransport(null)
    }
  }

  const removeHop = async (step: ProxyPathStep) => {
    setSaving(true)
    try {
      await client.request(`/proxy-path-steps/${step.id}`, { method: 'DELETE' })
      await load()
    } catch (error: any) {
      await alertError('删除节点失败', error)
    } finally {
      setSaving(false)
    }
  }

  const saveBind = async (family: 'ipv4' | 'ipv6', mode: BindMode, interfaceName: string, sourcePrefix: string) => {
    const hop = lastHop(family)
    if (!hop) return
    setSaving(true)
    try {
      await client.request(`/proxy-path-steps/${hop.id}`, {
        method: 'PATCH',
        body: JSON.stringify({ ...hop, config_json: mergeBindConfig(hop.config_json, mode, interfaceName, sourcePrefix) }),
      })
      await load()
    } catch (error: any) {
      await alertError('保存出口绑定失败', error)
    } finally {
      setSaving(false)
    }
  }

  const allowedModes: TransportMode[] = useMemo(() => {
    const hops = hopFamily === 'ipv4' ? ipv4Hops : ipv6Hops
    return hops.length === 0 ? ['singbox'] : ['singbox', 'tunnel']
  }, [hopFamily, ipv4Hops.length, ipv6Hops.length])

  const renderBranch = (family: 'ipv4' | 'ipv6', hops: ProxyPathStep[]) => {
    const bind = bindOf(family)
    const bindServer = bindServerID(family)
    const terminal = hops[hops.length - 1]?.node_type === 'warp'
    return (
      <section className="routing-action-target-panel" style={{ minWidth: 0 }}>
        <div className="routing-config-section-title" style={{ marginBottom: 10 }}>
          <strong>{family === 'ipv4' ? 'IPv4 分支' : 'IPv6 分支'}</strong>
          <small className="muted">{hops.length ? `${hops.length} 跳` : '尚未添加节点'}</small>
        </div>
        <ol className="muted" style={{ margin: '0 0 12px', paddingLeft: 18, display: 'grid', gap: 8 }}>
          <li>抽象流量入口（接到分流规则后才确定）</li>
          {hops.map(step => (
            <li key={step.id} style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
              <span>{hopLabel(step, servers, imported)} · {transportLabel(step.transport_mode)}</span>
              <button type="button" className="ghost icon-button" onClick={() => void removeHop(step)} aria-label="删除节点" title="删除节点"><Trash2 size={13} /></button>
            </li>
          ))}
        </ol>
        {!terminal && (
          <div className="form-grid two" style={{ marginBottom: 12 }}>
            <FormField label="下一跳类型">
              <Select value={hopKind} onChange={event => { setHopKind(event.target.value as HopKind); setHopFamily(family) }}>
                <option value="server">受控服务器</option>
                <option value="warp">WARP</option>
                <option value="imported">导入节点</option>
              </Select>
            </FormField>
            {hopKind === 'server' && (
              <FormField label="服务器" required>
                <Select value={hopServerID} onChange={event => setHopServerID(Number(event.target.value))}>
                  <option value={0}>选择服务器</option>
                  {servers.map(server => <option key={server.id} value={server.id}>{server.name}</option>)}
                </Select>
              </FormField>
            )}
            {hopKind === 'imported' && (
              <FormField label="导入节点" required>
                <Select value={hopImportedID} onChange={event => setHopImportedID(Number(event.target.value))}>
                  <option value={0}>选择导入节点</option>
                  {imported.map(node => <option key={node.id} value={node.id}>{node.name}</option>)}
                </Select>
              </FormField>
            )}
          </div>
        )}
        {!terminal && <button type="button" className="ghost" onClick={() => beginAddHop(family)} disabled={saving}><Plus size={14} />添加节点</button>}
        {hops.length > 0 && bindServer > 0 && (
          <div style={{ marginTop: 14 }}>
            <FormField label="最后一跳出站绑定" hint="网卡与源前缀互斥；接到分流规则且第一跳折叠为本机后，绑定作用在剩余出口上。">
              <Select
                value={bind.mode}
                onChange={event => {
                  const mode = event.target.value as BindMode
                  void saveBind(family, mode, bind.interface_name, bind.source_prefix)
                }}
              >
                <option value="none">默认</option>
                <option value="interface">指定网卡</option>
                <option value="source_prefix">源地址前缀</option>
              </Select>
            </FormField>
            {bind.mode === 'interface' && (
              <NetworkInterfacePicker serverID={bindServer} value={bind.interface_name} onChange={value => void saveBind(family, 'interface', value, '')} client={client} />
            )}
            {bind.mode === 'source_prefix' && (
              <NetworkInterfacePicker mode="source-prefix" serverID={bindServer} value={bind.source_prefix} onChange={value => void saveBind(family, 'source_prefix', '', value)} client={client} />
            )}
          </div>
        )}
      </section>
    )
  }

  return (
    <MotionDialogPanel onCancel={onClose} className="routing-composer-dialog" aria-labelledby="family-split-template-title">
      <header className="dialog-head routing-composer-head">
        <div className="routing-composer-head-left">
          <h2 id="family-split-template-title"><Layers size={17} aria-hidden="true" />双栈模板</h2>
          <p className="muted">独立编排 IPv4 / IPv6 两条分支。接到分流规则后才生效，可在多条链路复用。</p>
        </div>
        <button type="button" className="ghost dialog-close icon-button" onClick={onClose} aria-label="关闭" title="关闭"><X size={16} /></button>
      </header>
      <div className="dialog-body" style={{ display: 'grid', gap: 16 }}>
        <div className="form-grid two">
          <FormField label="已有模板">
            <Select
              value={creating ? 0 : selectedID}
              onChange={event => {
                const id = Number(event.target.value)
                if (!id) { setCreating(true); return }
                setCreating(false)
                setSelectedID(id)
                setNameDraft('')
              }}
            >
              <option value={0}>新建模板</option>
              {templates.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
            </Select>
          </FormField>
          <FormField label="模板名称" required>
            <input value={creating ? nameDraft : (nameDraft || template?.name || '')} onChange={event => setNameDraft(event.target.value)} placeholder="例如 StarHub" />
          </FormField>
        </div>
        <div className="dialog-actions" style={{ justifyContent: 'flex-start', padding: 0 }}>
          {creating
            ? <button type="button" onClick={() => void createTemplate()} disabled={saving || !nameDraft.trim()}>创建</button>
            : <>
              <button type="button" className="ghost" onClick={() => void renameTemplate()} disabled={saving || !template}>保存名称</button>
              <button type="button" className="ghost" onClick={() => void deleteTemplate()} disabled={saving || !template}>删除模板</button>
              {onSelect && template && <button type="button" onClick={() => onSelect(template.id)} disabled={saving}><GitBranch size={14} />使用此模板</button>}
            </>}
        </div>
        {template && (
          <div className="form-grid two">
            {renderBranch('ipv4', ipv4Hops)}
            {renderBranch('ipv6', ipv6Hops)}
          </div>
        )}
      </div>
      {transport && (
        <TransportDialog
          target={transport.target}
          allowedModes={allowedModes}
          chainMethods={chainMethods}
          onCancel={() => setTransport(null)}
          onSubmit={selection => void submitHop(transport.family, { node_type: 'server_inbound' }, selection)}
        />
      )}
    </MotionDialogPanel>
  )
}
