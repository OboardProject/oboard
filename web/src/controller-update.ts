export const CONTROLLER_UPDATE_PENDING_MESSAGE = '正在等待更新完成'
export const CONTROLLER_UPDATE_FORCE_FINISH_PHRASE = '强制结束更新任务'

export function isControllerUpdateForceFinishConfirmation(value: string | null | undefined): boolean {
  return String(value || '').trim() === CONTROLLER_UPDATE_FORCE_FINISH_PHRASE
}

const CONTROLLER_UPDATE_IN_PROGRESS_STATUSES = [
  'checking',
  'downloading',
  'preflight',
  'backing_up',
  'ready',
  'installing',
  'restarting',
  'verifying',
  'cancelling',
]
const EXPECTED_DISCONNECT_MARKERS = ['failed to fetch', 'networkerror', 'load failed', 'bad gateway', 'service unavailable', 'gateway timeout']

export function isControllerUpdateInProgressStatus(status: string | undefined | null): boolean {
  return status != null && CONTROLLER_UPDATE_IN_PROGRESS_STATUSES.includes(status)
}

export function controllerUpdateDisplayPhase(status: { status?: string; operation?: { active?: boolean; phase?: string } } | null | undefined): string {
  if (status?.operation?.active && status.operation.phase) return status.operation.phase
  return status?.status || ''
}

const FLOW_BASE: Record<string, number> = {
  starting: 6,
  checking: 10,
  downloading: 22,
  preflight: 32,
  backing_up: 34,
  ready: 74,
  installing: 78,
  restarting: 88,
  verifying: 96,
  cancelling: 40,
}

export function controllerUpdateFlowPercent(phase: string, backupPercent?: number): number {
  if (phase === 'complete' || phase === 'installed') return 100
  if (phase === 'backing_up') {
    const backup = Math.max(0, Math.min(100, backupPercent || 0))
    return Math.max(34, Math.min(72, 34 + backup * 0.38))
  }
  const base = FLOW_BASE[phase]
  if (typeof base === 'number') return base
  return 8
}

export function monotonicPercent(previous: number, next: number): number {
  const value = Math.max(0, Math.min(100, Math.round(next)))
  if (value < previous) return previous
  return value
}

export function isControllerUpdateFailedStatus(status: string | undefined | null, lastError?: string | null): boolean {
  return status === 'failed' || status === 'unavailable' || (status === 'cancelled' && Boolean(lastError))
}

const EXPECTED_DISCONNECT_STATUSES = [502, 503, 504]

export function createControllerUpdateRequestGuard() {
  let latestRequest = 0
  return {
    beginRequest() {
      latestRequest += 1
      return latestRequest
    },
    invalidate() {
      latestRequest += 1
    },
    isLatest(request: number) {
      return request === latestRequest
    },
  }
}

export function shouldDeferControllerUpdateTerminalStatus(status: string, installRequestPending: boolean, cancelExpected: boolean): boolean {
  if (!installRequestPending || cancelExpected) return false
  return status === 'cancelled' || status === 'idle' || status === 'pinned' || status === 'available'
}

export function isExpectedControllerUpdateDisconnect(error: unknown): boolean {
  if (error instanceof TypeError) return true
  const status = Number((error as any)?.status)
  if (EXPECTED_DISCONNECT_STATUSES.includes(status)) return true
  const message = String((error as any)?.message || error || '').trim().toLowerCase()
  return EXPECTED_DISCONNECT_MARKERS.some(value => message.includes(value))
}

export type ControllerUpdatePendingToast = { message: string; kind: 'info' }

export function controllerUpdatePendingToast(updateInProgress: boolean, error: unknown): ControllerUpdatePendingToast | null {
  if (!updateInProgress) return null
  if (!isExpectedControllerUpdateDisconnect(error)) return null
  return { message: CONTROLLER_UPDATE_PENDING_MESSAGE, kind: 'info' }
}
