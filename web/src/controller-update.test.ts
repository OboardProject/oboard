import { describe, expect, it } from 'vitest'

import {
  CONTROLLER_UPDATE_PENDING_MESSAGE,
  controllerUpdateDisplayPhase,
  controllerUpdatePendingToast,
  createControllerUpdateRequestGuard,
  isControllerUpdateFailedStatus,
  isControllerUpdateInProgressStatus,
  shouldDeferControllerUpdateTerminalStatus,
} from './controller-update'

describe('controller update pending toast', () => {
  it('rejects an old cancelled response after a new install request starts', () => {
    const guard = createControllerUpdateRequestGuard()
    const staleRefresh = guard.beginRequest()
    const installRequest = guard.beginRequest()

    expect(guard.isLatest(staleRefresh)).toBe(false)
    expect(guard.isLatest(installRequest)).toBe(true)

    guard.invalidate()
    expect(guard.isLatest(installRequest)).toBe(false)
  })

  it('defers an unrequested cancelled status while the install request is pending', () => {
    expect(shouldDeferControllerUpdateTerminalStatus('cancelled', true, false)).toBe(true)
    expect(shouldDeferControllerUpdateTerminalStatus('cancelled', true, true)).toBe(false)
    expect(shouldDeferControllerUpdateTerminalStatus('cancelled', false, false)).toBe(false)
    expect(shouldDeferControllerUpdateTerminalStatus('downloading', true, false)).toBe(false)
  })

  it('shows the pending info toast during an update on TypeError', () => {
    expect(controllerUpdatePendingToast(true, new TypeError('Failed to fetch'))).toEqual({ message: CONTROLLER_UPDATE_PENDING_MESSAGE, kind: 'info' })
  })

  it('shows the pending info toast during an update on network failures', () => {
    expect(controllerUpdatePendingToast(true, new Error('NetworkError when attempting to fetch resource'))).toEqual({ message: CONTROLLER_UPDATE_PENDING_MESSAGE, kind: 'info' })
    expect(controllerUpdatePendingToast(true, 'Failed to fetch')).toEqual({ message: CONTROLLER_UPDATE_PENDING_MESSAGE, kind: 'info' })
  })

  it.each(['Bad Gateway', 'Service Unavailable', 'Gateway Timeout', '502 Bad Gateway'])('shows the pending info toast for gateway unavailability: %s', (message) => {
    expect(controllerUpdatePendingToast(true, new Error(message))).toEqual({ message: CONTROLLER_UPDATE_PENDING_MESSAGE, kind: 'info' })
  })

  it('keeps the normal failure path outside an update', () => {
    expect(controllerUpdatePendingToast(false, new TypeError('Failed to fetch'))).toBeNull()
    expect(controllerUpdatePendingToast(false, new Error('Bad Gateway'))).toBeNull()
  })

  it('recognizes gateway unavailability by HTTP status even when the message is generic', () => {
    const error = new Error('操作失败，请稍后重试')
    ;(error as any).status = 502
    expect(controllerUpdatePendingToast(true, error)).toEqual({ message: CONTROLLER_UPDATE_PENDING_MESSAGE, kind: 'info' })
    for (const status of [502, 503, 504]) {
      const statusError = new Error('')
      ;(statusError as any).status = status
      expect(controllerUpdatePendingToast(true, statusError)).toEqual({ message: CONTROLLER_UPDATE_PENDING_MESSAGE, kind: 'info' })
    }
  })

  it('does not treat other HTTP statuses as expected disconnects', () => {
    for (const status of [401, 403, 500, 5024]) {
      const error = new Error('操作失败，请稍后重试')
      ;(error as any).status = status
      expect(controllerUpdatePendingToast(true, error)).toBeNull()
    }
  })

  it('does not mask real errors during an update', () => {
    expect(controllerUpdatePendingToast(true, new Error('forbidden'))).toBeNull()
    expect(controllerUpdatePendingToast(true, new Error('subscription_age_policy must be optional or required'))).toBeNull()
  })

  it('treats only active update phases as in progress', () => {
    expect(isControllerUpdateInProgressStatus('checking')).toBe(true)
    expect(isControllerUpdateInProgressStatus('preflight')).toBe(true)
    expect(isControllerUpdateInProgressStatus('backing_up')).toBe(true)
    expect(isControllerUpdateInProgressStatus('restarting')).toBe(true)
    expect(isControllerUpdateInProgressStatus('verifying')).toBe(true)
    expect(isControllerUpdateInProgressStatus('downloading')).toBe(true)
    expect(isControllerUpdateInProgressStatus('ready')).toBe(true)
    expect(isControllerUpdateInProgressStatus('installing')).toBe(true)
    expect(isControllerUpdateInProgressStatus('cancelling')).toBe(true)
    expect(isControllerUpdateInProgressStatus('current')).toBe(false)
    expect(isControllerUpdateInProgressStatus('failed')).toBe(false)
    expect(isControllerUpdateInProgressStatus(undefined)).toBe(false)
  })

  it('prefers the orchestration phase over updater status', () => {
    expect(controllerUpdateDisplayPhase({ status: 'ready', operation: { active: true, phase: 'backing_up' } })).toBe('backing_up')
    expect(controllerUpdateDisplayPhase({ status: 'available', operation: { active: false, phase: 'idle' } })).toBe('available')
  })

  it('distinguishes failed background cancellation from a requested cancellation', () => {
    expect(isControllerUpdateFailedStatus('cancelled', '创建数据库备份失败，已取消更新')).toBe(true)
    expect(isControllerUpdateFailedStatus('cancelled', '')).toBe(false)
    expect(isControllerUpdateFailedStatus('failed')).toBe(true)
    expect(isControllerUpdateFailedStatus('unavailable')).toBe(true)
  })
})
