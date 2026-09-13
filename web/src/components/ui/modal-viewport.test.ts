// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest'
import { trackModalViewport } from './modal-viewport'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  document.documentElement.style.removeProperty('--dialog-viewport-height')
  document.documentElement.style.removeProperty('--dialog-viewport-top')
  document.documentElement.removeAttribute('data-dialog-compact-viewport')
})

it('tracks keyboard height and panning, batches events, then restores the previous viewport', () => {
  const viewport = Object.assign(new EventTarget(), { height: 740, offsetTop: 0 })
  vi.stubGlobal('visualViewport', viewport)
  let scheduled!: FrameRequestCallback
  const request = vi.spyOn(window, 'requestAnimationFrame').mockImplementation(callback => { scheduled = callback; return 1 })
  const cancel = vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {})
  const root = document.documentElement
  root.style.setProperty('--dialog-viewport-height', '100dvh', 'important')
  const cleanup = trackModalViewport()
  expect(root.style.getPropertyValue('--dialog-viewport-height')).toBe('740px')
  viewport.height = 280
  viewport.offsetTop = 35
  viewport.dispatchEvent(new Event('resize'))
  viewport.dispatchEvent(new Event('scroll'))
  expect(request).toHaveBeenCalledTimes(1)
  scheduled(0)
  expect(root.style.getPropertyValue('--dialog-viewport-height')).toBe('280px')
  expect(root.style.getPropertyValue('--dialog-viewport-top')).toBe('35px')
  expect(root.hasAttribute('data-dialog-compact-viewport')).toBe(true)
  viewport.height = 740
  viewport.dispatchEvent(new Event('resize'))
  scheduled(16)
  expect(root.hasAttribute('data-dialog-compact-viewport')).toBe(false)
  viewport.dispatchEvent(new Event('scroll'))
  cleanup()
  expect(cancel).toHaveBeenCalledWith(1)
  expect(root.style.getPropertyValue('--dialog-viewport-height')).toBe('100dvh')
  expect(root.style.getPropertyPriority('--dialog-viewport-height')).toBe('important')
  expect(root.style.getPropertyValue('--dialog-viewport-top')).toBe('')
  request.mockClear()
  viewport.dispatchEvent(new Event('resize'))
  expect(request).not.toHaveBeenCalled()
})
