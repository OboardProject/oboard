import * as React from 'react'
import { CustomSelect, type CustomSelectOption } from './CustomSelect'

type NativeSelectProps = Omit<
  React.SelectHTMLAttributes<HTMLSelectElement>,
  'children' | 'defaultValue' | 'multiple' | 'onChange' | 'ref' | 'size' | 'value'
>

export interface SelectProps extends NativeSelectProps {
  children: React.ReactNode
  value?: string | number
  onChange?: React.ChangeEventHandler<HTMLSelectElement>
  variant?: 'menu' | 'segmented'
  placeholder?: string
}

type ParsedOption = CustomSelectOption & { key: React.Key }

function parseOptions(children: React.ReactNode): ParsedOption[] {
  return React.Children.toArray(children).flatMap((child, index) => {
    if (!React.isValidElement<React.OptionHTMLAttributes<HTMLOptionElement>>(child) || child.type !== 'option') return []
    return [{
      key: child.key ?? index,
      value: String(child.props.value ?? ''),
      label: child.props.children,
      disabled: child.props.disabled,
    }]
  })
}

function syntheticSelectEvent(value: string, name?: string): React.ChangeEvent<HTMLSelectElement> {
  const target = { value, name: name || '' } as HTMLSelectElement
  return { target, currentTarget: target } as React.ChangeEvent<HTMLSelectElement>
}

export const Select = React.forwardRef<HTMLButtonElement, SelectProps>(({
  children,
  className = '',
  disabled = false,
  id,
  name,
  onChange,
  placeholder,
  required = false,
  style,
  value = '',
  variant = 'menu',
  ...ariaProps
}, ref) => {
  const options = parseOptions(children)
  const stringValue = String(value ?? '')
  const selectedIndex = Math.max(0, options.findIndex(option => option.value === stringValue))
  const emitChange = (nextValue: string) => onChange?.(syntheticSelectEvent(nextValue, name))
  const ariaLabel = ariaProps['aria-label']

  if (variant === 'segmented') {
    return (
      <div
        className={`ui-segmented-select${disabled ? ' disabled' : ''}${className ? ` ${className}` : ''}`}
        style={{
          gridTemplateColumns: `repeat(${options.length}, minmax(0, 1fr))`,
          ...style,
        }}
        role="radiogroup"
        aria-label={ariaLabel}
        aria-required={required || undefined}
      >
        <span
          className="ui-segmented-indicator"
          style={{ transform: `translateX(calc(${selectedIndex} * (100% + 4px)))` }}
          aria-hidden="true"
        />
        {options.map((option, index) => {
          const selected = option.value === stringValue
          return (
            <button
              key={option.key}
              ref={selected ? ref : undefined}
              id={selected ? id : undefined}
              type="button"
              role="radio"
              aria-checked={selected}
              className={selected ? 'active' : ''}
              style={{ gridColumn: index + 1, gridRow: 1 }}
              disabled={disabled || option.disabled}
              onClick={() => emitChange(option.value)}
            >
              {option.label}
            </button>
          )
        })}
      </div>
    )
  }

  return (
    <CustomSelect
      value={stringValue}
      onChange={emitChange}
      options={options}
      className={className}
      style={style}
      disabled={disabled}
      placeholder={placeholder}
      id={id}
      ariaLabel={ariaLabel}
      required={required}
    />
  )
})
Select.displayName = 'Select'
