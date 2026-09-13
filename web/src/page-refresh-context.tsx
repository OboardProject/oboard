import React from 'react'

import type { PageRefreshHandler, PageRefreshOptions } from './page-refresh'

type PageRefreshContextValue = {
  register: (handler: PageRefreshHandler, options?: PageRefreshOptions) => () => void
  refresh: () => Promise<void>
  refreshing: boolean
  // refreshResources re-reads only what a mutation actually changed.
  refreshResources?: (resources: string[]) => Promise<void>
}

const PageRefreshContext = React.createContext<PageRefreshContextValue | null>(null)

export function PageRefreshProvider({ register, refresh, refreshing, refreshResources, children }: PageRefreshContextValue & { children: React.ReactNode }) {
  const value = React.useMemo(() => ({ register, refresh, refreshing, refreshResources }), [register, refresh, refreshing, refreshResources])
  return <PageRefreshContext.Provider value={value}>{children}</PageRefreshContext.Provider>
}

export function useRegisterPageRefresh(handler: PageRefreshHandler, resources?: string[]) {
  const context = React.useContext(PageRefreshContext)
  const handlerRef = React.useRef(handler)
  handlerRef.current = handler
  const declared = resources ? resources.join(',') : ''
  React.useEffect(() => {
    if (!context) return
    return context.register(() => handlerRef.current(), { resources: declared ? declared.split(',') : [] })
  }, [context, declared])
}

export function usePageRefreshAction() {
  return React.useContext(PageRefreshContext)
}

// useRefreshResources returns a partial refresh for the resources a mutation
// changed, or null when the component is rendered outside a provider so the
// caller can fall back to re-reading its own data.
export function useRefreshResources(): ((resources: string[]) => Promise<void>) | null {
  const context = React.useContext(PageRefreshContext)
  return React.useMemo(() => {
    if (!context) return null
    return async (resources: string[]) => {
      if (context.refreshResources) {
        await context.refreshResources(resources)
        return
      }
      await context.refresh()
    }
  }, [context])
}
