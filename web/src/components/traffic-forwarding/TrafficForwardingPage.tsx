import React, { useMemo, useState } from 'react'
import { AnimatePresence } from 'motion/react'
import { ArrowLeftRight, ArrowRight, Cable, CheckCircle2, CircleAlert, Edit3, Gauge, Network, Play, Plus, Power, Search, Server as ServerIcon, Trash2 } from 'lucide-react'

import type { Server } from '../proxy-path/types'
import { useDialogs } from '../ui/dialog-context'
import { OverflowMenu } from '../ui/overflow-menu'
import { RegionFlag, serverRegionCode } from '../ui/RegionFlag'
import { Select } from '../ui/select'
import { Switch } from '../ui/switch'
import { ForwardingEditorDialog, type ForwardingEditorState } from './ForwardingEditorDialog'
import {
  buildForwardingItems,
  emptyTrafficForwardDraft,
  emptyTunnelDraft,
  itemSearchText,
  itemTargetServerID,
  listenLabel,
  parseProbeDetails,
  protocolLabel,
  serverAddress,
  trafficForwardDraftFromForward,
  trafficForwardPayload,
  tunnelDraftFromTunnel,
  tunnelPayload,
  tunnelTypeLabel,
  type ForwardingItem,
  type ForwardingKind,
  type TrafficForward,
  type TrafficForwardDraft,
  type TrafficForwardProbe,
  type TrafficTunnel,
  type TunnelDraft,
} from './model'
import './TrafficForwarding.css'

type RequestClient = { request: (path: string, init?: RequestInit) => Promise<unknown> }
type Notify = (message: string, tone?: 'success' | 'error' | 'info') => void
type StatusFilter = 'all' | 'attention' | 'off'
type KindFilter = 'all' | ForwardingKind

