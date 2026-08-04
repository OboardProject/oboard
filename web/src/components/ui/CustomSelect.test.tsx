// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { CustomSelect } from './CustomSelect'

describe('CustomSelect', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('keeps filter tools available when the current filter has no options', () => {
    act(() => root.render(<CustomSelect
      value="1"
      onChange={() => {}}
      options={[]}
      selectedLabel={<span>当前服务器</span>}
      menuHeader={<input aria-label="搜索服务器" />}
      emptyMessage="没有匹配的服务器"
      ariaLabel="选择服务器"
    />))

    const trigger = container.querySelector<HTMLButtonElement>('[aria-label="选择服务器"]')!
    act(() => trigger.click())

    expect(document.body.querySelector('[aria-label="搜索服务器"]')).not.toBeNull()
    expect(document.body.textContent).toContain('没有匹配的服务器')
    expect(document.body.querySelector('[role="listbox"]')?.contains(document.body.querySelector('[aria-label="搜索服务器"]'))).toBe(false)

    act(() => trigger.click())
    act(() => trigger.click())
    expect(document.body.querySelector('[aria-label="搜索服务器"]')).not.toBeNull()
  })
})
