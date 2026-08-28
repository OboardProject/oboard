import * as React from 'react'

type MutationTask = () => Promise<void>

export function usePlanMutationQueue() {
  const queueRef = React.useRef(Promise.resolve())
  const enqueue = React.useCallback((task: MutationTask) => {
    const wrapped = async () => {
      try {
        await task()
      } catch {
        // Swallow to keep queue alive; caller handles its own error UI
      }
    }
    queueRef.current = queueRef.current.then(wrapped)
    return queueRef.current
  }, [])
  return { enqueue }
}
