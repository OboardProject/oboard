// @vitest-environment jsdom

import React, { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { NumericInput } from './numeric-input'

function typeInto(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
  setter?.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

describe('NumericInput', () => {
  let container: HTMLDivElement
  let root: Root
  let committed: Array<number | undefined>

  function Harness({ initial, fallback }: { initial: number; fallback?: number }) {
    const [value, setValue] = useState<number | undefined>(initial)
    return <NumericInput
      value={value ?? fallback}
      max={65535}
      onValueChange={next => {
        committed.push(next)
        setValue(next)
      }}
    />
  }

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    committed = []
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
  })

  it('lets every digit be cleared without a fallback snapping back', () => {
    act(() => root.render(<Harness initial={443} fallback={443} />))
    const input = container.querySelector('input') as HTMLInputElement
    act(() => input.focus())
    act(() => typeInto(input, ''))
    expect(input.value).toBe('')
    expect(committed.at(-1)).toBeUndefined()
    act(() => typeInto(input, '8'))
    act(() => typeInto(input, '84'))
    act(() => typeInto(input, '8443'))
    expect(input.value).toBe('8443')
    expect(committed.at(-1)).toBe(8443)
  })

  it('replaces the first digit and drops non-digits and leading zeros', () => {
    act(() => root.render(<Harness initial={443} />))
    const input = container.querySelector('input') as HTMLInputElement
    act(() => input.focus())
    act(() => typeInto(input, '943'))
    expect(committed.at(-1)).toBe(943)
    act(() => typeInto(input, '0a8x0'))
    expect(input.value).toBe('80')
    expect(committed.at(-1)).toBe(80)
  })

  it('caps the committed value at max and re-syncs the text on blur', () => {
    act(() => root.render(<Harness initial={443} />))
    const input = container.querySelector('input') as HTMLInputElement
    act(() => input.focus())
    act(() => typeInto(input, '70000'))
    expect(committed.at(-1)).toBe(65535)
    act(() => input.blur())
    expect(input.value).toBe('65535')
  })
})
