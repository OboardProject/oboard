import { useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import { ArrowDown, ArrowUp, Check, ChevronDown, Globe, LayoutGrid, List, Search, SlidersHorizontal, X } from 'lucide-react'
import { createPopoverPortal } from '../../ui/modal-layer'
import { RegionFlag } from '../../ui/RegionFlag'
import { formatBytes } from '../../../shared/presentation'
import type { ServerSortMode, ServerStatusFilter } from '../../../server-list'
import type { Server } from '../../proxy-path/types'
import { formatByteRate, isServerEnrolled, isServerOnline, serverAttention, sumBy } from './fleet-format'

export type FleetView = 'grid' | 'list'

export type FleetStats = {
  total: number
  online: number
  offline: number
  unenrolled: number
  attention: number
  attentionHint: string
  downloadBps: number
  uploadBps: number
  trafficBytes: number
}

const attentionNames: Record<string, string> = {
  delivery: '授权待同步',
  time: '时间异常',
  agent: 'Agent 待更新',
  expiry: '即将到期',
  traffic: '流量将满',
}

// Offline and unenrolled servers already have their own filters, so attention
// counts only the problems hiding on servers that otherwise look healthy.
export function serverNeedsAttention(server: Server, expectedBuild?: string) {
  return serverAttention(server, expectedBuild).some(item => item.key in attentionNames)
}

export function fleetStats(servers: Server[], expectedBuild?: string): FleetStats {
  const reasons = new Map<string, number>()
  let attention = 0
  for (const server of servers) {
    const keys = serverAttention(server, expectedBuild).map(item => item.key).filter(key => key in attentionNames)
    if (keys.length) attention++
    for (const key of keys) reasons.set(key, (reasons.get(key) || 0) + 1)
  }
  const online = servers.filter(isServerOnline)
  const enrolled = servers.filter(isServerEnrolled)
  const top = [...reasons.entries()].sort((a, b) => b[1] - a[1])[0]
  return {
    total: servers.length,
    online: online.length,
    offline: enrolled.length - enrolled.filter(isServerOnline).length,
    unenrolled: servers.length - enrolled.length,
    attention,
    attentionHint: top ? `${attentionNames[top[0]]} ${top[1]} 台${reasons.size > 1 ? ' 等' : ''}` : '一切正常',
    downloadBps: sumBy(online, server => server.network_download_bps),
    uploadBps: sumBy(online, server => server.network_upload_bps),
    trafficBytes: sumBy(servers, server => (Number(server.traffic_upload_bytes) || 0) + (Number(server.traffic_download_bytes) || 0)),
  }
}

export function FleetSummary({ stats, statusFilter, onStatusFilter }: { stats: FleetStats; statusFilter: ServerStatusFilter; onStatusFilter: (value: ServerStatusFilter) => void }) {
  const toggle = (value: ServerStatusFilter) => onStatusFilter(statusFilter === value ? 'all' : value)
  const onlineShare = stats.total ? (stats.online / stats.total) * 100 : 0
  const offlineShare = stats.total ? (stats.offline / stats.total) * 100 : 0
  return (
    <section className="fleet-summary" aria-label="服务器概况">
      <button type="button" className="fleet-stat is-total" aria-pressed={statusFilter === 'all'} onClick={() => onStatusFilter('all')}>
        <span className="fleet-stat-label">服务器</span>
        <strong className="fleet-stat-value">{stats.total}</strong>
        <span className="fleet-stat-split" aria-hidden="true"><span className="is-online" style={{ width: `${onlineShare}%` }} /><span className="is-offline" style={{ width: `${offlineShare}%` }} /></span>
        <small className="fleet-stat-sub">{stats.unenrolled ? `${stats.unenrolled} 台未接入` : '全部已接入'}</small>
      </button>
      <button type="button" className="fleet-stat is-online" aria-pressed={statusFilter === 'online'} onClick={() => toggle('online')}>
        <span className="fleet-stat-label"><span className="fleet-stat-dot" aria-hidden="true" />在线</span>
        <strong className="fleet-stat-value">{stats.online}</strong>
        <small className="fleet-stat-sub">{stats.total ? `${Math.round(onlineShare)}% 可用` : '—'}</small>
      </button>
      <button type="button" className={`fleet-stat is-offline${stats.offline ? ' has-value' : ''}`} aria-pressed={statusFilter === 'offline'} onClick={() => toggle('offline')}>
        <span className="fleet-stat-label"><span className="fleet-stat-dot" aria-hidden="true" />离线</span>
        <strong className="fleet-stat-value">{stats.offline}</strong>
        <small className="fleet-stat-sub">{stats.offline ? '点击筛选' : '无离线'}</small>
      </button>
      <button type="button" className={`fleet-stat is-attention${stats.attention ? ' has-value' : ''}`} aria-pressed={statusFilter === 'attention'} onClick={() => toggle('attention')}>
        <span className="fleet-stat-label"><span className="fleet-stat-dot" aria-hidden="true" />需关注</span>
        <strong className="fleet-stat-value">{stats.attention}</strong>
        <small className="fleet-stat-sub">{stats.attentionHint}</small>
      </button>
      <div className="fleet-stat is-rate">
        <span className="fleet-stat-label">实时速率</span>
        <span className="fleet-stat-rates">
          <span className="fleet-rate is-down"><ArrowDown size={13} aria-hidden="true" /><strong>{formatByteRate(stats.downloadBps)}</strong></span>
          <span className="fleet-rate is-up"><ArrowUp size={13} aria-hidden="true" /><strong>{formatByteRate(stats.uploadBps)}</strong></span>
        </span>
        <small className="fleet-stat-sub">在线服务器合计</small>
      </div>
      <div className="fleet-stat is-traffic">
        <span className="fleet-stat-label">本期流量</span>
        <strong className="fleet-stat-value is-text">{formatBytes(stats.trafficBytes)}</strong>
        <small className="fleet-stat-sub">各服务器当前周期合计</small>
      </div>
    </section>
  )
}

type PopoverPosition = { top: number; left: number; width: number; maxHeight: number }

function useAnchoredPopover(open: boolean, close: () => void, anchor: RefObject<HTMLElement | null>, panel: RefObject<HTMLElement | null>, width: number, align: 'start' | 'end' = 'start') {
  const [position, setPosition] = useState<PopoverPosition | null>(null)
  useEffect(() => {
    if (!open) {
      setPosition(null)
      return
    }
    const place = () => {
      const rect = anchor.current?.getBoundingClientRect()
      if (!rect) return
      const pad = 10
      const resolved = Math.min(width, window.innerWidth - pad * 2)
      const preferred = align === 'end' ? rect.right - resolved : rect.left
      const left = Math.min(Math.max(pad, preferred), window.innerWidth - pad - resolved)
      const below = window.innerHeight - rect.bottom - pad - 6
      const above = rect.top - pad - 6
      const maxHeight = Math.min(420, Math.max(220, Math.max(below, above)))
      const top = below >= 240 || below >= above ? rect.bottom + 6 : Math.max(pad, rect.top - 6 - maxHeight)
      setPosition({ top, left, width: resolved, maxHeight })
    }
    place()
    const outside = (event: MouseEvent) => {
      const target = event.target as Node
      if (!anchor.current?.contains(target) && !panel.current?.contains(target)) close()
    }
    const escape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      close()
      anchor.current?.focus()
    }
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    document.addEventListener('mousedown', outside)
    document.addEventListener('keydown', escape)
    return () => {
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
      document.removeEventListener('mousedown', outside)
      document.removeEventListener('keydown', escape)
    }
  }, [open, width, align])
  return position
}

