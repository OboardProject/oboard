// One place that owns a panel mutation from submit to settled.
//
// Three states are kept apart on purpose:
//   confirmed  - what the server last told us.
//   optimistic - what this client did and is still waiting on.
//   unknown    - what this client sent but never got an answer for.
//
// The third one is the reason this module exists. A request whose response was
// lost is not a failure: the write may well have landed. Reporting it as failed
// invites the operator to repeat an operation that already happened. Such an
// operation is recorded as unconfirmed and the resources it touched are
// re-read; what the panel shows meanwhile is the last state the server
// confirmed, because that was true at some point, while the optimistic state is
// only a guess about a request nobody answered.

export type MutationOutcome = 'applied' | 'rejected' | 'unknown' | 'superseded'

export type MutationResource = string

export type MutationSubmission<T> = {
  // key identifies the thing being changed, e.g. `server:12`. Results for the
  // same key are ordered: a slower earlier response never lands on top of a
  // faster later one.
  key: string
  // resources are what has to be re-read once this settles, e.g. ['servers'].
  resources?: MutationResource[]
  // optimistic applies the expected result immediately and returns the undo to
  // run if this operation - and only this operation - has to be taken back.
  optimistic?: () => () => void
  run: () => Promise<T>
}

export type MutationResult<T> = {
  outcome: MutationOutcome
  id: string
  value?: T
  error?: unknown
}

export type MutationState = {
  pending: number
  unknown: number
}

export type MutationCoordinatorOptions = {
  // refresh re-reads a set of resources. The coordinator only ever asks for the
  // resources an operation declared, so one save does not reload the panel.
  refresh?: (resources: MutationResource[]) => Promise<void> | void
  onStateChange?: (state: MutationState) => void
  newID?: () => string
}

// isDefiniteFailure reports whether the write is known not to have happened.
// A 4xx is that answer: the request was refused before it changed anything. A
// 5xx, a 408, or no status at all is not - the write may have committed and the
// answer got lost on the way back - so those stay unknown and are resolved by
// re-reading. This matches how server create and delete already reconcile an
// ambiguous response.
export function isDefiniteFailure(error: unknown): boolean {
  const status = Number((error as { status?: unknown } | null)?.status || 0)
  return status >= 400 && status < 500 && status !== 408
}

export function createMutationCoordinator(options: MutationCoordinatorOptions = {}) {
  const newID = options.newID || (() => Math.random().toString(36).slice(2) + Date.now().toString(36))
  // settled[key] is the sequence of the newest operation for that key whose
  // result has already been applied.
  const settled = new Map<string, number>()
  const issued = new Map<string, number>()
  const unknownOperations = new Map<string, { key: string; resources: MutationResource[] }>()
  let pending = 0

  const notify = () => {
    options.onStateChange?.({ pending, unknown: unknownOperations.size })
  }

  const refresh = async (resources: MutationResource[]) => {
    if (!options.refresh || resources.length === 0) return
    await options.refresh(resources)
  }

  async function submit<T>(submission: MutationSubmission<T>): Promise<MutationResult<T>> {
    const id = newID()
    const resources = submission.resources || []
    const sequence = (issued.get(submission.key) || 0) + 1
    issued.set(submission.key, sequence)
    const undo = submission.optimistic?.()
    pending++
    notify()
    let value: T | undefined
    let failure: unknown
    try {
      value = await submission.run()
    } catch (error) {
      failure = error
    }
    pending--
    const newer = (settled.get(submission.key) || 0) >= sequence
    if (newer) {
      // A later operation on the same object already landed. This result
      // describes a state the panel has moved past, so it is dropped. Its undo
      // is deliberately not run: the newer operation's optimistic state is what
      // the panel shows, and restoring what this one captured before it started
      // would put the object back to a value two operations ago.
      unknownOperations.delete(id)
      notify()
      return { outcome: 'superseded', id }
    }
    settled.set(submission.key, sequence)
    if (failure === undefined) {
      unknownOperations.delete(id)
      notify()
      await refresh(resources)
      return { outcome: 'applied', id, value }
    }
    if (isDefiniteFailure(failure)) {
      // The server rejected this one. Only this one is taken back.
      undo?.()
      notify()
      await refresh(resources)
      return { outcome: 'rejected', id, error: failure }
    }
    // No answer. Fall back to the last confirmed state, record that this
    // operation's outcome is unknown, and go find out.
    undo?.()
    unknownOperations.set(id, { key: submission.key, resources })
    notify()
    await refresh(resources)
    return { outcome: 'unknown', id, error: failure }
  }

  return {
    submit,
    state(): MutationState {
      return { pending, unknown: unknownOperations.size }
    },
    // confirm resolves an unknown operation once its resources have been
    // re-read, so the panel stops warning about an outcome it now knows.
    confirm(id: string) {
      if (unknownOperations.delete(id)) notify()
    },
    unknownResources(): MutationResource[] {
      const out = new Set<MutationResource>()
      for (const entry of unknownOperations.values()) {
        for (const resource of entry.resources) out.add(resource)
      }
      return Array.from(out)
    },
    reset() {
      settled.clear()
      issued.clear()
      unknownOperations.clear()
      pending = 0
      notify()
    },
  }
}

export type MutationCoordinator = ReturnType<typeof createMutationCoordinator>
