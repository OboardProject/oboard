export type EditSubmission<T> = Readonly<{ session: number; key: string; revision: number; draft: T }>
export type EditSessionState<T> = Readonly<{
  key: string | null
  draft: T | null
  dirty: boolean
  submitting: boolean
  unknown: boolean
}>

// Drafts live only in memory. Call reset at an account/permission boundary.
export function createEditSession<T>(options: { clone: (value: T) => T; equal: (left: T, right: T) => boolean }) {
  let generation = 0
  let revision = 0
  let baseline: T | null = null
  let state: EditSessionState<T> = { key: null, draft: null, dirty: false, submitting: false, unknown: false }
  const pending = new Map<string, EditSubmission<T>>()
  const uncertain = new Set<string>()
  const listeners = new Set<() => void>()
  const publish = (next: EditSessionState<T>) => {
    state = next
    listeners.forEach(listener => listener())
  }
  const current = (submission: EditSubmission<T>) => submission.session === generation && submission.key === state.key
  return {
    getSnapshot: () => state,
    subscribe(listener: () => void) {
      listeners.add(listener)
      return () => { listeners.delete(listener) }
    },
    begin(key: string, draft: T) {
      generation++
      revision = 0
      baseline = options.clone(draft)
      publish({ key, draft: options.clone(draft), dirty: false, submitting: pending.has(key), unknown: uncertain.has(key) })
    },
    change(draft: T) {
      if (state.key === null || baseline === null) return
      revision++
      publish({ ...state, draft: options.clone(draft), dirty: !options.equal(draft, baseline) })
    },
    capture(): EditSubmission<T> | null {
      if (state.key === null || state.draft === null || state.submitting || state.unknown) return null
      const submission = { session: generation, key: state.key, revision, draft: options.clone(state.draft) }
      pending.set(state.key, submission)
      publish({ ...state, submitting: true })
      return submission
    },
    isCurrent: current,
    settle(submission: EditSubmission<T>, outcome: 'applied' | 'rejected' | 'unknown', normalized?: T): { current: boolean; canClose: boolean } {
      if (pending.get(submission.key) !== submission) return { current: false, canClose: false }
      pending.delete(submission.key)
      if (outcome === 'unknown') uncertain.add(submission.key)
      if (!current(submission)) {
        // A reopened view of the same object still needs its operation status,
        // but never its previous session's draft or automatic close action.
        if (state.key === submission.key) publish({ ...state, submitting: false, unknown: uncertain.has(submission.key) })
        return { current: false, canClose: false }
      }
      const unchanged = revision === submission.revision
      if (outcome === 'applied') baseline = options.clone(normalized ?? submission.draft)
      const draft = outcome === 'applied' && unchanged ? options.clone(baseline!) : state.draft
      publish({ ...state, draft, submitting: false, unknown: outcome === 'unknown', dirty: draft !== null && baseline !== null && !options.equal(draft, baseline) })
      return { current: true, canClose: outcome === 'applied' && unchanged && !state.dirty }
    },
    close() {
      generation++
      baseline = null
      publish({ key: null, draft: null, dirty: false, submitting: false, unknown: false })
    },
    reset() {
      generation++
      baseline = null
      pending.clear()
      uncertain.clear()
      publish({ key: null, draft: null, dirty: false, submitting: false, unknown: false })
    },
  }
}
