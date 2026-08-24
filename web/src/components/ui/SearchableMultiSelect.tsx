import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { Check, ChevronDown, Search } from 'lucide-react'

export type SearchableMultiSelectOption = {
  value: string
  label: string
  keywords?: string
}

type SearchableMultiSelectProps = {
  value: string[]
  onChange: (value: string[]) => void
  options: SearchableMultiSelectOption[]
  placeholder: string
  searchPlaceholder?: string
  ariaLabel?: string
  className?: string
}

export function SearchableMultiSelect({
  value,
  onChange,
  options,
  placeholder,
  searchPlaceholder = '搜索选项',
  ariaLabel,
  className = '',
}: SearchableMultiSelectProps) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const rootRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const listID = useId()
  const selected = useMemo(() => new Set(value), [value])
  const visibleOptions = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase()
    if (!normalized) return options
    return options.filter(option => `${option.label} ${option.keywords || ''}`.toLocaleLowerCase().includes(normalized))
  }, [options, query])
  const selectedLabels = options.filter(option => selected.has(option.value)).map(option => option.label)
  const displayValue = selectedLabels.length === 0
    ? placeholder
    : selectedLabels.length <= 2
      ? selectedLabels.join('、')
      : `已选 ${selectedLabels.length} 项`

  useEffect(() => {
    if (!open) return
    const close = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false)
    }
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', close)
    document.addEventListener('keydown', closeOnEscape)
    searchRef.current?.focus()
    return () => {
      document.removeEventListener('mousedown', close)
      document.removeEventListener('keydown', closeOnEscape)
    }
  }, [open])

  const toggle = (optionValue: string) => {
    onChange(selected.has(optionValue) ? value.filter(item => item !== optionValue) : [...value, optionValue])
  }

  return <div ref={rootRef} className={`searchable-multi-select${open ? ' open' : ''}${className ? ` ${className}` : ''}`}>
    <button
      type="button"
      className="searchable-multi-select-trigger"
      role="combobox"
      aria-label={ariaLabel || placeholder}
      aria-expanded={open}
      aria-controls={listID}
      onClick={() => setOpen(current => !current)}
    >
      <span className={selectedLabels.length ? '' : 'placeholder'}>{displayValue}</span>
      <ChevronDown size={15} aria-hidden="true" />
    </button>
    {open && <div id={listID} data-popover="true" className="searchable-multi-select-menu" role="listbox" aria-multiselectable="true">
      <label className="searchable-multi-select-search">
        <Search size={14} aria-hidden="true" />
        <input ref={searchRef} value={query} onChange={event => setQuery(event.target.value)} placeholder={searchPlaceholder} />
      </label>
      <div className="searchable-multi-select-options">
        {visibleOptions.length ? visibleOptions.map(option => {
          const isSelected = selected.has(option.value)
          return <button
            key={option.value}
            type="button"
            role="option"
            aria-selected={isSelected}
            className={isSelected ? 'selected' : ''}
            onClick={() => toggle(option.value)}
          >
            <span>{option.label}</span>
            <span className="searchable-multi-select-check" aria-hidden="true">{isSelected && <Check size={13} />}</span>
          </button>
        }) : <span className="searchable-multi-select-empty">没有匹配项</span>}
      </div>
      {value.length > 0 && <button type="button" className="searchable-multi-select-clear" onClick={() => onChange([])}>清除已选</button>}
    </div>}
  </div>
}
