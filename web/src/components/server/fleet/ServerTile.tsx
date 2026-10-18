import type { MouseEvent, ReactNode } from 'react'
import { ArrowDown, ArrowUp, BadgeCheck, Building2, Loader2, MapPin, Package, Settings2, Zap } from 'lucide-react'
import { formatBytes, formatDate } from '../../../shared/presentation'
import { serverExpiryDateLabel } from '../../../server-expiry'
import { serverMonitoring } from '../../../server-monitoring'
import { RegionFlag, serverRegionCode } from '../../ui/RegionFlag'
import type { Server } from '../../proxy-path/types'
import { ServerActionMenu } from '../ServerActionMenu'
import { ServerAddresses } from './ServerAddresses'
import {
  cardExpiryStatus,
  cpuCoresLabel,
  cpuModelLabel,
  enrolledDaysLabel,
  formatByteRate,
  formatPercent,
  isServerEnrolled,
  isServerOnline,
  offlineAgoLabel,
  resourcePercent,
  serverAttention,
  serverDisplayName,
  serverTraffic,
  usageTone,
  type ServerAttention,
  type Tone,
} from './fleet-format'

type Role = 'admin' | 'operator' | 'viewer' | 'none'

export type ServerItemProps = {
  server: Server
  role?: Role
  expectedBuild?: string
  uninstalling?: boolean
  selected?: boolean
  sortControls?: ReactNode
  onAction: (type: string, server: Server) => void
}

// The whole surface opens load and latency details, except clicks that land on
// its own controls or bubble up through a portal (the action menu).
function openDetailsOnSurface(event: MouseEvent<HTMLElement>, open: () => void) {
  const target = event.target as Element | null
  if (!target || !event.currentTarget.contains(target)) return
  if (target.closest('button, a, input, select, textarea, [role="menu"], [role="dialog"]')) return
  if (window.getSelection?.()?.toString()) return
  open()
}

function canManage(role?: Role) {
  return role === 'admin' || role === 'operator'
}

export function displayTagIcon(tone?: string) {
  switch (tone) {
    case 'orange': return <MapPin size={11} aria-hidden="true" />
    case 'purple': return <Zap size={11} aria-hidden="true" />
    case 'green': return <BadgeCheck size={11} aria-hidden="true" />
    case 'gray': return <Package size={11} aria-hidden="true" />
    default: return <Building2 size={11} aria-hidden="true" />
  }
}

export function serverStatusView(server: Server) {
  if (!isServerEnrolled(server)) return { tone: 'idle', label: '未接入', title: '尚未安装 Agent' }
  if (isServerOnline(server)) return { tone: 'online', label: '在线', title: '在线' }
  return { tone: 'offline', label: '离线', title: offlineAgoLabel(server.last_seen_at) }
}

export function StatusPill({ server }: { server: Server }) {
  const status = serverStatusView(server)
  return <span className={`fleet-status is-${status.tone}`} title={status.title}><span className="fleet-status-dot" aria-hidden="true" />{status.label}</span>
}

export function Meter({ label, value, percent, sub, tone = '', title, muted = false }: { label: string; value: string; percent: number; sub?: string; tone?: Tone; title?: string; muted?: boolean }) {
  const resolved = tone || usageTone(percent)
  return (
    <div className={`fleet-meter${muted ? ' is-muted' : ''}`} title={title}>
      <div className="fleet-meter-head"><span>{label}</span><strong>{value}</strong></div>
      <div className="fleet-meter-track" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(percent)}>
        <span className={`fleet-meter-fill${resolved ? ` is-${resolved}` : ''}`} style={{ width: `${percent > 0 ? Math.max(percent, 2) : 0}%` }} />
      </div>
      {sub ? <small className="fleet-meter-sub">{sub}</small> : null}
    </div>
  )
}

export function QualityBar({ segments, label }: { segments: string[]; label: string }) {
  return <span className="fleet-quality-bar" aria-hidden="true" data-label={label}>{segments.map((tone, index) => <span key={index} className={tone ? `is-${tone}` : ''} />)}</span>
}

