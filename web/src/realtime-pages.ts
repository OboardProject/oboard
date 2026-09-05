import type { RealtimeEvent } from './realtime'

export const realtimeResourcePages: Record<string, string[]> = {
  user_overview: ['dashboard'],
  account: ['account'],
  notifications: ['notifications'],
  subscriptions: ['nodes', 'account'],
  servers: ['return-latency', 'dashboard', 'servers', 'proxy-paths', 'nodes', 'tasks', 'audit', 'settings'],
  server_runtime: ['return-latency', 'dashboard', 'servers'],
  server_metrics: ['dashboard', 'servers'],
  traffic: ['dashboard', 'servers', 'users', 'nodes', 'account'],
  tasks: ['dashboard'],
  deployments: ['dashboard', 'servers', 'proxy-paths', 'tasks'],
  configuration: ['dashboard', 'servers', 'proxy-paths', 'users', 'plans', 'nodes', 'dns', 'tasks'],
  probes: ['return-latency', 'servers', 'proxy-paths', 'dns', 'mtu', 'port-forwards', 'tasks'],
  topology: ['servers', 'proxy-paths', 'plans', 'nodes', 'settings'],
  audit: ['dashboard', 'audit'],
  mtu: ['servers', 'mtu'],
  port_forwards: ['proxy-paths', 'port-forwards'],
  tunnels: ['proxy-paths', 'tunnels'],
  users: ['users', 'plans', 'subscriptions', 'account', 'audit'],
  dns: ['dns', 'dns-records', 'servers', 'settings'],
  settings: ['dashboard', 'servers', 'subscriptions', 'settings'],
  backups: ['settings'],
  controller_update: ['settings'],
  automation: ['automation'],
}

export const realtimePageRefreshDelayMS = 600

export type RealtimeRefreshScheduler = (callback: () => void, delayMS: number) => number

export function scheduleRealtimeRefresh({
  page,
  activePage,
  visible,
  dirtyPages,
  hasPendingRequest,
  schedule,
  refresh,
}: {
  page: string
  activePage: string
  visible: boolean
  dirtyPages: Set<string>
  hasPendingRequest: boolean
  schedule: RealtimeRefreshScheduler
  refresh: (page: string) => void
}): number | undefined {
  if (page !== activePage || !visible) return undefined
  if (hasPendingRequest) {
    dirtyPages.delete(page)
    return undefined
  }
  return schedule(() => {
    if (dirtyPages.delete(page)) refresh(page)
  }, realtimePageRefreshDelayMS)
}

export function realtimeInvalidatedPages(event: RealtimeEvent, activePage: string, cachedPages: Iterable<string>): Set<string> {
  const pages = new Set<string>()
  const resync = event.type === 'resync_required' || (event.type === 'ready' && event.reconnected === true)
  if (resync || event.resources?.includes('all')) {
    for (const page of cachedPages) pages.add(page)
    pages.add(activePage)
    return pages
  }
  if (event.type !== 'invalidate') return pages
  for (const resource of event.resources || []) {
    for (const page of realtimeResourcePages[resource] || []) pages.add(page)
  }
  return pages
}
