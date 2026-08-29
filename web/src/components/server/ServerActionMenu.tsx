import React, { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Info, SlidersHorizontal, SquareTerminal, Network, Settings2, ClipboardList, Trash2, Terminal, RefreshCw, MoreVertical } from 'lucide-react'
import type { Server } from '../proxy-path/types'

type Role = 'admin' | 'operator' | 'viewer' | 'none'

function hasManagementAccess(role: Role) {
  return role === 'admin' || role === 'operator'
}

type Item = {
  label: string
  type: string
  icon: React.ComponentType<{ size?: number; className?: string; 'aria-hidden'?: boolean | 'true' | 'false' }>
  admin?: boolean
  danger?: boolean
}

export function ServerActionMenu({ server, role = 'viewer', onAction }: { server: Server; role?: Role; onAction: (type: string, server: Server) => void }) {
  const [isOpen, setIsOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  const buttonRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const [menuPosition, setMenuPosition] = useState({ top: 0, left: 0 })

  const enrolled = Boolean(String(server.agent_id || '').trim())
  const isOnline = String(server.status || '').toLowerCase() === 'online'

  const groups: Array<{ items: Item[]; dividerBefore?: boolean }> = [
    {
      items: [
        { label: '关于', type: 'about', icon: Info },
        { label: '基础设置', type: 'basic-settings', icon: SlidersHorizontal },
      ],
    },
    {
      dividerBefore: true,
      items: [
        { label: '接入命令', type: 'enroll', icon: Terminal, admin: true },
        ...(enrolled ? [{ label: '更新 Agent', type: 'update-agent', icon: RefreshCw, admin: true } satisfies Item] : []),
      ],
    },
    {
      dividerBefore: true,
      items: [
        { label: '远程终端', type: 'terminal', icon: SquareTerminal, admin: true },
        { label: '网络', type: 'network', icon: Network },
        { label: '系统', type: 'system', icon: Settings2 },
        { label: '任务记录', type: 'tasks', icon: ClipboardList },
      ],
    },
    {
      dividerBefore: true,
      items: [
        { label: '删除服务器', type: 'delete', icon: Trash2, danger: true },
      ],
    },
  ]

  const visibleGroups = groups
    .map(g => ({ ...g, items: g.items.filter(item => !item.admin || hasManagementAccess(role)) }))
    .filter(g => g.items.length > 0)

  const totalVisibleItems = visibleGroups.reduce((acc, g) => acc + g.items.length, 0)

  const updateMenuPosition = () => {
    const button = buttonRef.current
    if (!button) return
    const rect = button.getBoundingClientRect()
    const width = 192
    const estimatedHeight = Math.min(totalVisibleItems * 36 + visibleGroups.length * 12 + 16, window.innerHeight - 16)
    const height = menuRef.current?.offsetHeight || estimatedHeight
    const roomBelow = window.innerHeight - rect.bottom - 8 - 6
    const roomAbove = rect.top - 8 - 6
    const openBelow = roomBelow >= height || roomBelow >= roomAbove
    const left = Math.max(8, Math.min(window.innerWidth - width - 8, rect.right - width))
    const top = openBelow
      ? Math.min(rect.bottom + 6, window.innerHeight - height - 8)
      : Math.max(8, rect.top - height - 6)
    setMenuPosition({ top, left })
  }

  useEffect(() => {
    if (!isOpen) return
    updateMenuPosition()
    const frame = window.requestAnimationFrame(updateMenuPosition)
    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as HTMLElement | null
      if (target && ref.current && !ref.current.contains(target) && !menuRef.current?.contains(target)) {
        setIsOpen(false)
      }
    }
    const handleEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setIsOpen(false)
        buttonRef.current?.focus()
      }
    }
    window.addEventListener('resize', updateMenuPosition)
    window.addEventListener('scroll', updateMenuPosition, true)
    document.addEventListener('mousedown', handleClickOutside)
    document.addEventListener('keydown', handleEscape)
    return () => {
      window.cancelAnimationFrame(frame)
      window.removeEventListener('resize', updateMenuPosition)
      window.removeEventListener('scroll', updateMenuPosition, true)
      document.removeEventListener('mousedown', handleClickOutside)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [isOpen, totalVisibleItems])

  return (
    <div ref={ref} className={isOpen ? 'server-actions-dropdown is-open' : 'server-actions-dropdown'}>
      <button
        ref={buttonRef}
        type="button"
        onClick={(e) => {
          e.stopPropagation()
          if (!isOpen) updateMenuPosition()
          setIsOpen(!isOpen)
        }}
        className="ghost icon-button"
        style={{
          width: '28px',
          height: '28px',
          borderRadius: '50%',
          border: '1px solid var(--border-color)',
          display: 'grid',
          placeContent: 'center',
          cursor: 'pointer',
          backgroundColor: isOpen ? 'var(--bg-control)' : 'var(--bg-card)',
          color: 'var(--text-primary)',
          transition: 'all 0.15s',
        }}
        title="服务器操作"
        aria-label="打开服务器操作菜单"
        aria-haspopup="menu"
        aria-expanded={isOpen}
      >
        <MoreVertical size={16} aria-hidden="true" />
      </button>
      {isOpen && createPortal(
        <div
          ref={menuRef}
          className="server-actions-menu action-menu-portal server-actions-menu-v2"
          role="menu"
          style={{
            position: 'fixed',
            top: menuPosition.top,
            left: menuPosition.left,
            width: 192,
          }}
        >
          {visibleGroups.map((group, groupIdx) => (
            <React.Fragment key={groupIdx}>
              {group.dividerBefore && <div className="server-actions-divider" role="separator" />}
              <div className="server-actions-section">
                {group.items.map((item) => {
                  const Icon = item.icon
                  const disabled = item.type === 'terminal' && (!enrolled || !isOnline)
                  return (
                    <button
                      key={item.type}
                      type="button"
                      role="menuitem"
                      disabled={disabled}
                      title={disabled ? 'Agent 当前离线' : item.label}
                      onClick={(e) => {
                        e.stopPropagation()
                        if (disabled) return
                        onAction(item.type, server)
                        setIsOpen(false)
                      }}
                      className={item.danger ? 'danger' : disabled ? 'disabled' : ''}
                    >
                      <span className="server-action-icon"><Icon size={14} aria-hidden="true" /></span>
                      <span className="server-action-label">{item.label}</span>
                    </button>
                  )
                })}
              </div>
            </React.Fragment>
          ))}
        </div>,
        document.body
      )}
    </div>
  )
}
