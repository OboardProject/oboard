// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { ControllerUpdateLightfield } from './controller-update-lightfield'

it.each([
  { previous: null, theme: 'light', updateColor: '#f8f9fb' },
  { previous: '#123456', theme: 'dark', updateColor: '#050505' },
] as const)('uses $theme browser chrome and restores the previous color', ({ previous, theme, updateColor }) => {
  const priorTheme = document.documentElement.dataset.theme
  document.documentElement.dataset.theme = theme
  const context = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
  const existing = previous === null ? null : document.createElement('meta')
  if (existing) {
    existing.name = 'theme-color'
    existing.content = previous!
    document.head.appendChild(existing)
  }
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  try {
    act(() => root.render(<ControllerUpdateLightfield mode="running" reduceMotion />))
    expect(document.querySelectorAll('meta[name="theme-color"]')).toHaveLength(1)
    expect(document.querySelector('meta[name="theme-color"]')?.getAttribute('content')).toBe(updateColor)
    act(() => root.unmount())
    expect(document.querySelector('meta[name="theme-color"]')).toBe(existing)
    if (existing) expect(existing.content).toBe(previous)
  } finally {
    container.remove()
    existing?.remove()
    context.mockRestore()
    if (priorTheme === undefined) delete document.documentElement.dataset.theme
    else document.documentElement.dataset.theme = priorTheme
  }
})

it('updates browser chrome when the panel theme changes during an update', async () => {
  const priorTheme = document.documentElement.dataset.theme
  document.documentElement.dataset.theme = 'dark'
  const context = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  try {
    act(() => root.render(<ControllerUpdateLightfield mode="running" reduceMotion />))
    expect(document.querySelector('meta[name="theme-color"]')?.getAttribute('content')).toBe('#050505')
    await act(async () => { document.documentElement.dataset.theme = 'light' })
    expect(document.querySelector('meta[name="theme-color"]')?.getAttribute('content')).toBe('#f8f9fb')
    act(() => root.unmount())
    expect(document.querySelector('meta[name="theme-color"]')).toBeNull()
  } finally {
    container.remove()
    context.mockRestore()
    if (priorTheme === undefined) delete document.documentElement.dataset.theme
    else document.documentElement.dataset.theme = priorTheme
  }
})
