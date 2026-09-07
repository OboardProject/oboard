export type PageRefreshHandler = () => Promise<void> | void

const LIVE_SERVER_REFRESH_PAGES = new Set([
  'dashboard',
  'servers',
  'return-latency',
  'proxy-paths',
  'tasks',
  'nodes',
  'settings',
  'audit',
])

export function pageRefreshIncludesLiveServers(page: string) {
  return LIVE_SERVER_REFRESH_PAGES.has(page)
}

export function createPageRefreshRegistry() {
  const handlers = new Set<PageRefreshHandler>()
  return {
    register(handler: PageRefreshHandler) {
      handlers.add(handler)
      return () => { handlers.delete(handler) }
    },
    async runAll() {
      await Promise.all(Array.from(handlers, handler => Promise.resolve().then(handler).catch(() => undefined)))
    },
  }
}
