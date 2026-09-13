// @vitest-environment jsdom
import React, { act, useRef } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'
import { useSurfaceResize } from './surface-motion'

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

it('animates intrinsic changes once, interrupts cleanly, and disconnects on reduced motion', async () => {
  let resize!: ResizeObserverCallback
  const disconnect = vi.fn()
  vi.stubGlobal('ResizeObserver', class {
    constructor(callback: ResizeObserverCallback) { resize = callback }
    observe() {}
    disconnect = disconnect
  })
  const cancel = vi.fn()
  const animate = vi.fn(() => ({ cancel, playState: 'finished', onfinish: null }))
  Object.defineProperty(HTMLElement.prototype, 'animate', { value: animate, configurable: true })
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  function Surface({ enabled }: { enabled: boolean }) {
    const ref = useRef<HTMLDivElement>(null)
    useSurfaceResize(ref, enabled, 'form')
    return <div ref={ref} />
  }
  const measure = (width: number, height: number) => resize([
    { borderBoxSize: [{ inlineSize: width, blockSize: height }] } as unknown as ResizeObserverEntry,
  ], {} as ResizeObserver)
  try {
    await act(async () => root.render(<Surface enabled />))
    measure(400, 200)
    expect(animate).not.toHaveBeenCalled()
    measure(400, 400)
    expect(animate).toHaveBeenCalledWith([{ scale: '1 0.5' }, { scale: '1 1' }], expect.objectContaining({ duration: 220 }))
    measure(400, 400)
    expect(animate).toHaveBeenCalledTimes(1)
    measure(500, 200)
    expect(cancel).toHaveBeenCalledTimes(1)
    await act(async () => root.render(<Surface enabled={false} />))
    expect(disconnect).toHaveBeenCalledTimes(1)
    expect(cancel).toHaveBeenCalledTimes(2)
    expect(container.firstElementChild?.getAttribute('style')).not.toContain('will-change: scale')
  } finally {
    await act(async () => root.unmount())
    container.remove()
    delete (HTMLElement.prototype as any).animate
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
  }
})
