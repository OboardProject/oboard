// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  CONTROLLER_UPDATE_PROMPT_AUTO_DISMISS_MS,
  useControllerUpdatePromptAutoDismiss,
} from './controller-update-prompt'

function Harness({
  visible = true,
  autoUpdateEnabled = true,
  dialogOpen = false,
  onDismiss,
}: {
  visible?: boolean
  autoUpdateEnabled?: boolean
  dialogOpen?: boolean
  onDismiss: () => void
}) {
  useControllerUpdatePromptAutoDismiss(visible, autoUpdateEnabled, dialogOpen, onDismiss)
  return null
}

describe('controller update prompt auto-dismiss', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    vi.useFakeTimers()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.useRealTimers()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('dismisses an automatic-update prompt after ten seconds', () => {
    const onDismiss = vi.fn()
    act(() => root.render(<Harness onDismiss={onDismiss} />))

    act(() => vi.advanceTimersByTime(CONTROLLER_UPDATE_PROMPT_AUTO_DISMISS_MS - 1))
    expect(onDismiss).not.toHaveBeenCalled()

    act(() => vi.advanceTimersByTime(1))
    expect(onDismiss).toHaveBeenCalledTimes(1)
  })

  it('stays visible for manual updates and restarts the timer after the confirmation dialog closes', () => {
    const onDismiss = vi.fn()
    act(() => root.render(<Harness autoUpdateEnabled={false} onDismiss={onDismiss} />))
    act(() => vi.advanceTimersByTime(CONTROLLER_UPDATE_PROMPT_AUTO_DISMISS_MS))
    expect(onDismiss).not.toHaveBeenCalled()

    act(() => root.render(<Harness onDismiss={onDismiss} />))
    act(() => vi.advanceTimersByTime(5_000))
    act(() => root.render(<Harness dialogOpen onDismiss={onDismiss} />))
    act(() => vi.advanceTimersByTime(CONTROLLER_UPDATE_PROMPT_AUTO_DISMISS_MS))
    expect(onDismiss).not.toHaveBeenCalled()

    act(() => root.render(<Harness onDismiss={onDismiss} />))
    act(() => vi.advanceTimersByTime(CONTROLLER_UPDATE_PROMPT_AUTO_DISMISS_MS - 1))
    expect(onDismiss).not.toHaveBeenCalled()
    act(() => vi.advanceTimersByTime(1))
    expect(onDismiss).toHaveBeenCalledTimes(1)
  })

  it('cleans up the pending timer when the prompt disappears', () => {
    const onDismiss = vi.fn()
    act(() => root.render(<Harness onDismiss={onDismiss} />))
    act(() => vi.advanceTimersByTime(5_000))
    act(() => root.render(<Harness visible={false} onDismiss={onDismiss} />))
    act(() => vi.advanceTimersByTime(CONTROLLER_UPDATE_PROMPT_AUTO_DISMISS_MS))

    expect(onDismiss).not.toHaveBeenCalled()
  })
})
