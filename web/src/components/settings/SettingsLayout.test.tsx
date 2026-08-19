// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { SettingsDisclosure, SettingsGroup, SettingsRow, SettingsSwitchRow } from './SettingsLayout'
import { Select } from '../ui/select'

describe('SettingsLayout', () => {
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

  it('renders grouped setting rows with an accessible switch', () => {
    act(() => root.render(
      <SettingsGroup title="通知" description="通知策略">
        <SettingsRow label="阈值" description="秒"><input aria-label="阈值" /></SettingsRow>
        <SettingsSwitchRow label="启用通知" checked onChange={() => undefined} ariaLabel="启用通知" />
      </SettingsGroup>,
    ))

    expect(container.querySelector('h3')?.textContent).toBe('通知')
    expect(container.querySelector('[role="switch"]')).not.toBeNull()
    expect(container.querySelector('[role="switch"]')?.getAttribute('aria-checked')).toBe('true')
    expect(container.textContent).toContain('已开启')
  })

  it('keeps segmented choices bounded and emits the selected value', () => {
    let selected = ''
    act(() => root.render(
      <Select variant="segmented" value="short" onChange={event => { selected = event.target.value }} aria-label="模式">
        <option value="short">短</option>
        <option value="long">较长选项</option>
      </Select>,
    ))

    const segmented = container.querySelector('.ui-segmented-select') as HTMLElement
    expect(segmented.className).toContain('segments-2')
    expect(segmented.style.gridTemplateColumns).toContain('min-content')
    act(() => (container.querySelectorAll('[role="radio"]')[1] as HTMLButtonElement).click())
    expect(selected).toBe('long')
  })

  it('keeps advanced content collapsed until the disclosure is opened', () => {
    act(() => root.render(<SettingsDisclosure title="高级设置" summary="未展开"><p>隐藏内容</p></SettingsDisclosure>))

    const disclosure = container.querySelector('details')!
    expect(disclosure.open).toBe(false)
    act(() => (container.querySelector('summary') as HTMLElement).click())
    expect(disclosure.open).toBe(true)
    expect(container.textContent).toContain('隐藏内容')
  })
})
