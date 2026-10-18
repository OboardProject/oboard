import React, { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { createPopoverPortal as createPortal } from '../ui/modal-layer'
import { Activity, ClipboardList, Gauge, Info, MoreHorizontal, Network, RefreshCw, Settings2, SlidersHorizontal, SquareTerminal, Terminal, Trash2, X } from 'lucide-react'
import type { Server } from '../proxy-path/types'
import { RegionFlag, serverRegionCode } from '../ui/RegionFlag'

type Role = 'admin' | 'operator' | 'viewer' | 'none'

function hasManagementAccess(role: Role) {
  return role === 'admin' || role === 'operator'
}

type Item = {
  label: string
  type: string
  icon: React.ComponentType<{ size?: number; 'aria-hidden'?: boolean | 'true' | 'false' }>
  hint?: string
  admin?: boolean
  danger?: boolean
  disabled?: string
}

type Group = { label: string; items: Item[] }

const MENU_WIDTH = 232
const SHEET_QUERY = '(max-width: 640px)'

export function serverActionGroups(server: Server, role: Role = 'viewer'): Group[] {
  const enrolled = Boolean(String(server.agent_id || '').trim())
  const online = String(server.status || '').toLowerCase() === 'online'
  const terminalBlocked = !enrolled ? '请先安装并连接 Agent' : !online ? 'Agent 当前离线' : ''
  const deliveryPending = enrolled && online && (server.authorization_confirmed === false || server.users_confirmed === false)
  const groups: Group[] = [
    { label: '查看', items: [
      { label: '服务器资料', type: 'about', icon: Info },
      { label: '负载与延迟', type: 'resource-details', icon: Activity },
      { label: '任务记录', type: 'tasks', icon: ClipboardList },
    ] },
    { label: '配置与运维', items: [
      { label: '服务器设置', type: 'edit', icon: SlidersHorizontal, admin: true },
      { label: '远程终端', type: 'terminal', icon: SquareTerminal, admin: true, disabled: terminalBlocked },
      { label: '网络工具', type: 'network', icon: Network },
      { label: '拨测任务', type: 'return-latency', icon: Gauge, admin: true },
    ] },
    { label: 'Agent', items: [
      { label: 'Agent 命令', type: 'enroll', icon: Terminal, admin: true, hint: enrolled ? '安装 / 更新 / 卸载' : '获取接入命令' },
      ...(enrolled ? [
        { label: '更新 Agent', type: 'update-agent', icon: RefreshCw, admin: true } satisfies Item,
        { label: 'Agent 维护与日志', type: 'agent-maintenance', icon: Settings2, admin: true } satisfies Item,
      ] : []),
      ...(deliveryPending ? [{ label: '重试授权下发', type: 'retry-delivery', icon: RefreshCw, admin: true } satisfies Item] : []),
    ] },
    { label: '危险操作', items: [
      { label: '删除服务器', type: 'delete', icon: Trash2, danger: true, admin: true },
    ] },
  ]
  return groups
    .map(group => ({ ...group, items: group.items.filter(item => !item.admin || hasManagementAccess(role)) }))
    .filter(group => group.items.length > 0)
}

function useSheetLayout(open: boolean) {
  const [sheet, setSheet] = useState(false)
  useLayoutEffect(() => {
    if (!open) return
    const query = window.matchMedia?.(SHEET_QUERY)
    setSheet(Boolean(query?.matches))
    if (!query?.addEventListener) return
    const change = () => setSheet(query.matches)
    query.addEventListener('change', change)
    return () => query.removeEventListener('change', change)
  }, [open])
  return sheet
}

export function ServerActionMenu({ server, role = 'viewer', onAction }: { server: Server; role?: Role; onAction: (type: string, server: Server) => void }) {
  const [open, setOpen] = useState(false)
  const menuID = useId()
  const initialFocus = useRef<'first' | 'last'>('first')
  const buttonRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const [position, setPosition] = useState({ top: 0, left: 0 })
  const sheet = useSheetLayout(open)
  const groups = serverActionGroups(server, role)
  const name = server.name || `服务器 #${server.id}`

  const place = () => {
    const button = buttonRef.current
    if (!button || sheet) return
    const rect = button.getBoundingClientRect()
    const height = menuRef.current?.offsetHeight || Math.min(420, window.innerHeight - 16)
    const below = window.innerHeight - rect.bottom - 14
    const above = rect.top - 14
    const openBelow = below >= height || below >= above
    setPosition({
      left: Math.max(8, Math.min(window.innerWidth - MENU_WIDTH - 8, rect.right - MENU_WIDTH)),
      top: openBelow ? Math.max(8, Math.min(rect.bottom + 6, window.innerHeight - height - 8)) : Math.max(8, rect.top - height - 6),
    })
  }

  const close = (restoreFocus = true) => {
    setOpen(false)
    if (restoreFocus) buttonRef.current?.focus()
  }

  useEffect(() => {
    if (!open) return
    place()
    const frame = window.requestAnimationFrame(() => {
      place()
      const items = menuRef.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')
      if (items?.length) items[initialFocus.current === 'last' ? items.length - 1 : 0].focus()
    })
    const outside = (event: MouseEvent) => {
      const target = event.target as Node | null
      if (target && !buttonRef.current?.contains(target) && !menuRef.current?.contains(target)) setOpen(false)
    }
    const escape = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && (menuRef.current?.contains(event.target as Node) || event.target === buttonRef.current)) {
        event.preventDefault()
        event.stopPropagation()
        close()
      }
    }
    const reposition = () => place()
    window.addEventListener('resize', reposition)
    window.addEventListener('scroll', reposition, true)
    document.addEventListener('mousedown', outside)
    document.addEventListener('keydown', escape)
    return () => {
      window.cancelAnimationFrame(frame)
      window.removeEventListener('resize', reposition)
      window.removeEventListener('scroll', reposition, true)
      document.removeEventListener('mousedown', outside)
      document.removeEventListener('keydown', escape)
    }
  }, [open, sheet])

  const onMenuKeyDown = (event: React.KeyboardEvent) => {
    if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      close()
      return
    }
    if (event.key === 'Tab') {
      close()
      return
    }
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
    event.preventDefault()
    const items = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)') || [])
    const index = items.findIndex(item => item === document.activeElement)
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length
    items[next]?.focus()
  }

  const menu = (
    <div
      ref={menuRef}
      className={`fleet-menu action-menu-portal${sheet ? ' is-sheet' : ''}`}
      role="menu"
      id={menuID}
      aria-label={`${name}操作`}
      onKeyDown={onMenuKeyDown}
      onClick={event => event.stopPropagation()}
      style={sheet ? undefined : { position: 'fixed', top: position.top, left: position.left, width: MENU_WIDTH }}
    >
      {sheet ? <div className="fleet-menu-sheet-head">
        <RegionFlag code={serverRegionCode(server)} size={20} />
        <strong>{name}</strong>
        <button type="button" className="ghost icon-button" onClick={() => close()} aria-label="关闭操作菜单"><X size={16} aria-hidden="true" /></button>
      </div> : null}
      {groups.map(group => (
        <div key={group.label} className={`fleet-menu-group${group.items.some(item => item.danger) ? ' is-danger' : ''}`} role="group" aria-label={group.label}>
          <span className="fleet-menu-label" aria-hidden="true">{group.label}</span>
          {group.items.map(item => {
            const Icon = item.icon
            return (
              <button
                key={item.type}
                type="button"
                role="menuitem"
                disabled={Boolean(item.disabled)}
                title={item.disabled || undefined}
                className={`fleet-menu-item${item.danger ? ' is-danger' : ''}`}
                onClick={event => {
                  event.stopPropagation()
                  if (item.disabled) return
                  close()
                  onAction(item.type, server)
                }}
              >
                <Icon size={16} aria-hidden="true" />
                <span className="fleet-menu-text">{item.label}</span>
                {item.disabled || item.hint ? <small className="fleet-menu-hint" aria-hidden="true">{item.disabled || item.hint}</small> : null}
              </button>
            )
          })}
        </div>
      ))}
    </div>
  )

  return (
    <div className={`fleet-menu-anchor${open ? ' is-open' : ''}`}>
      <button
        ref={buttonRef}
        type="button"
        className="ghost icon-button fleet-menu-trigger"
        title="服务器操作"
        aria-label="打开服务器操作菜单"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuID : undefined}
        onClick={event => {
          event.stopPropagation()
          if (!open) initialFocus.current = 'first'
          setOpen(value => !value)
        }}
        onKeyDown={event => {
          if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
          event.preventDefault()
          event.stopPropagation()
          initialFocus.current = event.key === 'ArrowUp' ? 'last' : 'first'
          setOpen(true)
        }}
      >
        <MoreHorizontal size={18} aria-hidden="true" />
      </button>
      {open && createPortal(sheet
        ? <div className="fleet-menu-backdrop" onMouseDown={event => { if (event.target === event.currentTarget) close() }}>{menu}</div>
        : menu, document.body)}
    </div>
  )
}
