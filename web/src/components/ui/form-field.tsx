import * as React from 'react'
import { useState } from 'react'
import { HelpCircle } from 'lucide-react'
import { Select } from './select'

export function FormField({ label, hint, required, children, className = '', full = false }: { label: string; hint?: string; required?: boolean; children: React.ReactNode; className?: string; full?: boolean }) {
  const [hintOpen, setHintOpen] = useState(false)
  const hintID = React.useId()
  const useHintPopover = Boolean(hint && hint.length > 16)
  return (
    <label className={`form-field${full ? ' form-field-full' : ''}${className ? ` ${className}` : ''}`.trim()}>
      <div className="form-field-meta">
        <span className="form-field-label">
          {label}
          {required ? <em aria-label="必填">*</em> : null}
          {useHintPopover ? (
            <button
              type="button"
              className="form-field-help"
              aria-label={`${label}说明`}
              aria-describedby={hintID}
              aria-expanded={hintOpen}
              onClick={event => {
                event.preventDefault()
                event.stopPropagation()
                setHintOpen(open => !open)
              }}
              onBlur={() => setHintOpen(false)}
            >
              <HelpCircle size={14} aria-hidden="true" />
              <span id={hintID} role="tooltip" className="form-field-help-popover" data-open={hintOpen || undefined}>{hint}</span>
            </button>
          ) : null}
        </span>
        {hint && !useHintPopover ? <small className="form-field-hint">{hint}</small> : null}
      </div>
      <div className="form-field-control">{children}</div>
    </label>
  )
}

type TrafficDisplayUnit = 'GB' | 'TB'

export function TrafficLimitInput({ bytes, onChange }: { bytes: number; onChange: (bytes: number) => void }) {
  const [unit, setUnit] = useState<TrafficDisplayUnit>(() => bytes >= 1024 ** 4 ? 'TB' : 'GB')
  const multiplier = unit === 'TB' ? 1024 ** 4 : 1024 ** 3
  const displayValue = bytes > 0 ? Number((bytes / multiplier).toFixed(3)) : ''
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
    <Select variant="segmented" value={unit} onChange={e => setUnit(e.target.value as TrafficDisplayUnit)} aria-label="流量额度单位"><option value="GB">GB</option><option value="TB">TB</option></Select>
  </div>
}