function AttentionChips({ items, server, uninstalling, onAction }: { items: ServerAttention[]; server: Server; uninstalling?: boolean; onAction: ServerItemProps['onAction'] }) {
  const visible = items.filter(item => item.key === 'delivery' || item.key === 'time' || item.key === 'agent')
  if (!visible.length && !uninstalling) return null
  return (
    <div className="fleet-chips">
      {uninstalling ? <span className="fleet-chip is-warning" role="status"><Loader2 size={12} className="spin" aria-hidden="true" />卸载并删除中</span> : null}
      {visible.map(item => item.action
        ? <button key={item.key} type="button" className={`fleet-chip is-${item.tone}`} title={item.detail || item.label} aria-label={`${item.label}${item.detail ? `：${item.detail}` : ''}`} onClick={event => { event.stopPropagation(); onAction(item.action!, server) }}>{item.label}</button>
        : <span key={item.key} className={`fleet-chip is-${item.tone}`} title={item.detail || item.label}>{item.label}</span>)}
    </div>
  )
}

function serverMetrics(server: Server) {
  const online = isServerOnline(server)
  const cpu = Number.isFinite(server.cpu_usage_percent) ? Number(server.cpu_usage_percent) : 0
  const memory = resourcePercent(server.memory_used_bytes, server.memory_total_bytes)
  const disk = resourcePercent(server.disk_bytes, server.disk_total_bytes)
  const monitoring = serverMonitoring(server.monitoring_display, online)
  return { online, cpu, memory, disk, monitoring, traffic: serverTraffic(server) }
}

function lossLabel(loss: number | null, online: boolean) {
  return !online || loss == null ? '—' : `${loss.toFixed(loss >= 10 ? 0 : 1)}%`
}

function TargetButton({ server, name, onAction }: { server: Server; name: string; onAction: ServerItemProps['onAction'] }) {
  return <button type="button" className="fleet-target-button" aria-label={`选择 ${serverDisplayName(server)} 的监控目标，当前：${name}`} title={`监控目标：${name}`} onClick={event => { event.stopPropagation(); onAction('monitoring-target', server) }}><Settings2 size={12} aria-hidden="true" /></button>
}

export function ServerTile({ server, role, expectedBuild, uninstalling, selected, sortControls, onAction }: ServerItemProps) {
  const name = serverDisplayName(server)
  const { online, cpu, memory, disk, monitoring, traffic } = serverMetrics(server)
  const attention = serverAttention(server, expectedBuild)
  const expiry = cardExpiryStatus(server)
  const age = enrolledDaysLabel(server)
  const tags = (server.display_tags || []).filter(tag => String(tag.text || '').trim())
  const na = !online
  return (
    <article className={`fleet-tile${online ? '' : ' is-offline'}${selected ? ' is-selected' : ''}`} aria-label={name} onClick={event => openDetailsOnSurface(event, () => onAction('resource-details', server))}>
      <button type="button" className="fleet-tile-hit" onClick={() => onAction('resource-details', server)} aria-label={`查看 ${name} 的负载与延迟`} />
      <header className="fleet-tile-head">
        {sortControls}
        <RegionFlag code={serverRegionCode(server)} size={22} className="fleet-flag" />
        <div className="fleet-tile-title">
          <button type="button" className="fleet-name" onClick={event => { event.stopPropagation(); onAction('inspect', server) }} title="查看概要">{name}</button>
          <ServerAddresses server={server} />
        </div>
        <div className="fleet-tile-tools">
          <StatusPill server={server} />
          <ServerActionMenu server={server} role={role} onAction={onAction} />
        </div>
      </header>
      <AttentionChips items={attention} server={server} uninstalling={uninstalling} onAction={onAction} />
      <div className="fleet-tile-meters">
        <Meter label="CPU" value={na ? '—' : formatPercent(cpu)} percent={na ? 0 : cpu} sub={cpuCoresLabel(server)} title={cpuModelLabel(server) || undefined} muted={na} />
        <Meter label="内存" value={na || !server.memory_total_bytes ? '—' : formatPercent(memory)} percent={na ? 0 : memory} sub={server.memory_total_bytes ? `${formatBytes(server.memory_used_bytes || 0)} / ${formatBytes(server.memory_total_bytes)}` : '—'} muted={na} />
        <Meter label="磁盘" value={na || !server.disk_total_bytes ? '—' : formatPercent(disk)} percent={na ? 0 : disk} sub={server.disk_total_bytes ? `${formatBytes(server.disk_bytes || 0)} / ${formatBytes(server.disk_total_bytes)}` : '—'} muted={na} />
      </div>
      <div className="fleet-tile-network">
        <div className="fleet-rates">
          <span className="fleet-rate is-down" title={`下载 · 本期累计 ${formatBytes(server.traffic_download_bytes || 0)}`}><ArrowDown size={13} aria-hidden="true" /><strong>{na ? '—' : formatByteRate(server.network_download_bps || 0)}</strong></span>
          <span className="fleet-rate is-up" title={`上传 · 本期累计 ${formatBytes(server.traffic_upload_bytes || 0)}`}><ArrowUp size={13} aria-hidden="true" /><strong>{na ? '—' : formatByteRate(server.network_upload_bps || 0)}</strong></span>
        </div>
        <Meter
          label="本期流量"
          value={traffic.limit > 0 ? `${formatBytes(traffic.used)} / ${formatBytes(traffic.limit)}` : formatBytes(traffic.used)}
          percent={traffic.percent}
          tone={traffic.tone}
          title={`↓ ${formatBytes(server.traffic_download_bytes || 0)} · ↑ ${formatBytes(server.traffic_upload_bytes || 0)}`}
        />
      </div>
      <div className="fleet-tile-quality" title={`${monitoring.name} · 最近 20 轮探测`}>
        <div className="fleet-quality">
          <span className="fleet-quality-label">延迟</span>
          <strong>{monitoring.latency}</strong>
          <QualityBar segments={monitoring.qualitySegments} label="延迟" />
        </div>
        <div className="fleet-quality">
          <span className="fleet-quality-label">丢包{canManage(role) ? <TargetButton server={server} name={monitoring.name} onAction={onAction} /> : null}</span>
          <strong>{lossLabel(monitoring.loss, online)}</strong>
          <QualityBar segments={monitoring.lossSegments} label="丢包" />
        </div>
      </div>
      <footer className="fleet-tile-foot">
        <span title={`到期 ${serverExpiryDateLabel(server.expires_at)}`}>到期 <strong className={`tone-${expiry.tone}`}>{expiry.label}</strong></span>
        <span title={server.created_at ? `入库时间 ${formatDate(server.created_at)}` : undefined}>已运行 <strong>{age || '—'}</strong></span>
        {tags.length ? <span className="fleet-tags">{tags.map(tag => <span key={`${tag.tone}-${tag.text}`} className={`fleet-tag tone-${tag.tone || 'blue'}`}>{displayTagIcon(tag.tone)}{tag.text}</span>)}</span> : null}
      </footer>
    </article>
  )
}