export function TrafficForwardingPage({
  data,
  client,
  load,
  notify,
  localizeError = message => message,
}: {
  data: { servers?: Server[]; port_forwards?: TrafficForward[]; port_forward_probes?: TrafficForwardProbe[]; tunnels?: TrafficTunnel[] }
  client: RequestClient
  load: () => Promise<void>
  notify?: Notify
  localizeError?: (message: string) => string
}) {
  const dialogs = useDialogs()
  const servers = data.servers || []
  const forwards = data.port_forwards || []
  const tunnels = data.tunnels || []
  const [query, setQuery] = useState('')
  const [kindFilter, setKindFilter] = useState<KindFilter>('all')
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [serverFilter, setServerFilter] = useState(0)
  const [editor, setEditor] = useState<ForwardingEditorState | null>(null)
  const [busyKeys, setBusyKeys] = useState<Set<string>>(() => new Set())

  const items = useMemo(() => buildForwardingItems(forwards, data.port_forward_probes || [], tunnels), [forwards, data.port_forward_probes, tunnels])
  const counts = useMemo(() => ({
    all: items.length,
    healthy: items.filter(entry => entry.health.tone === 'success').length,
    attention: items.filter(entry => entry.health.tone === 'danger').length,
    off: items.filter(entry => entry.health.tone === 'off').length,
    forward: items.filter(entry => entry.kind === 'forward').length,
    tunnel: items.filter(entry => entry.kind === 'tunnel').length,
  }), [items])

  const visible = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase('zh-CN')
    return items.filter(entry => {
      if (kindFilter !== 'all' && entry.kind !== kindFilter) return false
      if (statusFilter === 'attention' && entry.health.tone !== 'danger') return false
      if (statusFilter === 'off' && entry.health.tone !== 'off') return false
      if (serverFilter && entry.item.source_server_id !== serverFilter && itemTargetServerID(entry) !== serverFilter) return false
      return !normalized || itemSearchText(entry, servers).includes(normalized)
    })
  }, [items, kindFilter, statusFilter, serverFilter, query, servers])

  const groups = useMemo(() => {
    const bySource = new Map<number, ForwardingItem[]>()
    visible.forEach(entry => {
      const list = bySource.get(entry.item.source_server_id) || []
      list.push(entry)
      bySource.set(entry.item.source_server_id, list)
    })
    const order = new Map(servers.map((server, index) => [server.id, index]))
    return Array.from(bySource.entries())
      .sort(([left], [right]) => (order.get(left) ?? Number.MAX_SAFE_INTEGER) - (order.get(right) ?? Number.MAX_SAFE_INTEGER))
      .map(([serverID, entries]) => ({ serverID, server: servers.find(server => server.id === serverID), entries }))
  }, [visible, servers])

  const filtered = Boolean(query.trim()) || kindFilter !== 'all' || statusFilter !== 'all' || serverFilter > 0
  const clearFilters = () => { setQuery(''); setKindFilter('all'); setStatusFilter('all'); setServerFilter(0) }

  const emptyDraft = (kind: ForwardingKind, sourceServerID: number): ForwardingEditorState => kind === 'forward'
    ? { kind, draft: emptyTrafficForwardDraft(servers, forwards, sourceServerID) }
    : { kind, draft: emptyTunnelDraft(servers, sourceServerID) }

  const openCreate = (sourceServerID = serverFilter) => {
    if (!servers.length) return
    setEditor(emptyDraft('forward', sourceServerID || servers[0].id))
  }
  const openEdit = (entry: ForwardingItem) => setEditor(entry.kind === 'forward'
    ? { kind: 'forward', id: entry.item.id, draft: trafficForwardDraftFromForward(entry.item) }
    : { kind: 'tunnel', id: entry.item.id, draft: tunnelDraftFromTunnel(entry.item) })

  const request = async (path: string, init?: RequestInit) => {
    try {
      return await client.request(path, init)
    } catch (error: any) {
      throw new Error(localizeError(String(error?.message || error)))
    }
  }

  const withBusy = async (key: string, action: () => Promise<void>) => {
    setBusyKeys(current => new Set(current).add(key))
    try {
      await action()
    } catch (error: any) {
      notify?.(String(error?.message || error), 'error')
    } finally {
      setBusyKeys(current => { const next = new Set(current); next.delete(key); return next })
    }
  }

  const saveForward = async (draft: TrafficForwardDraft, id?: number) => {
    await request(id ? `/port-forwards/${id}` : '/port-forwards', { method: id ? 'PATCH' : 'POST', body: JSON.stringify(trafficForwardPayload(draft, servers)) })
    setEditor(null)
    notify?.(id ? '端口转发已保存，正在同步' : '端口转发已创建，正在同步', 'success')
    await load()
  }
  const saveTunnel = async (draft: TunnelDraft, id?: number) => {
    await request(id ? `/tunnels/${id}` : '/tunnels', { method: id ? 'PATCH' : 'POST', body: JSON.stringify(tunnelPayload(draft, servers)) })
    setEditor(null)
    notify?.(id ? '服务器隧道已保存，正在同步' : '服务器隧道已创建，正在同步', 'success')
    await load()
  }

  const toggle = (entry: ForwardingItem, enabled: boolean) => withBusy(entry.key, async () => {
    if (entry.kind === 'forward') {
      await request(`/port-forwards/${entry.item.id}`, { method: 'PATCH', body: JSON.stringify({ ...trafficForwardDraftFromForward(entry.item), enabled }) })
    } else {
      await request(`/tunnels/${entry.item.id}`, { method: 'PATCH', body: JSON.stringify({ ...tunnelPayload(tunnelDraftFromTunnel(entry.item), servers), enabled }) })
    }
    notify?.(`${enabled ? '已启用' : '已停用'}「${entry.item.name}」`, 'success')
    await load()
  })

  const probe = (entry: ForwardingItem) => withBusy(entry.key, async () => {
    await request(`/port-forwards/${entry.item.id}/probe`, { method: 'POST', body: '{}' })
    notify?.(`已开始检查「${entry.item.name}」，结果稍后更新`, 'success')
  })

  const remove = (entry: ForwardingItem) => dialogs.confirm({
    title: entry.kind === 'forward' ? '删除端口转发' : '删除服务器隧道',
    message: entry.kind === 'forward'
      ? `确认删除「${entry.item.name}」？入口服务器会停止监听该端口，检查记录一并删除。`
      : `确认删除「${entry.item.name}」？两端服务器之间的隧道会被拆除。`,
    tone: 'danger',
    confirmText: '删除',
    pendingText: '删除中…',
    onConfirm: async () => {
      await request(`/${entry.kind === 'forward' ? 'port-forwards' : 'tunnels'}/${entry.item.id}`, { method: 'DELETE' })
      await load()
    },
  })

  return (
    <div className="tf-page">
      <section className="tf-summary" aria-label="转发概况">
        <SummaryTile label="全部转发" value={counts.all} detail={`端口转发 ${counts.forward} · 隧道 ${counts.tunnel}`} icon={<Network size={16} />} tone="primary" active={statusFilter === 'all'} onClick={() => setStatusFilter('all')} />
        <SummaryTile label="检查正常" value={counts.healthy} detail="最近一次检查可达" icon={<CheckCircle2 size={16} />} tone="success" />
        <SummaryTile label="需要处理" value={counts.attention} detail={counts.attention ? '点击只看异常项' : '没有异常'} icon={<CircleAlert size={16} />} tone="danger" active={statusFilter === 'attention'} onClick={counts.attention ? () => setStatusFilter(statusFilter === 'attention' ? 'all' : 'attention') : undefined} />
        <SummaryTile label="已停用" value={counts.off} detail={counts.off ? '点击只看停用项' : '全部在运行'} icon={<Power size={16} />} tone="muted" active={statusFilter === 'off'} onClick={counts.off ? () => setStatusFilter(statusFilter === 'off' ? 'all' : 'off') : undefined} />
      </section>

      <div className="tf-toolbar">
        <label className="tf-search">
          <Search size={14} aria-hidden="true" />
          <input value={query} onChange={event => setQuery(event.target.value)} aria-label="搜索转发" placeholder="搜索名称、服务器、地址或端口" />
        </label>
        <Select variant="segmented" value={kindFilter} aria-label="转发类型" onChange={event => setKindFilter(event.target.value as KindFilter)}>
          <option value="all">全部</option>
          <option value="forward">端口转发</option>
          <option value="tunnel">隧道</option>
        </Select>
        <Select className="tf-server-filter" value={serverFilter} aria-label="按服务器筛选" onChange={event => setServerFilter(Number(event.target.value))}>
          <option value={0}>全部服务器</option>
          {servers.map(server => <option key={server.id} value={server.id}>{server.name}</option>)}
        </Select>
        <button type="button" className="tf-create" onClick={() => openCreate()} disabled={!servers.length}><Plus size={15} aria-hidden="true" />新建转发</button>
      </div>

      {groups.length > 0 ? (
        <div className="tf-groups">
          {groups.map(group => (
            <section key={group.serverID} className="tf-group" aria-label={`入口 ${group.server?.name || group.serverID}`}>
              <header className="tf-group-head">
                <span className="tf-server-badge" data-online={group.server?.status === 'online'} aria-hidden="true">
                  <RegionFlag code={serverRegionCode(group.server)} size={16} fallback={<ServerIcon size={14} />} />
                </span>
                <div>
                  <strong>{group.server?.name || `服务器 ${group.serverID}`}</strong>
                  <small>{[serverAddress(group.server), group.server?.status === 'online' ? '在线' : '离线，恢复后自动同步'].filter(Boolean).join(' · ')}</small>
                </div>
                <span className="tf-group-count tabular-nums">{group.entries.length}</span>
                <button type="button" className="ghost icon-button" onClick={() => openCreate(group.serverID)} aria-label={`在 ${group.server?.name || '该服务器'} 新建转发`} title="在此服务器新建"><Plus size={15} /></button>
              </header>
              <div className="tf-rows">
                {group.entries.map(entry => (
                  <ForwardingRow
                    key={entry.key}
                    entry={entry}
                    servers={servers}
                    busy={busyKeys.has(entry.key)}
                    onEdit={() => openEdit(entry)}
                    onToggle={enabled => void toggle(entry, enabled)}
                    onProbe={() => void probe(entry)}
                    onDelete={() => void remove(entry)}
                  />
                ))}
              </div>
            </section>
          ))}
        </div>
      ) : (
        <div className="tf-empty">
          <span aria-hidden="true">{filtered ? <Search size={22} /> : <ArrowLeftRight size={22} />}</span>
          <strong>{filtered ? '没有符合条件的转发' : servers.length ? '还没有转发' : '需要先添加服务器'}</strong>
          <p>{filtered ? '换个关键字，或清除筛选条件。' : servers.length ? '端口转发把入口端口的流量送到目标地址；服务器隧道在两台服务器之间建立加密链路。' : '添加服务器后即可配置端口转发和服务器隧道。'}</p>
          {filtered ? <button type="button" className="ghost" onClick={clearFilters}>清除筛选</button> : servers.length > 0 && <button type="button" onClick={() => openCreate()}><Plus size={15} aria-hidden="true" />新建转发</button>}
        </div>
      )}

      <p className="tf-footnote">代理拓扑中由链路自动生成的转发和隧道不在此处列出，请在代理拓扑中管理。</p>

      <AnimatePresence>
        {editor && <ForwardingEditorDialog
          key={`${editor.kind}-${editor.id || 'new'}`}
          servers={servers}
          initial={editor}
          emptyDraft={emptyDraft}
          onCancel={() => setEditor(null)}
          onSaveForward={saveForward}
          onSaveTunnel={saveTunnel}
        />}
      </AnimatePresence>
    </div>
  )
}

