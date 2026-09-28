// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AppearanceSettingsPanel } from './AppearanceSettingsPanel'
import { applyThemeToDocument } from '../../theme'

describe('AppearanceSettingsPanel', () => {
  let root: Root
  let host: HTMLDivElement

  beforeEach(() => {
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
    localStorage.clear()
    applyThemeToDocument('light')
    host = document.createElement('div')
    document.body.append(host)
    root = createRoot(host)
  })

  afterEach(() => {
    act(() => root.unmount())
    host.remove()
    localStorage.clear()
    applyThemeToDocument('light')
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

    const defaultBlueBtn = host.querySelector('button[aria-label="浅色模式：经典蓝"]') as HTMLButtonElement
    const purpleBtn = host.querySelector('button[aria-label="浅色模式：罗兰紫"]') as HTMLButtonElement
    const darkBlueBtn = host.querySelector('button[aria-label="深色模式：拓扑蓝"]') as HTMLButtonElement

    expect(defaultBlueBtn).not.toBeNull()
    expect(purpleBtn).not.toBeNull()
    expect(darkBlueBtn.getAttribute('aria-pressed')).toBe('true')
    expect(defaultBlueBtn.getAttribute('aria-pressed')).toBe('true')
    expect(purpleBtn.getAttribute('aria-pressed')).toBe('false')

    await act(async () => {
      purpleBtn.click()
    })

    expect(onAccentColorChange).toHaveBeenCalledWith('light', '#7c3aed')
    expect(notify).toHaveBeenCalledWith('已应用浅色模式强调色', 'success')
    expect(purpleBtn.getAttribute('aria-pressed')).toBe('true')
    expect(document.documentElement.style.getPropertyValue('--color-primary')).toBe('#7c3aed')

    // Test reset button
    const resetBtn = host.querySelector('button.accent-reset-btn') as HTMLButtonElement
    expect(resetBtn).not.toBeNull()

    await act(async () => {
      resetBtn.click()
    })

    expect(onAccentColorChange).toHaveBeenCalledWith('light', '#007aff')
    expect(defaultBlueBtn.getAttribute('aria-pressed')).toBe('true')
    expect(document.documentElement.style.getPropertyValue('--color-primary')).toBe('#007aff')
  })

  it('saves the dark color without changing the visible light theme', async () => {
    await act(async () => root.render(<AppearanceSettingsPanel />))
    const darkPurpleBtn = host.querySelector('button[aria-label="深色模式：罗兰紫"]') as HTMLButtonElement
    await act(async () => darkPurpleBtn.click())

    expect(localStorage.getItem('oboard.accent_color.dark')).toBe('#a78bfa')
    expect(localStorage.getItem('oboard.accent_color')).toBeNull()
    expect(document.documentElement.style.getPropertyValue('--primary')).toBe('#007aff')
    expect(darkPurpleBtn.getAttribute('aria-pressed')).toBe('true')

    applyThemeToDocument('dark')
    expect(document.documentElement.style.getPropertyValue('--primary')).toBe('#a78bfa')
    const resetBtn = host.querySelectorAll<HTMLButtonElement>('.accent-reset-btn')[0]
    await act(async () => resetBtn.click())
    expect(localStorage.getItem('oboard.accent_color.dark')).toBe('#60a5fa')
    expect(document.documentElement.style.getPropertyValue('--primary')).toBe('#60a5fa')
  })
})
