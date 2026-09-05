// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ThemeSelector } from './components/ui/ThemeSelector'
import { applyThemeToDocument, getThemePreference, resolveTheme, saveThemePreference, watchSystemTheme, type ThemePreference } from './theme'

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  localStorage.clear()
  applyThemeToDocument('light')
})

function mockSystemTheme(dark: boolean) {
  const media = new EventTarget() as EventTarget & { matches: boolean }
  media.matches = dark
  vi.stubGlobal('matchMedia', vi.fn(() => media))
  return media
}

describe('theme preference', () => {
  it('defaults to automatic and follows the system without saving its resolved color', () => {
    const media = mockSystemTheme(true)
    expect(getThemePreference()).toBe('auto')
    applyThemeToDocument(resolveTheme(getThemePreference()))
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(localStorage.getItem('oboard.theme')).toBeNull()

    saveThemePreference('auto')
    const stop = watchSystemTheme('auto', applyThemeToDocument)
    media.matches = false
    media.dispatchEvent(new Event('change'))
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(getThemePreference()).toBe('auto')
    stop?.()
    media.matches = true
    media.dispatchEvent(new Event('change'))
    expect(document.documentElement.dataset.theme).toBe('light')
  })

  it.each(['dark', 'light'] as const)('restores explicit %s and ignores system changes', preference => {
    const media = mockSystemTheme(preference !== 'dark')
    saveThemePreference(preference)
    expect(getThemePreference()).toBe(preference)
    expect(resolveTheme(getThemePreference())).toBe(preference)
    const onChange = vi.fn()
    expect(watchSystemTheme(preference, onChange)).toBeUndefined()
    media.dispatchEvent(new Event('change'))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('uses the system even when browser storage is unavailable', () => {
    mockSystemTheme(true)
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('unavailable') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable') })
    expect(resolveTheme(getThemePreference())).toBe('dark')
    expect(() => saveThemePreference('light')).not.toThrow()
  })
})

it('cycles one button through automatic, dark, light, and automatic', async () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  function Harness() {
    const [value, setValue] = React.useState<ThemePreference>('auto')
    return <ThemeSelector value={value} onChange={setValue} variant="sidebar" />
  }
  try {
    await act(async () => root.render(<Harness />))
    const button = container.querySelector('button')!
    button.focus()
    expect(button.textContent).toBe('自动主题')
    for (const label of ['深色主题', '浅色主题', '自动主题']) {
      await act(async () => button.click())
      expect(button.textContent).toBe(label)
      expect(button.getAttribute('aria-label')).toContain(`当前${label}`)
      expect(container.querySelectorAll('button')).toHaveLength(1)
      expect(document.activeElement).toBe(button)
    }
  } finally {
    await act(async () => root.unmount())
    container.remove()
  }
})
