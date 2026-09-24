// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AppearanceSettingsPanel } from './AppearanceSettingsPanel'

describe('AppearanceSettingsPanel', () => {
  let root: Root
  let host: HTMLDivElement

  beforeEach(() => {
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
    host = document.createElement('div')
    document.body.append(host)
    root = createRoot(host)
  })

  afterEach(() => {
    act(() => root.unmount())
    host.remove()
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('renders accent color presets and allows switching accent color', async () => {
    const onAccentColorChange = vi.fn()
    const notify = vi.fn()

    await act(async () => {
      root.render(
        <AppearanceSettingsPanel
          onAccentColorChange={onAccentColorChange}
          notify={notify}
        />
      )
    })

    const defaultBlueBtn = host.querySelector('button[aria-label="经典蓝"]') as HTMLButtonElement
    const purpleBtn = host.querySelector('button[aria-label="罗兰紫"]') as HTMLButtonElement

    expect(defaultBlueBtn).not.toBeNull()
    expect(purpleBtn).not.toBeNull()
    expect(defaultBlueBtn.getAttribute('aria-checked')).toBe('true')
    expect(purpleBtn.getAttribute('aria-checked')).toBe('false')

    await act(async () => {
      purpleBtn.click()
    })

    expect(onAccentColorChange).toHaveBeenCalledWith('#8b5cf6')
    expect(notify).toHaveBeenCalledWith('已应用强调色', 'success')
    expect(purpleBtn.getAttribute('aria-checked')).toBe('true')
    expect(document.documentElement.style.getPropertyValue('--color-primary')).toBe('#8b5cf6')

    // Test reset button
    const resetBtn = host.querySelector('button.accent-reset-btn') as HTMLButtonElement
    expect(resetBtn).not.toBeNull()

    await act(async () => {
      resetBtn.click()
    })

    expect(onAccentColorChange).toHaveBeenCalledWith('#007aff')
    expect(defaultBlueBtn.getAttribute('aria-checked')).toBe('true')
    expect(document.documentElement.style.getPropertyValue('--color-primary')).toBe('#007aff')
  })
})

