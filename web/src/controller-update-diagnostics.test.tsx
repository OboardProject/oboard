// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  CONTROLLER_UPDATE_SLOW_MS,
  controllerUpdateActiveStartedAt,
  controllerUpdateDiagnosticsReason,
  controllerUpdateDiagnosticsTrigger,
  controllerUpdateElapsedLabel,
  controllerUpdateElapsedMs,
  isControllerUpdateFailedPhase,
  isControllerUpdateWaitingPhase,
  useControllerUpdateDiagnostics,
  type ControllerUpdateDiagnostics,
  type ControllerUpdateDiagnosticsState,
} from './controller-update-diagnostics'

describe('controller update diagnostics triggers', () => {
  it('treats abnormal terminal phases as failures', () => {
    expect(isControllerUpdateFailedPhase('failed')).toBe(true)
    expect(isControllerUpdateFailedPhase('stopped')).toBe(true)
    expect(isControllerUpdateFailedPhase('force_finished')).toBe(true)
    expect(isControllerUpdateFailedPhase('complete')).toBe(false)
    expect(isControllerUpdateFailedPhase('cancelled')).toBe(false)
  })

  it('only counts phases that are still working as waiting', () => {
    expect(isControllerUpdateWaitingPhase('installing')).toBe(true)
    expect(isControllerUpdateWaitingPhase('restarting')).toBe(true)
    expect(isControllerUpdateWaitingPhase('cancelling')).toBe(false)
    expect(isControllerUpdateWaitingPhase('confirm')).toBe(false)
  })

  it('fetches on failure immediately and on a waiting phase only after the slow threshold', () => {
    expect(controllerUpdateDiagnosticsReason('failed', 0)).toBe('failed')
    expect(controllerUpdateDiagnosticsReason('installing', 0)).toBe('')
    expect(controllerUpdateDiagnosticsReason('installing', CONTROLLER_UPDATE_SLOW_MS - 1)).toBe('')
    expect(controllerUpdateDiagnosticsReason('installing', CONTROLLER_UPDATE_SLOW_MS)).toBe('timeout')
    expect(controllerUpdateDiagnosticsReason('cancelled', CONTROLLER_UPDATE_SLOW_MS)).toBe('')
  })

  it('keeps one trigger per reason, phase and run', () => {
    expect(controllerUpdateDiagnosticsTrigger('', 'failed', 'run-1')).toBe('')
    expect(controllerUpdateDiagnosticsTrigger('failed', 'failed', 'run-1')).toBe('failed|failed|run-1')
    expect(controllerUpdateDiagnosticsTrigger('failed', 'stopped', 'run-1')).not.toBe(
      controllerUpdateDiagnosticsTrigger('failed', 'failed', 'run-1'),
    )
    expect(controllerUpdateDiagnosticsTrigger('timeout', 'installing', 'run-1')).not.toBe(
      controllerUpdateDiagnosticsTrigger('failed', 'failed', 'run-1'),
    )
  })

  it('keeps one timeout trigger while a slow run moves through its phases', () => {
    expect(controllerUpdateDiagnosticsTrigger('timeout', 'installing', 'run-1')).toBe(
      controllerUpdateDiagnosticsTrigger('timeout', 'restarting', 'run-1'),
    )
    expect(controllerUpdateDiagnosticsTrigger('timeout', 'installing', 'run-2')).not.toBe(
      controllerUpdateDiagnosticsTrigger('timeout', 'installing', 'run-1'),
    )
  })
})

describe('controller update elapsed time', () => {
  const now = Date.parse('2026-09-08T10:10:00Z')

  it('uses the server start time when it is sane', () => {
    expect(controllerUpdateElapsedMs('2026-09-08T10:00:00Z', now - 60_000, now)).toBe(600_000)
  })

  it('falls back to local time when the server time is unusable', () => {
    expect(controllerUpdateElapsedMs('', now - 60_000, now)).toBe(60_000)
    expect(controllerUpdateElapsedMs('2026-09-08T11:00:00Z', now - 60_000, now)).toBe(60_000)
    expect(controllerUpdateElapsedMs('2020-01-01T00:00:00Z', now - 60_000, now)).toBe(60_000)
  })

  it('ignores the completed previous run when starting a new update', () => {
    const startedAt = controllerUpdateActiveStartedAt({ active: false, started_at: '2026-09-08T10:00:00Z' })
    const elapsed = controllerUpdateElapsedMs(startedAt, now, now)
    expect(controllerUpdateDiagnosticsReason('starting', elapsed)).toBe('')
    expect(controllerUpdateDiagnosticsReason('checking', elapsed)).toBe('')
    const activeStart = controllerUpdateActiveStartedAt({ active: true, started_at: '2026-09-08T10:00:00Z' })
    expect(controllerUpdateDiagnosticsReason('downloading', controllerUpdateElapsedMs(activeStart, now, now))).toBe('timeout')
  })

  it('formats an elapsed label', () => {
    expect(controllerUpdateElapsedLabel(500)).toBe('')
    expect(controllerUpdateElapsedLabel(42_000)).toBe('已用时 42 秒')
    expect(controllerUpdateElapsedLabel(125_000)).toBe('已用时 2 分 5 秒')
  })
})

function Harness({ trigger, fetcher, onState }: { trigger: string; fetcher: () => Promise<ControllerUpdateDiagnostics>; onState: (state: ControllerUpdateDiagnosticsState & { retry: () => void }) => void }) {
  const state = useControllerUpdateDiagnostics(trigger, fetcher)
  onState(state)
  return null
}

describe('useControllerUpdateDiagnostics', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.restoreAllMocks()
  })

  it('fetches once per trigger and exposes the report', async () => {
    const fetcher = vi.fn(async () => ({ report: '更新诊断报告', log_hint: '日志已轮转' }))
    let latest: ControllerUpdateDiagnosticsState & { retry: () => void } = { status: 'idle', report: '', hint: '', error: '', retry: () => {} }
    await act(async () => {
      root.render(<Harness trigger="failed|failed|run-1" fetcher={fetcher} onState={state => { latest = state }} />)
    })
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(latest.status).toBe('ready')
    expect(latest.report).toBe('更新诊断报告')
    expect(latest.hint).toBe('日志已轮转')

    await act(async () => {
      root.render(<Harness trigger="failed|failed|run-1" fetcher={fetcher} onState={state => { latest = state }} />)
    })
    expect(fetcher).toHaveBeenCalledTimes(1)

    await act(async () => {
      root.render(<Harness trigger="failed|failed|run-2" fetcher={fetcher} onState={state => { latest = state }} />)
    })
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('stays idle without a trigger and reports fetch failures', async () => {
    const fetcher = vi.fn(async () => { throw new Error('controller unavailable') })
    let latest: ControllerUpdateDiagnosticsState & { retry: () => void } = { status: 'idle', report: '', hint: '', error: '', retry: () => {} }
    await act(async () => {
      root.render(<Harness trigger="" fetcher={fetcher} onState={state => { latest = state }} />)
    })
    expect(fetcher).not.toHaveBeenCalled()
    expect(latest.status).toBe('idle')

    await act(async () => {
      root.render(<Harness trigger="timeout|installing|run-1" fetcher={fetcher} onState={state => { latest = state }} />)
    })
    expect(latest.status).toBe('failed')
    expect(latest.error).toContain('controller unavailable')

    const retry = latest.retry
    await act(async () => { retry() })
    expect(fetcher).toHaveBeenCalledTimes(2)
  })
})
