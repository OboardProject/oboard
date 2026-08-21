// @vitest-environment jsdom

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { usePausedInterval } from './visibility'

let visibilityOverride: 'visible' | 'hidden' = 'visible'
function setVisibility(visible: boolean) {
  visibilityOverride = visible ? 'visible' : 'hidden'
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibilityOverride })
  document.dispatchEvent(new Event('visibilitychange'))
}

function IntervalHarness({ callback, active = true, delayMS = 1000 }: { callback: () => void; active?: boolean; delayMS?: number }) {
  usePausedInterval(callback, delayMS, active)
  return null
}

describe('usePausedInterval', () => {
  let root: Root
  let container: HTMLDivElement

  beforeEach(() => {
    vi.useFakeTimers()
    setVisibility(true)
    container = document.createElement('div')
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    vi.useRealTimers()
    setVisibility(true)
  })

  it('stops while hidden and restarts timing when visible', () => {
    const callback = vi.fn()
    act(() => root.render(<IntervalHarness callback={callback} />))
    act(() => vi.advanceTimersByTime(1000))
    expect(callback).toHaveBeenCalledTimes(1)

    act(() => setVisibility(false))
    act(() => vi.advanceTimersByTime(5000))
    expect(callback).toHaveBeenCalledTimes(1)

    act(() => setVisibility(true))
    act(() => vi.advanceTimersByTime(1000))
    expect(callback).toHaveBeenCalledTimes(2)
  })

  it('respects the active flag', () => {
    const callback = vi.fn()
    act(() => root.render(<IntervalHarness callback={callback} active={false} />))
    act(() => vi.advanceTimersByTime(5000))
    expect(callback).not.toHaveBeenCalled()

    act(() => root.render(<IntervalHarness callback={callback} />))
    act(() => vi.advanceTimersByTime(1000))
    expect(callback).toHaveBeenCalledTimes(1)
  })
})
