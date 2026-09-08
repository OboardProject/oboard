import { useCallback, useEffect, useRef, useState } from 'react'

export type ControllerUpdateDiagnostics = {
  generated_at?: string
  outcome?: string
  report?: string
  log_lines?: number
  log_available?: boolean
  log_hint?: string
}

export type ControllerUpdateDiagnosticsState = {
  status: 'idle' | 'loading' | 'ready' | 'failed'
  report: string
  hint: string
  error: string
}

export const CONTROLLER_UPDATE_DIAGNOSTICS_IDLE: ControllerUpdateDiagnosticsState = { status: 'idle', report: '', hint: '', error: '' }

// A normal update finishes in a few minutes. The Controller only gives up on a
// stuck run after its own install timeout, so the panel raises the log earlier
// to let an operator start troubleshooting while the run is still tracked.
export const CONTROLLER_UPDATE_SLOW_MS = 8 * 60_000
const CONTROLLER_UPDATE_MAX_ELAPSED_MS = 24 * 60 * 60_000

const CONTROLLER_UPDATE_FAILED_PHASES = ['failed', 'stopped', 'force_finished']
const CONTROLLER_UPDATE_WAITING_PHASES = ['starting', 'checking', 'downloading', 'preflight', 'backing_up', 'ready', 'installing', 'restarting', 'verifying']

export function isControllerUpdateFailedPhase(phase: string | null | undefined): boolean {
  return phase != null && CONTROLLER_UPDATE_FAILED_PHASES.includes(phase)
}

export function isControllerUpdateWaitingPhase(phase: string | null | undefined): boolean {
  return phase != null && CONTROLLER_UPDATE_WAITING_PHASES.includes(phase)
}

export type ControllerUpdateDiagnosticsReason = '' | 'failed' | 'timeout'

export function controllerUpdateDiagnosticsReason(phase: string | null | undefined, elapsedMs: number): ControllerUpdateDiagnosticsReason {
  if (isControllerUpdateFailedPhase(phase)) return 'failed'
  if (isControllerUpdateWaitingPhase(phase) && elapsedMs >= CONTROLLER_UPDATE_SLOW_MS) return 'timeout'
  return ''
}

// The trigger identifies one abnormal outcome. It keeps the panel from
// re-fetching while the same failure stays on screen, and forces a fresh report
// when the run or the reason changes. A slow run keeps one trigger across its
// remaining phases so a long install does not refetch on every phase change.
export function controllerUpdateDiagnosticsTrigger(reason: ControllerUpdateDiagnosticsReason, phase: string, runKey: string): string {
  if (!reason) return ''
  if (reason === 'timeout') return `timeout|${String(runKey || '')}`
  return `${reason}|${phase}|${String(runKey || '')}`
}

export function controllerUpdateElapsedMs(startedAt: string | null | undefined, activatedAt: number, now: number): number {
  const localElapsed = Math.max(0, now - activatedAt)
  const parsed = Date.parse(String(startedAt || ''))
  if (!Number.isFinite(parsed)) return localElapsed
  const serverElapsed = now - parsed
  if (serverElapsed < 0 || serverElapsed > CONTROLLER_UPDATE_MAX_ELAPSED_MS) return localElapsed
  return Math.max(localElapsed, serverElapsed)
}

export function controllerUpdateElapsedLabel(elapsedMs: number): string {
  if (!Number.isFinite(elapsedMs) || elapsedMs < 1000) return ''
  const totalSeconds = Math.floor(elapsedMs / 1000)
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  if (minutes <= 0) return `已用时 ${seconds} 秒`
  return `已用时 ${minutes} 分 ${seconds} 秒`
}

export function useControllerUpdateElapsed(startedAt: string | null | undefined, active: boolean, intervalMs = 5000): number {
  const [elapsed, setElapsed] = useState(0)
  const activatedAtRef = useRef(0)
  useEffect(() => {
    if (!active) {
      activatedAtRef.current = 0
      setElapsed(0)
      return
    }
    if (!activatedAtRef.current) activatedAtRef.current = Date.now()
    const tick = () => setElapsed(controllerUpdateElapsedMs(startedAt, activatedAtRef.current, Date.now()))
    tick()
    const timer = window.setInterval(tick, intervalMs)
    return () => window.clearInterval(timer)
  }, [active, startedAt, intervalMs])
  return elapsed
}

export function useControllerUpdateDiagnostics(
  trigger: string,
  fetcher: () => Promise<ControllerUpdateDiagnostics>,
): ControllerUpdateDiagnosticsState & { retry: () => void } {
  const [state, setState] = useState<ControllerUpdateDiagnosticsState>(CONTROLLER_UPDATE_DIAGNOSTICS_IDLE)
  const [attempt, setAttempt] = useState(0)
  const fetcherRef = useRef(fetcher)
  fetcherRef.current = fetcher
  const lastTriggerRef = useRef('')

  useEffect(() => {
    if (!trigger) {
      lastTriggerRef.current = ''
      setState(CONTROLLER_UPDATE_DIAGNOSTICS_IDLE)
      return
    }
    const key = `${trigger}#${attempt}`
    if (lastTriggerRef.current === key) return
    lastTriggerRef.current = key
    let cancelled = false
    setState({ status: 'loading', report: '', hint: '', error: '' })
    void fetcherRef.current()
      .then(result => {
        if (cancelled) return
        setState({ status: 'ready', report: String(result?.report || ''), hint: String(result?.log_hint || ''), error: '' })
      })
      .catch(error => {
        if (cancelled) return
        setState({ status: 'failed', report: '', hint: '', error: String(error?.message || error || '') })
      })
    return () => { cancelled = true }
  }, [trigger, attempt])

  const retry = useCallback(() => setAttempt(value => value + 1), [])
  return { ...state, retry }
}