export function FleetSearch({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  return (
    <label className="fleet-search">
      <Search size={15} aria-hidden="true" />
      <input type="search" value={value} onChange={event => onChange(event.target.value)} onKeyDown={event => { if (event.key === 'Escape' && value) { event.preventDefault(); onChange('') } }} placeholder="搜索名称、IP、编号或地区" aria-label="搜索服务器" />
      {value ? <button type="button" className="ghost icon-button" onClick={() => onChange('')} aria-label="清除搜索" title="清除搜索"><X size={14} /></button> : null}
    </label>
  )
}

export function RegionFilter({ value, onChange, regions, total }: { value: string; onChange: (code: string) => void; regions: { code: string; label: string; count: number }[]; total: number }) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const triggerRef = useRef<HTMLButtonElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const position = useAnchoredPopover(open, () => setOpen(false), triggerRef, panelRef, 320)
  const isAll = value === 'all'
  const selected = isAll ? null : regions.find(region => (region.code || '') === value)
  const label = isAll ? '全部地区' : selected?.label || '待检测'
  const normalized = query.trim().toLocaleLowerCase('zh-CN')
  const filtered = useMemo(() => normalized ? regions.filter(region => region.label.toLocaleLowerCase('zh-CN').includes(normalized) || region.code.toLowerCase().includes(normalized)) : regions, [regions, normalized])
  useEffect(() => {
    if (open && position && window.innerWidth > 720) searchRef.current?.focus()
  }, [open, Boolean(position)])
  const choose = (code: string) => {
    onChange(code)
    setOpen(false)
    setQuery('')
    triggerRef.current?.focus()
  }
  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        className={`fleet-control fleet-region-trigger${isAll ? '' : ' is-active'}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={`按地区筛选：${label}，${isAll ? total : selected?.count ?? 0} 台`}
        onClick={() => { setQuery(''); setOpen(current => !current) }}
      >
        {isAll ? <Globe size={15} aria-hidden="true" /> : <RegionFlag code={selected?.code} size={16} />}
        <span className="fleet-control-label">{label}</span>
        <small>{isAll ? total : selected?.count ?? 0}</small>
        <ChevronDown size={14} aria-hidden="true" />
      </button>
      {open && position && createPopoverPortal(
        <div ref={panelRef} data-popover="true" className="fleet-popover fleet-region-panel" style={{ top: position.top, left: position.left, width: position.width, maxHeight: position.maxHeight }}>
          <label className="fleet-popover-search">
            <Search size={15} aria-hidden="true" />
            <input ref={searchRef} value={query} onChange={event => setQuery(event.target.value)} placeholder="搜索地区或代码" aria-label="搜索地区" />
          </label>
          <div className="fleet-region-list" role="listbox" aria-label="按地区筛选">
            {!normalized ? <button type="button" role="option" aria-selected={isAll} className="fleet-option" onClick={() => choose('all')}>
              <Globe size={16} aria-hidden="true" /><span>全部地区</span><small>{total}</small>{isAll ? <Check size={14} aria-hidden="true" /> : <span />}
            </button> : null}
            {filtered.map(region => {
              const active = !isAll && value === (region.code || '')
              return <button key={region.code || 'pending'} type="button" role="option" aria-selected={active} className="fleet-option" onClick={() => choose(region.code || '')}>
                <RegionFlag code={region.code} size={18} /><span>{region.code ? region.label : '待检测'}</span><small>{region.count}</small>{active ? <Check size={14} aria-hidden="true" /> : <span />}
              </button>
            })}
            {normalized && !filtered.length ? <p className="fleet-popover-empty">没有匹配的地区</p> : null}
          </div>
        </div>,
        document.body,
      )}
    </>
  )
}

const statusOptions: [ServerStatusFilter, string][] = [['all', '全部'], ['online', '在线'], ['offline', '离线'], ['attention', '需关注'], ['unenrolled', '未接入']]
const sortOptions: [ServerSortMode, string][] = [['created', '最近添加'], ['country', '按地区'], ['custom', '自定义']]

export function FilterPopover({ statusFilter, onStatusFilterChange, sortMode, onSortModeChange }: { statusFilter: ServerStatusFilter; onStatusFilterChange: (value: ServerStatusFilter) => void; sortMode: ServerSortMode; onSortModeChange: (value: ServerSortMode) => void }) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  const position = useAnchoredPopover(open, () => setOpen(false), triggerRef, panelRef, 280, 'end')
  const active = statusFilter !== 'all' || sortMode !== 'created'
  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        className={`fleet-control fleet-filter-trigger${active ? ' is-active' : ''}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label="筛选与排序"
        title="筛选与排序"
        onClick={() => setOpen(current => !current)}
      >
        <SlidersHorizontal size={15} aria-hidden="true" />
        <span className="fleet-control-label">{sortOptions.find(([value]) => value === sortMode)?.[1]}</span>
        {active ? <span className="fleet-control-dot" aria-hidden="true" /> : null}
      </button>
      {open && position && createPopoverPortal(
        <div ref={panelRef} data-popover="true" className="fleet-popover fleet-filter-panel" role="dialog" aria-label="服务器筛选与排序" style={{ top: position.top, left: position.left, width: position.width }}>
          <div className="fleet-popover-head">
            <strong>筛选与排序</strong>
            {active ? <button type="button" className="ghost" onClick={() => { onStatusFilterChange('all'); onSortModeChange('created') }}>重置</button> : null}
          </div>
          <fieldset className="fleet-popover-field">
            <legend>状态</legend>
            <div className="fleet-choice-grid">
              {statusOptions.map(([value, label]) => <button key={value} type="button" aria-pressed={statusFilter === value} onClick={() => onStatusFilterChange(value)}>{label}</button>)}
            </div>
          </fieldset>
          <fieldset className="fleet-popover-field">
            <legend>排序</legend>
            <div className="fleet-choice-grid is-three">
              {sortOptions.map(([value, label]) => <button key={value} type="button" aria-pressed={sortMode === value} onClick={() => onSortModeChange(value)}>{label}</button>)}
            </div>
            {sortMode === 'custom' ? <p className="fleet-popover-note">拖动卡片左侧手柄，或使用上下箭头调整顺序。</p> : null}
          </fieldset>
        </div>,
        document.body,
      )}
    </>
  )
}

export function ViewToggle({ value, onChange }: { value: FleetView; onChange: (value: FleetView) => void }) {
  return (
    <div className="fleet-view-toggle" role="group" aria-label="显示方式">
      <button type="button" aria-pressed={value === 'grid'} onClick={() => onChange('grid')} aria-label="卡片视图" title="卡片视图"><LayoutGrid size={15} aria-hidden="true" /></button>
      <button type="button" aria-pressed={value === 'list'} onClick={() => onChange('list')} aria-label="列表视图" title="列表视图"><List size={15} aria-hidden="true" /></button>
    </div>
  )
}
