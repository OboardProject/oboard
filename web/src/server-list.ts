export type ServerSortMode = 'created' | 'country' | 'custom'
export type ServerStatusFilter = 'all' | 'online' | 'offline' | 'unenrolled'

export type ServerListItem = {
  id: number
  name?: string
  status?: string
  agent_id?: string
  entry_address?: string
  public_ipv4?: string
  public_ipv6?: string
  interface_ipv6?: string
  created_at?: string
}

type RegionResolver<T> = (server: T) => { code: string; label: string }

function createdTime(server: ServerListItem) {
  const value = Date.parse(String(server.created_at || ''))
  return Number.isFinite(value) ? value : 0
}

function compareCreated(a: ServerListItem, b: ServerListItem) {
  return createdTime(b) - createdTime(a) || Number(b.id) - Number(a.id)
}

function compareCountry<T extends ServerListItem>(a: T, b: T, resolveRegion: RegionResolver<T>) {
  const regionA = resolveRegion(a)
  const regionB = resolveRegion(b)
  if (!regionA.code && regionB.code) return 1
  if (regionA.code && !regionB.code) return -1
  return regionA.label.localeCompare(regionB.label, 'zh-CN') || compareCreated(a, b)
}

export function normalizeServerOrder(value: unknown) {
  if (!Array.isArray(value)) return []
  const seen = new Set<number>()
  return value.flatMap(item => {
    const id = Number(item)
    if (!Number.isSafeInteger(id) || id <= 0 || seen.has(id)) return []
    seen.add(id)
    return [id]
  })
}

export function reconcileCustomServerOrder<T extends ServerListItem>(servers: T[], order: number[], resolveRegion: RegionResolver<T>) {
  const serverByID = new Map(servers.map(server => [Number(server.id), server]))
  const next = normalizeServerOrder(order).filter(id => serverByID.has(id))
  const existing = new Set(next)
  const missing = servers.filter(server => !existing.has(Number(server.id))).sort((a, b) => compareCountry(a, b, resolveRegion))

  for (const server of missing) {
    const region = resolveRegion(server)
    let insertAt = -1
    for (let index = next.length - 1; index >= 0; index -= 1) {
      const current = serverByID.get(next[index])
      if (current && resolveRegion(current).code === region.code) {
        insertAt = index + 1
        break
      }
    }
    if (insertAt < 0) {
      insertAt = next.findIndex(id => {
        const current = serverByID.get(id)
        return current ? compareCountry(server, current, resolveRegion) < 0 : false
      })
    }
    if (insertAt < 0) insertAt = next.length
    next.splice(insertAt, 0, Number(server.id))
  }
  return next
}

export function sortServerList<T extends ServerListItem>(servers: T[], mode: ServerSortMode, customOrder: number[], resolveRegion: RegionResolver<T>) {
  const items = [...servers]
  if (mode === 'created') return items.sort(compareCreated)
  if (mode === 'country') return items.sort((a, b) => compareCountry(a, b, resolveRegion))

  const order = reconcileCustomServerOrder(items, customOrder, resolveRegion)
  const positions = new Map(order.map((id, index) => [id, index]))
  return items.sort((a, b) => (positions.get(Number(a.id)) ?? Number.MAX_SAFE_INTEGER) - (positions.get(Number(b.id)) ?? Number.MAX_SAFE_INTEGER))
}

export function filterServerList<T extends ServerListItem>(servers: T[], query: string, status: ServerStatusFilter, regionCode: string, resolveRegion: RegionResolver<T>) {
  const normalizedQuery = query.trim().toLocaleLowerCase('zh-CN')
  return servers.filter(server => {
    const normalizedStatus = String(server.status || '').trim().toLowerCase()
    if (status === 'online' && normalizedStatus !== 'online') return false
    if (status === 'offline' && normalizedStatus !== 'offline') return false
    if (status === 'unenrolled' && String(server.agent_id || '').trim()) return false

    const region = resolveRegion(server)
    if (regionCode !== 'all' && region.code !== regionCode) return false
    if (!normalizedQuery) return true
    return [
      server.name,
      `#${server.id}`,
      `server-${server.id}`,
      server.entry_address,
      server.public_ipv4,
      server.public_ipv6,
      server.interface_ipv6,
      server.agent_id,
      region.code,
      region.label,
    ].some(value => String(value || '').toLocaleLowerCase('zh-CN').includes(normalizedQuery))
  })
}

export function moveServerOrder(order: number[], sourceID: number, targetID: number, placement: 'before' | 'after' = 'before') {
  if (sourceID === targetID) return normalizeServerOrder(order)
  const next = normalizeServerOrder(order)
  const sourceIndex = next.indexOf(sourceID)
  if (sourceIndex < 0 || !next.includes(targetID)) return next
  next.splice(sourceIndex, 1)
  const targetIndex = next.indexOf(targetID)
  next.splice(targetIndex + (placement === 'after' ? 1 : 0), 0, sourceID)
  return next
}
