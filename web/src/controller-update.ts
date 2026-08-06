export const CONTROLLER_UPDATE_PENDING_MESSAGE = '正在等待更新完成'

const CONTROLLER_UPDATE_IN_PROGRESS_STATUSES = ['downloading', 'ready', 'installing', 'cancelling']
const EXPECTED_DISCONNECT_MARKERS = ['failed to fetch', 'networkerror', 'load failed', 'bad gateway', 'service unavailable', 'gateway timeout']

export function isControllerUpdateInProgressStatus(status: string | undefined | null): boolean {
  return status != null && CONTROLLER_UPDATE_IN_PROGRESS_STATUSES.includes(status)
}

export function isExpectedControllerUpdateDisconnect(error: unknown): boolean {
  if (error instanceof TypeError) return true
  const message = String((error as any)?.message || error || '').trim().toLowerCase()
  return EXPECTED_DISCONNECT_MARKERS.some(value => message.includes(value))
}

export type ControllerUpdatePendingToast = { message: string; kind: 'info' }

export function controllerUpdatePendingToast(updateInProgress: boolean, error: unknown): ControllerUpdatePendingToast | null {
  if (!updateInProgress) return null
  if (!isExpectedControllerUpdateDisconnect(error)) return null
  return { message: CONTROLLER_UPDATE_PENDING_MESSAGE, kind: 'info' }
}
