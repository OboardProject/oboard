import { isIndeterminateMutationFailure } from './mutation-outcome'

export type MutationOutcome = 'applied' | 'rejected' | 'unknown' | 'superseded'
export type MutationResource = string

export type MutationSubmission<T> = {
  key: string
  resources?: MutationResource[]
  // Undo must restore only this operation's fields, never an entire collection.
  optimistic?: () => () => void
  run: () => Promise<T>
  // Commands are single-flight by default. Only explicitly ordered, independent
  // save snapshots may queue; this is not server-side idempotency or conflict control.
  concurrency?: 'single-flight' | 'queue'
}

export type MutationResult<T> = {
  outcome: MutationOutcome
  id: string
  value?: T
  error?: unknown
}

export type MutationState = { pending: number; unknown: number }

export type MutationCoordinatorOptions = {
  refresh?: (resources: MutationResource[]) => Promise<void> | void
  onRefreshError?: (error: unknown, resources: MutationResource[]) => void
  onStateChange?: (state: MutationState) => void
  newID?: () => string
}

export function isDefiniteFailure(error: unknown): boolean {
  return !isIndeterminateMutationFailure(error)
}

export function createMutationCoordinator(options: MutationCoordinatorOptions = {}) {
  const newID = options.newID || (() => Math.random().toString(36).slice(2) + Date.now().toString(36))
  const lanes = new Map<string, Promise<void>>()
  const unknownOperations = new Map<string, { key: string; resources: MutationResource[]; error: unknown }>()
  let generation = 0
  let pending = 0
  const notify = () => options.onStateChange?.({ pending, unknown: unknownOperations.size })

  const refresh = (resources: MutationResource[]) => {
    if (!options.refresh || resources.length === 0) return
    // Read failure cannot undo a confirmed write or postpone releasing its form.
    try {
      void Promise.resolve(options.refresh(resources)).catch(error => options.onRefreshError?.(error, resources))
    } catch (error) {
      options.onRefreshError?.(error, resources)
    }
  }

  function submit<T>(submission: MutationSubmission<T>): Promise<MutationResult<T>> {
    const id = newID()
    const previous = lanes.get(submission.key)
    if (previous && submission.concurrency !== 'queue') {
      return Promise.resolve({ outcome: 'superseded', id })
    }
    const submittedGeneration = generation
    const resources = submission.resources || []
    pending++
    notify()
    const execute = async (): Promise<MutationResult<T>> => {
      if (previous) await previous
      if (submittedGeneration !== generation) return { outcome: 'superseded', id }
      const unresolved = Array.from(unknownOperations.entries()).find(([, entry]) => entry.key === submission.key)
      if (unresolved) {
        pending--
        notify()
        return { outcome: 'unknown', id: unresolved[0], error: unresolved[1].error }
      }
      let undo: (() => void) | undefined
      let value: T | undefined
      let failure: unknown
      let failed = false
      try {
        undo = submission.optimistic?.()
        value = await submission.run()
      } catch (error) {
        failed = true
        failure = error
      }
      // A reset denotes a new account/component lifetime, not cancellation of
      // the server command. Old completions may not touch that lifetime.
      if (submittedGeneration !== generation) return { outcome: 'superseded', id }
      pending--
      if (!failed) {
        notify()
        refresh(resources)
        return { outcome: 'applied', id, value }
      }
      undo?.()
      const outcome = isDefiniteFailure(failure) ? 'rejected' : 'unknown'
      if (outcome === 'unknown') unknownOperations.set(id, { key: submission.key, resources, error: failure })
      notify()
      refresh(resources)
      return { outcome, id, error: failure }
    }
    // Reserve the lane before executing user code, including optimistic callbacks.
    let tail: Promise<void>
    const result = Promise.resolve().then(execute).finally(() => {
      if (lanes.get(submission.key) === tail) lanes.delete(submission.key)
    })
    tail = result.then(() => undefined, () => undefined)
    lanes.set(submission.key, tail)
    return result
  }

  return {
    submit,
    state(): MutationState {
      return { pending, unknown: unknownOperations.size }
    },
    // Only an authoritative result for this operation resolves uncertainty.
    // A refreshed entity that merely resembles the request is not evidence.
    confirm(id: string, evidence: { operationID: string; outcome: 'applied' | 'rejected' }) {
      if (evidence.operationID !== id) return false
      const removed = unknownOperations.delete(id)
      if (removed) notify()
      return removed
    },
    unknownResources(): MutationResource[] {
      return [...new Set(Array.from(unknownOperations.values()).flatMap(entry => entry.resources))]
    },
    reset() {
      generation++
      lanes.clear()
      unknownOperations.clear()
      pending = 0
      notify()
    },
  }
}

export type MutationCoordinator = ReturnType<typeof createMutationCoordinator>

export function describeMutationOutcome(outcome: MutationResult<unknown>, action: string): string {
  const reason = (outcome.error as { message?: string } | undefined)?.message || String(outcome.error ?? '')
  if (outcome.outcome === 'unknown') {
    return `${action}的结果未知${reason ? `（${reason}）` : ''}。请核对当前状态与操作记录，确认前不要重复提交`
  }
  return `${action}失败${reason ? `：${reason}` : ''}`
}
