import React, { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { MoreHorizontal } from 'lucide-react'

type IconComponent = React.ComponentType<{ size?: number; className?: string; 'aria-hidden'?: boolean | 'true' | 'false' }>

export type OverflowMenuItem = {
  key: string
  label: string
  icon?: IconComponent
  disabled?: boolean
  danger?: boolean
  title?: string
  onSelect: () => void
}

export type OverflowMenuGroup = {
  key: string
  items: OverflowMenuItem[]
}

// Row-level overflow menu. Rows that would otherwise line up four or five icon
// buttons keep one trigger and move the rest behind it, so a list of records
// stays readable as it grows.
export function OverflowMenu({
  groups,
  label = '更多操作',
  width = 192,
  triggerClassName = 'ghost icon-button',
}: {
  groups: OverflowMenuGroup[]
  label?: string
  width?: number
  triggerClassName?: string
}) {
  const [isOpen, setIsOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)
  const buttonRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const [position, setPosition] = useState({ top: 0, left: 0 })

  const visibleGroups = groups.filter(group => group.items.length > 0)
  const totalItems = visibleGroups.reduce((count, group) => count + group.items.length, 0)

  const placeMenu = () => {
    const button = buttonRef.current
    if (!button) return
    const rect = button.getBoundingClientRect()
    const estimatedHeight = Math.min(totalItems * 36 + visibleGroups.length * 12 + 16, window.innerHeight - 16)
    const height = menuRef.current?.offsetHeight || estimatedHeight
    const roomBelow = window.innerHeight - rect.bottom - 14
    const roomAbove = rect.top - 14
    const openBelow = roomBelow >= height || roomBelow >= roomAbove
    setPosition({
      top: openBelow
        ? Math.min(rect.bottom + 6, Math.max(8, window.innerHeight - height - 8))
        : Math.max(8, rect.top - height - 6),
      left: Math.max(8, Math.min(window.innerWidth - width - 8, rect.right - width)),
    })
  }

  useEffect(() => {
    if (!isOpen) return
    placeMenu()
    const frame = window.requestAnimationFrame(placeMenu)
    const handlePointerDown = (event: MouseEvent) => {
      const target = event.target as HTMLElement | null
      if (target && !rootRef.current?.contains(target) && !menuRef.current?.contains(target)) setIsOpen(false)
    }
    const handleEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.stopPropagation()
      setIsOpen(false)
      buttonRef.current?.focus()
    }
    window.addEventListener('resize', placeMenu)
    window.addEventListener('scroll', placeMenu, true)
    document.addEventListener('mousedown', handlePointerDown)
    document.addEventListener('keydown', handleEscape)
    return () => {
      window.cancelAnimationFrame(frame)
      window.removeEventListener('resize', placeMenu)
      window.removeEventListener('scroll', placeMenu, true)
      document.removeEventListener('mousedown', handlePointerDown)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [isOpen, totalItems])

  if (!totalItems) return null

  return <div ref={rootRef} className={isOpen ? 'server-actions-dropdown is-open' : 'server-actions-dropdown'}>
    <button
      ref={buttonRef}
      type="button"
      className={triggerClassName}
      onClick={event => { event.stopPropagation(); if (!isOpen) placeMenu(); setIsOpen(!isOpen) }}
      title={label}
      aria-label={label}
      aria-haspopup="menu"
      aria-expanded={isOpen}
    >
      <MoreHorizontal size={15} aria-hidden="true" />
    </button>
    {isOpen && createPortal(
      <div
        ref={menuRef}
        className="server-actions-menu action-menu-portal server-actions-menu-v2"
        role="menu"
        style={{ position: 'fixed', top: position.top, left: position.left, width }}
      >
        {visibleGroups.map((group, index) => <React.Fragment key={group.key}>
          {index > 0 && <div className="server-actions-divider" role="separator" />}
          <div className="server-actions-section">
            {group.items.map(item => {
              const Icon = item.icon
              return <button
                key={item.key}
                type="button"
                role="menuitem"
                disabled={item.disabled}
                title={item.title || item.label}
                className={item.danger ? 'danger' : item.disabled ? 'disabled' : ''}
                onClick={event => {
                  event.stopPropagation()
                  if (item.disabled) return
                  setIsOpen(false)
                  item.onSelect()
                }}
              >
                {Icon && <span className="server-action-icon"><Icon size={14} aria-hidden="true" /></span>}
                <span className="server-action-label">{item.label}</span>
              </button>
            })}
          </div>
        </React.Fragment>)}
      </div>,
      document.body,
    )}
  </div>
}