export const serverListColumns = [
  { key: 'server', label: '服务器' },
  { key: 'status', label: '状态' },
  { key: 'cpu', label: 'CPU' },
  { key: 'memory', label: '内存' },
  { key: 'disk', label: '磁盘' },
  { key: 'rate', label: '实时速率' },
  { key: 'traffic', label: '本期流量' },
  { key: 'quality', label: '延迟 / 丢包' },
  { key: 'expiry', label: '到期' },
] as const

export function ServerListHeader({ sortable = false }: { sortable?: boolean }) {
  return (
    <div className={`fleet-row fleet-row-head${sortable ? ' is-sortable' : ''}`} role="row">
      {sortable ? <span className="fleet-cell fleet-cell-sort" role="columnheader"><span className="sr-only">排序</span></span> : null}
      {serverListColumns.map(column => <span key={column.key} className={`fleet-cell fleet-cell-${column.key}`} role="columnheader">{column.label}</span>)}
      <span className="fleet-cell fleet-cell-actions" role="columnheader"><span className="sr-only">操作</span></span>
    </div>
  )
}

function CompactMeter({ percent, value, sub, tone = '', muted = false, label }: { percent: number; value: string; sub?: string; tone?: Tone; muted?: boolean; label: string }) {
  const resolved = tone || usageTone(percent)
  return (
    <span className={`fleet-compact-meter${muted ? ' is-muted' : ''}`}>
      <span className="fleet-compact-value"><span className="fleet-compact-label">{label}</span>{value}</span>
      <span className="fleet-meter-track" aria-hidden="true"><span className={`fleet-meter-fill${resolved ? ` is-${resolved}` : ''}`} style={{ width: `${percent > 0 ? Math.max(percent, 3) : 0}%` }} /></span>
      {sub ? <small>{sub}</small> : null}
    </span>
  )
}

