// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ThemeSelector } from './components/ui/ThemeSelector'
import { applyAccentColorToDocument, applyThemeToDocument, getAccentColor, getThemePreference, resolveTheme, saveAccentColor, saveThemePreference, watchSystemTheme, type ThemePreference } from './theme'

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

it('updates primary text contrast when the accent color changes', () => {
  applyThemeToDocument('dark')
  applyAccentColorToDocument('#7c3aed')
  expect(document.documentElement.style.getPropertyValue('--primary-contrast')).toBe('#ffffff')

  applyAccentColorToDocument('#fbbf24')
  expect(document.documentElement.style.getPropertyValue('--primary-contrast')).toBe('#111827')
})

it('keeps separate light and dark accent colors through theme changes', () => {
  expect(getAccentColor('light')).toBe('#007aff')
  expect(getAccentColor('dark')).toBe('#60a5fa')
  saveAccentColor('light', '#7c3aed')
  saveAccentColor('dark', '#22d3ee')

  applyThemeToDocument('light')
  expect(document.documentElement.style.getPropertyValue('--primary')).toBe('#7c3aed')
  expect(document.documentElement.style.getPropertyValue('--theme-accent-color')).toBe('#7c3aed')
  applyThemeToDocument('dark')
  expect(document.documentElement.style.getPropertyValue('--primary')).toBe('#22d3ee')
  expect(document.documentElement.style.getPropertyValue('--theme-accent-color')).toBe('#22d3ee')
  expect(document.documentElement.style.getPropertyValue('--primary-contrast')).toBe('#111827')
  applyThemeToDocument('light')
  expect(document.documentElement.style.getPropertyValue('--primary')).toBe('#7c3aed')
})

it('selects light, automatic and dark modes directly with icon-only buttons', async () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  function Harness() {
    const [value, setValue] = React.useState<ThemePreference>('auto')
    return <div data-preference={value}><ThemeSelector value={value} onChange={setValue} variant="sidebar" /></div>
  }
  try {
    await act(async () => root.render(<Harness />))
    const buttons = [...container.querySelectorAll<HTMLButtonElement>('.theme-selector-option')]
    expect(buttons.map(button => button.getAttribute('aria-label'))).toEqual(['浅色模式', '自动模式，跟随系统', '暗黑模式'])
    expect(buttons.every(button => button.textContent === '')).toBe(true)
    expect(buttons.map(button => button.getAttribute('aria-pressed'))).toEqual(['false', 'true', 'false'])

    for (const [index, preference] of [[2, 'dark'], [0, 'light'], [1, 'auto']] as const) {
      buttons[index].focus()
      await act(async () => buttons[index].click())
      expect(container.firstElementChild?.getAttribute('data-preference')).toBe(preference)
      expect(buttons.map(button => button.getAttribute('aria-pressed'))).toEqual(buttons.map((_, buttonIndex) => String(buttonIndex === index)))
      expect(document.activeElement).toBe(buttons[index])
    }
  } finally {
    await act(async () => root.unmount())
    container.remove()
  }
})
