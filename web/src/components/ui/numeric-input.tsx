import * as React from 'react'

type NativeInputProps = Omit<React.InputHTMLAttributes<HTMLInputElement>, 'value' | 'onChange' | 'type' | 'inputMode' | 'min' | 'max'>

export interface NumericInputProps extends NativeInputProps {
  /** Committed number; undefined, null, NaN and 0 render as an empty field. */
  value: number | undefined | null
  /** Receives the parsed number while typing, or undefined once the field is empty. */
  onValueChange: (value: number | undefined) => void
  /** Upper bound applied while typing; a longer value is cut to its digit count. */
  max?: number
}

function displayValue(value: number | undefined | null) {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? String(Math.trunc(value)) : ''
}

/**
 * Digits-only text input for ports, bandwidth and similar positive integers.
 *
 * The field owns its text while focused, so an operator can clear every digit
 * or retype the first one without a fallback value snapping back in. Blur
 * re-syncs the text with the committed value.
 */
export const NumericInput = React.forwardRef<HTMLInputElement, NumericInputProps>(({
  value,
  onValueChange,
  max,
  onFocus,
  onBlur,
  ...props
}, ref) => {
  const [text, setText] = React.useState(() => displayValue(value))
  const focused = React.useRef(false)
  React.useEffect(() => {
    if (!focused.current) setText(displayValue(value))
  }, [value])
  const maxDigits = typeof max === 'number' && max > 0 ? String(Math.trunc(max)).length : 10
  return (
    <input
      {...props}
      ref={ref}
      type="text"
      inputMode="numeric"
      pattern="[0-9]*"
      autoComplete="off"
      value={text}
      onFocus={event => {
        focused.current = true
        onFocus?.(event)
      }}
      onBlur={event => {
        focused.current = false
        setText(displayValue(value))
        onBlur?.(event)
      }}
      onChange={event => {
        const digits = event.target.value.replace(/\D/g, '').replace(/^0+(?=\d)/, '').slice(0, maxDigits)
        setText(digits)
        if (!digits) {
          onValueChange(undefined)
          return
        }
        const parsed = Number(digits)
        onValueChange(typeof max === 'number' ? Math.min(parsed, max) : parsed)
      }}
    />
  )
})
NumericInput.displayName = 'NumericInput'
