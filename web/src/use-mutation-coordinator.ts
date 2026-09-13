import React from 'react'

import { createMutationCoordinator, type MutationCoordinator } from './mutation-coordinator'

export type MutationCoordinatorRefresh = (resources: string[]) => Promise<void>

// useMutationCoordinator gives a component one coordinator for its lifetime,
// wired to re-read only the resources an operation declared.
export function useMutationCoordinator(refresh?: MutationCoordinatorRefresh | null): MutationCoordinator {
  const refreshRef = React.useRef(refresh)
  refreshRef.current = refresh
  return React.useMemo(() => createMutationCoordinator({
    refresh: async resources => {
      const current = refreshRef.current
      if (!current) return
      await current(resources)
    },
  }), [])
}