export function ServerRow({ server, role, expectedBuild, uninstalling, selected, sortControls, onAction }: ServerItemProps) {
  const name = serverDisplayName(server)
  const { online, cpu, memory, disk, monitoring, traffic } = serverMetrics(server)
  const attention = serverAttention(server, expectedBuild)
  const expiry = cardExpiryStatus(server)
  const na = !online
  return (
    <article className={`fleet-row${online ? '' : ' is-offline'}${selected ? ' is-selected' : ''}${sortControls ? ' is-sortable' : ''}`} role="row" aria-label={name} onClick={event => openDetailsOnSurface(event, () => onAction('resource-details', server))}>
      <button type="button" className="fleet-tile-hit" onClick={() => onAction('resource-details', server)} aria-label={`查看 ${name} 的负载与延迟`} />
      {sortControls ? <span className="fleet-cell fleet-cell-sort" role="cell">{sortControls}</span> : null}
      <div className="fleet-cell fleet-cell-server" role="cell">
        <RegionFlag code={serverRegionCode(server)} size={20} className="fleet-flag" />
        <div className="fleet-row-title">
          <button type="button" className="fleet-name" onClick={event => { event.stopPropagation(); onAction('inspect', server) }} title="查看概要">{name}</button>
          <ServerAddresses server={server} />
          <AttentionChips items={attention} server={server} uninstalling={uninstalling} onAction={onAction} />
        </div>
      </div>
      <div className="fleet-cell fleet-cell-status" role="cell"><StatusPill server={server} /></div>
      <div className="fleet-cell fleet-cell-cpu" role="cell"><CompactMeter label="CPU" percent={na ? 0 : cpu} value={na ? '—' : formatPercent(cpu)} sub={cpuCoresLabel(server)} muted={na} /></div>
      <div className="fleet-cell fleet-cell-memory" role="cell"><CompactMeter label="内存" percent={na ? 0 : memory} value={na || !server.memory_total_bytes ? '—' : formatPercent(memory)} sub={server.memory_total_bytes ? formatBytes(server.memory_total_bytes) : undefined} muted={na} /></div>
      <div className="fleet-cell fleet-cell-disk" role="cell"><CompactMeter label="磁盘" percent={na ? 0 : disk} value={na || !server.disk_total_bytes ? '—' : formatPercent(disk)} sub={server.disk_total_bytes ? formatBytes(server.disk_total_bytes) : undefined} muted={na} /></div>
      <div className="fleet-cell fleet-cell-rate" role="cell">
        <span className="fleet-rate is-down"><ArrowDown size={12} aria-hidden="true" /><strong>{na ? '—' : formatByteRate(server.network_download_bps || 0)}</strong></span>
        <span className="fleet-rate is-up"><ArrowUp size={12} aria-hidden="true" /><strong>{na ? '—' : formatByteRate(server.network_upload_bps || 0)}</strong></span>
      </div>
      <div className="fleet-cell fleet-cell-traffic" role="cell"><CompactMeter label="流量" percent={traffic.percent} tone={traffic.tone} value={formatBytes(traffic.used)} sub={traffic.limit > 0 ? `/ ${formatBytes(traffic.limit)}` : '不限'} /></div>
      <div className="fleet-cell fleet-cell-quality" role="cell" title={`${monitoring.name} · 最近 20 轮探测`}>
        <span className="fleet-quality-inline"><strong>{monitoring.latency}</strong><QualityBar segments={monitoring.qualitySegments} label="延迟" /></span>
        <span className="fleet-quality-inline"><strong>{lossLabel(monitoring.loss, online)}</strong><QualityBar segments={monitoring.lossSegments} label="丢包" />{canManage(role) ? <TargetButton server={server} name={monitoring.name} onAction={onAction} /> : null}</span>
      </div>
      <div className="fleet-cell fleet-cell-expiry" role="cell" title={`到期 ${serverExpiryDateLabel(server.expires_at)}`}><strong className={`tone-${expiry.tone}`}>{expiry.label}</strong></div>
      <div className="fleet-row-metrics" aria-hidden="true">
        <CompactMeter label="CPU" percent={na ? 0 : cpu} value={na ? '—' : formatPercent(cpu)} muted={na} />
        <CompactMeter label="内存" percent={na ? 0 : memory} value={na || !server.memory_total_bytes ? '—' : formatPercent(memory)} muted={na} />
        <CompactMeter label="流量" percent={traffic.percent} tone={traffic.tone} value={formatBytes(traffic.used)} />
        <span className="fleet-compact-meter"><span className="fleet-compact-value"><span className="fleet-compact-label">延迟</span>{monitoring.latency}</span><QualityBar segments={monitoring.qualitySegments} label="延迟" /></span>
      </div>
      <div className="fleet-cell fleet-cell-actions" role="cell"><ServerActionMenu server={server} role={role} onAction={onAction} /></div>
    </article>
  )
}
