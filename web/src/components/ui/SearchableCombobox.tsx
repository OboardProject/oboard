import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { Check, ChevronDown } from 'lucide-react'

type SearchableComboboxProps = {
  value: string
  onChange: (value: string) => void
  options: string[]
  placeholder?: string
  ariaLabel: string
  required?: boolean
}

export function SearchableCombobox({ value, onChange, options, placeholder, ariaLabel, required = false }: SearchableComboboxProps) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [highlightedIndex, setHighlightedIndex] = useState(-1)
  const rootRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const listID = useId()
  const visibleOptions = useMemo(() => {
    const normalized = query.trim().toLocaleLowerCase()
    if (!normalized) return options
    return options.filter(option => option.toLocaleLowerCase().includes(normalized))
  }, [options, query])

  useEffect(() => {
    if (!open || visibleOptions.length === 0) {
      setHighlightedIndex(-1)
      return
    }
    const selectedIndex = visibleOptions.indexOf(value)
    setHighlightedIndex(selectedIndex >= 0 ? selectedIndex : 0)
  }, [open, value, visibleOptions])

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
    return () => {
      document.removeEventListener('mousedown', close)
      document.removeEventListener('keydown', closeOnEscape)
    }
  }, [open])

  const showOptions = open && options.length > 0
  return <div ref={rootRef} className={`searchable-combobox${showOptions ? ' open' : ''}`}>
    <input
      ref={inputRef}
      role="combobox"
      aria-label={ariaLabel}
      aria-autocomplete="list"
      aria-expanded={showOptions}
      aria-controls={showOptions ? listID : undefined}
      aria-activedescendant={showOptions && highlightedIndex >= 0 ? `${listID}-option-${highlightedIndex}` : undefined}
      required={required}
      value={value}
      placeholder={placeholder}
      onFocus={() => {
        setQuery('')
        if (options.length) setOpen(true)
      }}
      onChange={event => {
        onChange(event.target.value)
        setQuery(event.target.value)
        if (options.length) setOpen(true)
      }}
      onKeyDown={event => {
        if (!visibleOptions.length) return
        if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
          event.preventDefault()
          setOpen(true)
          const direction = event.key === 'ArrowDown' ? 1 : -1
          setHighlightedIndex(current => current < 0 ? 0 : (current + direction + visibleOptions.length) % visibleOptions.length)
        } else if (event.key === 'Enter' && showOptions && highlightedIndex >= 0) {
          event.preventDefault()
          onChange(visibleOptions[highlightedIndex])
          setQuery('')
          setOpen(false)
        }
      }}
    />
    <button
      type="button"
      aria-label="展开模型列表"
      aria-expanded={showOptions}
      disabled={!options.length}
      onClick={() => {
        setQuery('')
        setOpen(current => !current)
        inputRef.current?.focus()
      }}
    >
      <ChevronDown size={15} aria-hidden="true" />
    </button>
    {showOptions && <div id={listID} data-popover="true" className="searchable-combobox-menu" role="listbox">
      {visibleOptions.length ? visibleOptions.map((option, index) => <button
        key={option}
        id={`${listID}-option-${index}`}
        type="button"
        role="option"
        aria-selected={option === value}
        className={`${option === value ? 'selected' : ''}${index === highlightedIndex ? ' highlighted' : ''}`.trim()}
        onMouseDown={event => event.preventDefault()}
        onMouseEnter={() => setHighlightedIndex(index)}
        onClick={() => {
          onChange(option)
          setQuery('')
          setOpen(false)
          inputRef.current?.focus()
        }}
      >
        <span>{option}</span>
        {option === value && <Check size={14} aria-hidden="true" />}
      </button>) : <span className="searchable-combobox-empty">没有匹配的模型</span>}
    </div>}
  </div>
}
