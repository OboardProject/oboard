// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AppearanceSettingsPanel } from './AppearanceSettingsPanel'
import { type ThemePreference } from '../../theme'

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

  it('renders standard and transparent theme cards and switches themes on click', async () => {
    const onThemeChange = vi.fn()
    const notify = vi.fn()

    await act(async () => {
      root.render(<AppearanceSettingsPanel theme="dark" onThemeChange={onThemeChange} notify={notify} />)
    })

    const standardBtn = host.querySelector('button[aria-label="标准主题"]') as HTMLButtonElement
    const glassBtn = host.querySelector('button[aria-label="通透主题"]') as HTMLButtonElement

    expect(standardBtn).not.toBeNull()
    expect(glassBtn).not.toBeNull()
    expect(standardBtn.getAttribute('aria-checked')).toBe('true')
    expect(glassBtn.getAttribute('aria-checked')).toBe('false')
    expect(standardBtn.textContent).toContain('当前生效中')
    expect(glassBtn.textContent).toContain('点击应用')

    await act(async () => {
      glassBtn.click()
    })

    expect(onThemeChange).toHaveBeenCalledTimes(1)
    expect(onThemeChange.mock.calls[0][0]).toBe('glass')
    expect(notify).toHaveBeenCalledWith('已应用通透主题', 'success')
  })

  it('reflects active state when transparent theme is selected', async () => {
    const onThemeChange = vi.fn()
    const notify = vi.fn()

    await act(async () => {
      root.render(<AppearanceSettingsPanel theme="glass" onThemeChange={onThemeChange} notify={notify} />)
    })

    const standardBtn = host.querySelector('button[aria-label="标准主题"]') as HTMLButtonElement
    const glassBtn = host.querySelector('button[aria-label="通透主题"]') as HTMLButtonElement

    expect(standardBtn.getAttribute('aria-checked')).toBe('false')
    expect(glassBtn.getAttribute('aria-checked')).toBe('true')
    expect(glassBtn.textContent).toContain('当前生效中')
    expect(standardBtn.textContent).toContain('点击应用')

    await act(async () => {
      standardBtn.click()
    })

    expect(onThemeChange).toHaveBeenCalledTimes(1)
    expect(onThemeChange.mock.calls[0][0]).toBe('dark')
    expect(notify).toHaveBeenCalledWith('已应用标准主题', 'success')
  })
})
