import * as React from 'react'

export interface SwitchProps extends Omit<React.InputHTMLAttributes<HTMLInputElement>, 'onChange' | 'size'> {
  checked?: boolean
  defaultChecked?: boolean
  onChange?: (checked: boolean) => void
  disabled?: boolean
  size?: 'sm' | 'md' | 'lg'
  ariaLabel?: string
  className?: string
}

export const Switch = React.forwardRef<HTMLInputElement, SwitchProps>(({
  checked,
  defaultChecked,
  onChange,
  disabled = false,
  size = 'md',
  ariaLabel,
  className = '',
  ...props
}, ref) => {
  const [internalChecked, setInternalChecked] = React.useState(defaultChecked ?? false)
  const isControlled = checked !== undefined
  const isChecked = isControlled ? Boolean(checked) : internalChecked

  const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    if (disabled) return
    const next = e.target.checked
    if (!isControlled) {
      setInternalChecked(next)
    }
    onChange?.(next)
  }

  return (
    <label
      className={`ui-switch ui-switch-${size}${isChecked ? ' is-checked' : ''}${disabled ? ' is-disabled' : ''}${className ? ` ${className}` : ''}`}
      onClick={e => e.stopPropagation()}
    >
      <input
        type="checkbox"
        ref={ref}
        checked={isChecked}
        disabled={disabled}
        onChange={handleChange}
        aria-label={ariaLabel}
        aria-checked={isChecked}
        role="switch"
        className="ui-switch-input"
        {...props}
      />
      <span className="ui-switch-track" aria-hidden="true">
        <span className="ui-switch-thumb" />
      </span>
    </label>
  )
})

Switch.displayName = 'Switch'