function SummaryTile({ label, value, detail, icon, tone, active = false, onClick }: { label: string; value: number; detail: string; icon: React.ReactNode; tone: 'primary' | 'success' | 'danger' | 'muted'; active?: boolean; onClick?: () => void }) {
  const content = <>
    <span className="tf-summary-icon" aria-hidden="true">{icon}</span>
    <span className="tf-summary-copy"><small>{label}</small><strong className="tabular-nums">{value}</strong><em>{detail}</em></span>
  </>
  if (!onClick) return <div className="tf-summary-tile" data-tone={tone}>{content}</div>
  return <button type="button" className="tf-summary-tile" data-tone={tone} data-active={active} aria-pressed={active} onClick={onClick}>{content}</button>
}

function ForwardingRow({ entry, servers, busy, onEdit, onToggle, onProbe, onDelete }: {
  entry: ForwardingItem
  servers: Server[]
  busy: boolean
  onEdit: () => void
  onToggle: (enabled: boolean) => void
  onProbe: () => void
  onDelete: () => void
}) {
  const target = servers.find(server => server.id === itemTargetServerID(entry))
  const health = entry.health
  const isForward = entry.kind === 'forward'
  const route = isForward
    ? {
      from: `:${entry.item.listen_port}`,
      fromDetail: listenLabel(entry.item.listen_ip) === '*' ? '全部地址' : entry.item.listen_ip,
      to: target?.name || entry.item.target_address || '目标',
      toDetail: `${entry.item.target_address || serverAddress(target) || '自动解析'}:${entry.item.target_port}`,
    }
    : {
      from: entry.item.local_address || '本端',
      fromDetail: entry.item.listen_port ? `端口 ${entry.item.listen_port}` : '端口自动',
      to: target?.name || `服务器 ${entry.item.target_server_id}`,
      toDetail: `${entry.item.target_endpoint || serverAddress(target) || '自动'}${entry.item.target_port ? `:${entry.item.target_port}` : ''}`,
    }
  const probeSummary = isForward && entry.probe ? `${parseProbeDetails(entry.probe).successCount}/${entry.probe.sample_count} 次成功` : ''

  return (
    <article className="tf-row" data-kind={entry.kind} data-enabled={entry.item.enabled}>
      <button type="button" className="tf-row-main" onClick={onEdit} aria-label={`编辑 ${entry.item.name}`}>
        <span className="tf-row-kind" aria-hidden="true">{isForward ? <Network size={15} /> : <Cable size={15} />}</span>
        <span className="tf-row-title">
          <strong title={entry.item.name}>{entry.item.name}</strong>
          <small>{isForward ? `端口转发 · ${protocolLabel(entry.item.protocol)}` : `隧道 · ${tunnelTypeLabel(entry.item.type)}`}</small>
        </span>
      </button>
      <div className="tf-row-route">
        <span><strong>{route.from}</strong><small>{route.fromDetail}</small></span>
        {isForward ? <ArrowRight size={14} aria-hidden="true" /> : <ArrowLeftRight size={14} aria-hidden="true" />}
        <span><strong>{route.to}</strong><small>{route.toDetail}</small></span>
      </div>
      <div className="tf-row-health" data-tone={health.tone}>
        <span aria-hidden="true">{health.tone === 'success' ? <CheckCircle2 size={14} /> : health.tone === 'danger' ? <CircleAlert size={14} /> : health.tone === 'off' ? <Power size={14} /> : <Gauge size={14} />}</span>
        <span><strong>{health.label}</strong><small title={health.detail}>{probeSummary || health.detail}</small></span>
      </div>
      <div className="tf-row-actions">
        <Switch checked={entry.item.enabled} disabled={busy} onChange={onToggle} ariaLabel={`${entry.item.enabled ? '停用' : '启用'} ${entry.item.name}`} size="sm" />
        {!isForward && <span className="tf-action-spacer" aria-hidden="true" />}
        {isForward && <button type="button" className="ghost icon-button" onClick={onProbe} disabled={busy || !entry.item.enabled} aria-label={`立即检查 ${entry.item.name}`} title="立即检查"><Play size={14} /></button>}
        <OverflowMenu
          label={`${entry.item.name} 的更多操作`}
          groups={[
            { key: 'edit', items: [{ key: 'edit', label: '编辑', icon: Edit3, onSelect: onEdit }] },
            { key: 'danger', items: [{ key: 'delete', label: '删除', icon: Trash2, danger: true, disabled: busy, onSelect: onDelete }] },
          ]}
        />
      </div>
    </article>
  )
}
