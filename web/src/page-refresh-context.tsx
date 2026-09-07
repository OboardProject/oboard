import React from 'react'

import type { PageRefreshHandler } from './page-refresh'

type PageRefreshContextValue = {
  register: (handler: PageRefreshHandler) => () => void
  refresh: () => Promise<void>
  refreshing: boolean
}

const PageRefreshContext = React.createContext<PageRefreshContextValue | null>(null)

export function PageRefreshProvider({ register, refresh, refreshing, children }: PageRefreshContextValue & { children: React.ReactNode }) {
  const value = React.useMemo(() => ({ register, refresh, refreshing }), [register, refresh, refreshing])
  return <PageRefreshContext.Provider value={value}>{children}</PageRefreshContext.Provider>
}

export function useRegisterPageRefresh(handler: PageRefreshHandler) {
  const context = React.useContext(PageRefreshContext)
  const handlerRef = React.useRef(handler)
  handlerRef.current = handler
  React.useEffect(() => {
    if (!context) return
    return context.register(() => handlerRef.current())
  }, [context])
}

export function usePageRefreshAction() {
  return React.useContext(PageRefreshContext)
}
