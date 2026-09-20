import { useEffect, useMemo, useSyncExternalStore } from 'react'
import { createEditSession } from './edit-session'

export function useEditSession<T>(clone: (value: T) => T, equal: (left: T, right: T) => boolean) {
  // Supply stable clone/equal functions for the lifetime of an editor.
  const session = useMemo(() => createEditSession({ clone, equal }), [clone, equal])
  const state = useSyncExternalStore(session.subscribe, session.getSnapshot, session.getSnapshot)
  useEffect(() => () => session.reset(), [session])
  return { ...session, state }
}
