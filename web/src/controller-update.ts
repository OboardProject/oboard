export const CONTROLLER_UPDATE_PENDING_MESSAGE = '正在等待更新完成'

const CONTROLLER_UPDATE_IN_PROGRESS_STATUSES = ['downloading', 'ready', 'installing', 'cancelling']
const EXPECTED_DISCONNECT_MARKERS = ['failed to fetch', 'networkerror', 'load failed', 'bad gateway', 'service unavailable', 'gateway timeout']

export function isControllerUpdateInProgressStatus(status: string | undefined | null): boolean {
  return status != null && CONTROLLER_UPDATE_IN_PROGRESS_STATUSES.includes(status)
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
