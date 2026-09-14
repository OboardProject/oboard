import * as React from 'react'
import { useState } from 'react'
import { createPortal } from 'react-dom'
import { HelpCircle } from 'lucide-react'
import { Select } from './select'

type HelpPlacement = 'top' | 'bottom'

export function FieldHelp({ label, hint, placement = 'top' }: { label: string; hint: React.ReactNode; placement?: HelpPlacement }) {
  const hintID = React.useId()
  const buttonRef = React.useRef<HTMLButtonElement>(null)
  const tooltipRef = React.useRef<HTMLSpanElement>(null)
  const [open, setOpen] = useState(false)
  const [position, setPosition] = useState({ top: 0, left: 0 })
  React.useLayoutEffect(() => {
    if (!open) return
    const button = buttonRef.current?.getBoundingClientRect()
    const tooltip = tooltipRef.current?.getBoundingClientRect()
    if (button && tooltip) {
      const below = button.bottom + 8
      const above = button.top - tooltip.height - 8
      setPosition({
        left: Math.max(12, Math.min(button.left, window.innerWidth - tooltip.width - 12)),
        top: Math.max(12, placement === 'bottom' && below + tooltip.height <= window.innerHeight - 12 ? below : above >= 12 ? above : below),
      })
    }
    const close = () => setOpen(false)
    window.addEventListener('scroll', close, true)
    window.addEventListener('resize', close)
    return () => {
      window.removeEventListener('scroll', close, true)
      window.removeEventListener('resize', close)
    }
  }, [open, placement])
  return <>
    <button
      ref={buttonRef}
      type="button"
      className="form-field-help"
      aria-label={`${label}说明`}
      aria-describedby={open ? hintID : undefined}
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
      onFocus={() => setOpen(true)}
      onBlur={() => setOpen(false)}
      onClick={event => { event.preventDefault(); event.stopPropagation(); setOpen(true) }}
      onPointerDown={event => event.stopPropagation()}
      onKeyDown={event => {
        if (event.key === 'Enter' || event.key === ' ') event.stopPropagation()
        if (event.key === 'Escape' && open) {
          event.stopPropagation()
          setOpen(false)
        }
      }}
    ><HelpCircle size={14} aria-hidden="true" /></button>
    {open && createPortal(<span ref={tooltipRef} id={hintID} role="tooltip" className="form-field-help-popover" style={{ position: 'fixed', top: position.top, left: position.left, bottom: 'auto', opacity: 1, visibility: 'visible', transform: 'none' }}>{hint}</span>, document.body)}
  </>
}

export function FormField({ label, hint, required, children, className = '', full = false, placement = 'top' }: { label: string; hint?: string; required?: boolean; children: React.ReactNode; className?: string; full?: boolean; placement?: HelpPlacement }) {
  return (
    <div className={`form-field${full ? ' form-field-full' : ''}${className ? ` ${className}` : ''}`.trim()}>
      <div className="form-field-meta">
        <label className="form-field-label">
          {label}
          {required ? <em aria-label="必填">*</em> : null}
        </label>
        {hint ? <FieldHelp label={label} hint={hint} placement={placement} /> : null}
      </div>
      <div className="form-field-control">{children}</div>
    </div>
  )
}

type TrafficDisplayUnit = 'GB' | 'TB'

export function TrafficLimitInput({ bytes, onChange }: { bytes: number; onChange: (bytes: number) => void }) {
  const [unit, setUnit] = useState<TrafficDisplayUnit>(() => bytes >= 1024 ** 4 ? 'TB' : 'GB')
  const multiplier = unit === 'TB' ? 1024 ** 4 : 1024 ** 3
  const displayValue = bytes > 0 ? Number((bytes / multiplier).toFixed(3)) : ''
  const handleUnitChange = (nextUnit: TrafficDisplayUnit) => {
    setUnit(nextUnit)
    if (bytes > 0) {
      const num = Number((bytes / multiplier).toFixed(3))
      const nextMultiplier = nextUnit === 'TB' ? 1024 ** 4 : 1024 ** 3
      onChange(Math.round(num * nextMultiplier))
    }
  }
  return <div className="traffic-limit-input">
    <input
      type="number"
      min={0}
      step="any"
      placeholder="0"
      value={displayValue}
      onChange={e => {
        const val = e.target.value
        if (val === '') {
          onChange(0)
        } else {
          onChange(Math.round(Math.max(0, Number(val)) * multiplier))
        }
      }}
    />
    <Select variant="segmented" value={unit} onChange={e => handleUnitChange(e.target.value as TrafficDisplayUnit)} aria-label="流量额度单位"><option value="GB">GB</option><option value="TB">TB</option></Select>
  </div>
}
