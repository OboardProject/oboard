import { describe, expect, it } from 'vitest'

import { CONTROLLER_UPDATE_PENDING_MESSAGE, controllerUpdatePendingToast, isControllerUpdateInProgressStatus } from './controller-update'

describe('controller update pending toast', () => {
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

  it('does not mask real errors during an update', () => {
    expect(controllerUpdatePendingToast(true, new Error('forbidden'))).toBeNull()
    expect(controllerUpdatePendingToast(true, new Error('subscription_age_policy must be optional or required'))).toBeNull()
  })

  it('treats only active update phases as in progress', () => {
    expect(isControllerUpdateInProgressStatus('downloading')).toBe(true)
    expect(isControllerUpdateInProgressStatus('ready')).toBe(true)
    expect(isControllerUpdateInProgressStatus('installing')).toBe(true)
    expect(isControllerUpdateInProgressStatus('cancelling')).toBe(true)
    expect(isControllerUpdateInProgressStatus('current')).toBe(false)
    expect(isControllerUpdateInProgressStatus('failed')).toBe(false)
    expect(isControllerUpdateInProgressStatus(undefined)).toBe(false)
  })
})
