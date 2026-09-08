import { useEffect, useRef } from 'react'

import { PageDataRequestCoordinator } from './page-data'
import { useDocumentVisible } from './visibility'

/**
 * Coalesced read helper: reuses PageDataRequestCoordinator so page-data and
 * ad-hoc reads share one in-flight/abort model.
 *
 * - Same query key keeps only one in-flight request
 * - cancel/key change aborts obsolete reads only
 * - Never caches response bodies (do not put subscription content or one-time
 *   tokens through this path; submit writes with client.request directly)
 */
export type CoalescedReadCoordinator<T> = PageDataRequestCoordinator<T>
export const CoalescedReadCoordinator = PageDataRequestCoordinator

export type CoalescedReadHandlers<T> = {
  onSuccess: (data: T) => void
  onError?: (error: unknown) => void
  onStart?: () => void
  onSettled?: () => void
}

export type CoalescedReadOptions = {
  /** When false, skip the request entirely. */
  enabled?: boolean
  /** When true, fetch even while the document is hidden. */
  essential?: boolean
}

/**
 * Runs a coalesced JSON/read request keyed by `key`.
 * Hidden pages skip nonessential polls; becoming visible starts one reconcile.
 * Cleanup aborts the in-flight read for this key without touching writes.
 */
export function useCoalescedReadRequest<T>(
  key: string,
  load: (signal: AbortSignal) => Promise<T>,
  handlers: CoalescedReadHandlers<T>,
  options?: CoalescedReadOptions,
) {
  const visible = useDocumentVisible()
  const coordinatorRef = useRef<CoalescedReadCoordinator<T> | null>(null)
  if (!coordinatorRef.current) coordinatorRef.current = new CoalescedReadCoordinator<T>()
  const coordinator = coordinatorRef.current
  const loadRef = useRef(load)
  loadRef.current = load
  const handlersRef = useRef(handlers)
  handlersRef.current = handlers

  const enabled = options?.enabled !== false
  const essential = Boolean(options?.essential)
  const shouldRun = enabled && Boolean(key) && (essential || visible)

  useEffect(() => {
    if (!shouldRun) return
    let cancelled = false
    handlersRef.current.onStart?.()
    const request = coordinator.request(key, signal => loadRef.current(signal))
    void request.then(
      response => {
        if (cancelled || !coordinator.isCurrent(key, response)) return
        handlersRef.current.onSuccess(response.data)
      },
      (error: unknown) => {
        if (cancelled) return
        if (isAbortError(error)) return
        handlersRef.current.onError?.(error)
      },
    ).finally(() => {
      if (!cancelled) handlersRef.current.onSettled?.()
    })
    return () => {
      cancelled = true
      coordinator.cancel(key)
    }
  }, [coordinator, key, shouldRun])
}

function isAbortError(error: unknown) {
  if (!error || typeof error !== 'object') return false
  const name = (error as { name?: string }).name
  return name === 'AbortError' || name === 'CanceledError'
}
