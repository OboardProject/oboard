import React, { useCallback, useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ChevronDown, Check } from 'lucide-react'

export interface CustomSelectOption {
  value: string
  label: React.ReactNode
  disabled?: boolean
}

export interface CustomSelectProps {
  value: string
  onChange: (val: string) => void
  options: CustomSelectOption[]
  className?: string
  style?: React.CSSProperties
  disabled?: boolean
  placeholder?: string
  id?: string
  ariaLabel?: string
  ariaDescribedBy?: string
  required?: boolean
  selectedLabel?: React.ReactNode
  menuHeader?: React.ReactNode
  emptyMessage?: React.ReactNode
}

type MenuPosition = {
  top: number
  left: number
  width: number
  maxHeight: number
  placement: 'top' | 'bottom'
}

export const CustomSelect: React.FC<CustomSelectProps> = ({
  value,
  onChange,
  options,
  className = '',
  style,
  disabled = false,
  placeholder = '请选择',
  id,
  ariaLabel,
  ariaDescribedBy,
  required = false,
  selectedLabel,
  menuHeader,
  emptyMessage = '没有匹配的选项',
}) => {
  const [isOpen, setIsOpen] = useState(false)
  const [highlightedIndex, setHighlightedIndex] = useState(-1)
  const [menuPosition, setMenuPosition] = useState<MenuPosition | null>(null)
  const rootRef = useRef<HTMLDivElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const listId = useId()
  const activeOption = options.find(o => o.value === value)
  const selectedIndex = options.findIndex(option => option.value === value)

  const updateMenuPosition = useCallback(() => {
    const trigger = rootRef.current?.querySelector<HTMLButtonElement>('.custom-select-trigger')
    if (!trigger) return
    const rect = trigger.getBoundingClientRect()
    const gutter = 8
    const viewportPadding = 10
    const below = window.innerHeight - rect.bottom - viewportPadding
    const above = rect.top - viewportPadding
    const desiredHeight = Math.min(menuHeader ? 420 : 280, Math.max(42, options.length * 40 + (menuHeader ? 112 : 2)))
    const placement: MenuPosition['placement'] = below >= Math.min(desiredHeight, 180) || below >= above ? 'bottom' : 'top'
    const available = Math.max(96, (placement === 'bottom' ? below : above) - gutter)
    const maxHeight = Math.min(desiredHeight, available)
    const width = Math.min(rect.width, window.innerWidth - viewportPadding * 2)
    const left = Math.min(Math.max(viewportPadding, rect.left), window.innerWidth - viewportPadding - width)
    const top = placement === 'bottom'
      ? rect.bottom + gutter
      : Math.max(viewportPadding, rect.top - gutter - maxHeight)
    setMenuPosition({ top, left, width, maxHeight, placement })
  }, [menuHeader, options.length])

  const openMenu = useCallback((preferredIndex = selectedIndex) => {
    if (disabled || (options.length === 0 && !menuHeader)) return
    const nextIndex = preferredIndex >= 0 && !options[preferredIndex]?.disabled
      ? preferredIndex
      : options.findIndex(option => !option.disabled)
    setHighlightedIndex(nextIndex)
    setIsOpen(true)
  }, [disabled, menuHeader, options, selectedIndex])

  const moveHighlight = useCallback((direction: 1 | -1) => {
    if (!options.length) return
    let next = highlightedIndex
    for (let attempt = 0; attempt < options.length; attempt += 1) {
      next = (next + direction + options.length) % options.length
      if (!options[next]?.disabled) {
        setHighlightedIndex(next)
        return
      }
    }
  }, [highlightedIndex, options])

  const selectHighlighted = useCallback(() => {
    const option = options[highlightedIndex]
    if (!option || option.disabled) return
    onChange(option.value)
    setIsOpen(false)
  }, [highlightedIndex, onChange, options])

  useEffect(() => {
    if (!isOpen) return
    const onPointerDown = (event: MouseEvent) => {
      const target = event.target as Node
      if (!rootRef.current?.contains(target) && !menuRef.current?.contains(target)) setIsOpen(false)
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setIsOpen(false)
    }
    document.addEventListener('mousedown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('mousedown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [isOpen])

  useEffect(() => {
    if (!isOpen) return
    updateMenuPosition()
    const reposition = () => updateMenuPosition()
    const scrollParents: HTMLElement[] = []
    let parent: HTMLElement | null = rootRef.current?.parentElement ?? null
    while (parent) {
      scrollParents.push(parent)
      parent = parent.parentElement
    }
    window.addEventListener('resize', reposition)
    window.addEventListener('scroll', reposition, true)
    document.addEventListener('scroll', reposition, true)
    scrollParents.forEach(element => element.addEventListener('scroll', reposition))
    return () => {
      window.removeEventListener('resize', reposition)
      window.removeEventListener('scroll', reposition, true)
      document.removeEventListener('scroll', reposition, true)
      scrollParents.forEach(element => element.removeEventListener('scroll', reposition))
    }
  }, [isOpen, updateMenuPosition])

  useEffect(() => {
    if (!isOpen || highlightedIndex < 0) return
    menuRef.current
      ?.querySelector<HTMLElement>(`[data-option-index="${highlightedIndex}"]`)
      ?.scrollIntoView({ block: 'nearest' })
  }, [highlightedIndex, isOpen])

  useEffect(() => {
    if (!isOpen) return
    if (highlightedIndex >= 0 && options[highlightedIndex] && !options[highlightedIndex].disabled) return
    const nextIndex = selectedIndex >= 0 && !options[selectedIndex]?.disabled
      ? selectedIndex
      : options.findIndex(option => !option.disabled)
    setHighlightedIndex(nextIndex)
  }, [highlightedIndex, isOpen, options, selectedIndex])

  const handleTriggerKeyDown = (event: React.KeyboardEvent<HTMLButtonElement>) => {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      if (!isOpen) openMenu(event.key === 'ArrowDown' ? selectedIndex : Math.max(0, selectedIndex))
      else moveHighlight(event.key === 'ArrowDown' ? 1 : -1)
      return
    }
    if (event.key === 'Home' || event.key === 'End') {
      event.preventDefault()
      if (!isOpen) openMenu()
      const indexes = options.map((_, index) => index).filter(index => !options[index]?.disabled)
      setHighlightedIndex(event.key === 'Home' ? (indexes[0] ?? -1) : (indexes[indexes.length - 1] ?? -1))
      return
    }
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      if (isOpen) selectHighlighted()
      else openMenu()
    }
  }

  return (
    <div
      ref={rootRef}
      className={`custom-select${isOpen ? ' open' : ''}${disabled ? ' disabled' : ''}${className ? ` ${className}` : ''}`.trim()}
      style={style}
    >
      <button
        id={id}
        type="button"
        className="custom-select-trigger"
        role="combobox"
        aria-haspopup="listbox"
        aria-expanded={isOpen}
        aria-controls={listId}
        aria-activedescendant={isOpen && highlightedIndex >= 0 ? `${listId}-option-${highlightedIndex}` : undefined}
        aria-label={ariaLabel}
        aria-describedby={ariaDescribedBy}
        aria-required={required || undefined}
        disabled={disabled}
        onKeyDown={handleTriggerKeyDown}
        onClick={() => {
          if (isOpen) setIsOpen(false)
          else openMenu()
        }}
      >
        <span className={`custom-select-value${activeOption || selectedLabel != null ? '' : ' placeholder'}`}>
          {selectedLabel ?? activeOption?.label ?? placeholder}
        </span>
        <ChevronDown size={14} className="custom-select-chevron" aria-hidden="true" />
      </button>

      {isOpen && menuPosition && createPortal(
        <div
          ref={menuRef}
          data-popover="true"
          className={`custom-select-menu custom-select-menu-portal placement-${menuPosition.placement}`}
          style={{ top: menuPosition.top, left: menuPosition.left, width: menuPosition.width, maxHeight: menuPosition.maxHeight }}
        >
          {menuHeader ? <div className="custom-select-menu-header">{menuHeader}</div> : null}
          <div id={listId} className="custom-select-options" role="listbox">
            {options.length ? options.map((opt, index) => {
              const isSelected = opt.value === value
              const isHighlighted = index === highlightedIndex
              return (
                <button
                  key={opt.value}
                  id={`${listId}-option-${index}`}
                  data-option-index={index}
                  type="button"
                  role="option"
                  aria-selected={isSelected}
                  className={`custom-select-option${isSelected ? ' selected' : ''}${isHighlighted ? ' highlighted' : ''}`}
                  disabled={opt.disabled}
                  onMouseEnter={() => !opt.disabled && setHighlightedIndex(index)}
                  onClick={() => {
                    if (opt.disabled) return
                    onChange(opt.value)
                    setIsOpen(false)
                  }}
                >
                  <span>{opt.label}</span>
                  {isSelected ? <Check size={13} aria-hidden="true" /> : null}
                </button>
              )
            }) : <div className="custom-select-empty">{emptyMessage}</div>}
          </div>
        </div>,
        document.body,
      )}
    </div>
  )
}

export default CustomSelect
