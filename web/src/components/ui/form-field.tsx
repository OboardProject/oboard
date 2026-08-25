import * as React from 'react'
import { useState } from 'react'
import { HelpCircle } from 'lucide-react'
import { Select } from './select'

type HelpPlacement = 'top' | 'bottom'

export function FieldHelp({ label, hint, placement = 'top' }: { label: string; hint: string; placement?: HelpPlacement }) {
  const hintID = React.useId()
  const stopToggle = (event: React.SyntheticEvent) => {
    event.stopPropagation()
  }
  return (
    <button
      type="button"
      className="form-field-help"
      aria-label={`${label}说明`}
      aria-describedby={hintID}
      tabIndex={0}
      onClick={event => {
        event.preventDefault()
        event.stopPropagation()
      }}
      onPointerDown={stopToggle}
      onKeyDown={event => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault()
          event.stopPropagation()
        }
      }}
    >
      <HelpCircle size={14} aria-hidden="true" />
      <span id={hintID} role="tooltip" className={`form-field-help-popover${placement === 'bottom' ? ' popover-bottom' : ''}`}>{hint}</span>
    </button>
  )
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
