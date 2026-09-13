export type PageRefreshHandler = () => Promise<void> | void

// A handler may declare which resources it re-reads. A mutation then refreshes
// only the handlers that cover what it changed, instead of every registered
// handler on the page.
export type PageRefreshOptions = { resources?: string[] }

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
  const handlers = new Map<PageRefreshHandler, string[]>()
  const run = async (selected: PageRefreshHandler[]) => {
    await Promise.all(selected.map(handler => Promise.resolve().then(handler).catch(() => undefined)))
  }
  return {
    register(handler: PageRefreshHandler, options: PageRefreshOptions = {}) {
      handlers.set(handler, options.resources || [])
      return () => { handlers.delete(handler) }
    },
    async runAll() {
      await run(Array.from(handlers.keys()))
    },
    // runFor refreshes the handlers that read any of these resources. A handler
    // that declared none is always included: it has not said what it reads, so
    // it cannot be ruled out.
    async runFor(resources: string[]) {
      if (resources.length === 0) return
      const wanted = new Set(resources)
      const selected: PageRefreshHandler[] = []
      for (const [handler, declared] of handlers) {
        if (declared.length === 0 || declared.some(resource => wanted.has(resource))) selected.push(handler)
      }
      await run(selected)
    },
  }
}
