import { describe, expect, it } from 'vitest'
import { filterServerList, moveServerOrder, normalizeServerOrder, reconcileCustomServerOrder, sortServerList } from './server-list'

type Server = {
  id: number
  name: string
  status: string
  agent_id?: string
  public_ipv4?: string
  created_at?: string
  region: string
}

const servers: Server[] = [
  { id: 3, name: 'Tokyo', status: 'online', agent_id: 'a3', public_ipv4: '203.0.113.3', created_at: '2026-08-03T00:00:00Z', region: 'JP' },
  { id: 2, name: 'Los Angeles', status: 'offline', created_at: '2026-08-02T00:00:00Z', region: 'US' },
  { id: 1, name: 'Osaka', status: 'online', agent_id: 'a1', created_at: '2026-08-01T00:00:00Z', region: 'JP' },
]

const regions: Record<string, string> = { JP: '日本', US: '美国', DE: '德国' }
const resolveRegion = (server: Server) => ({ code: server.region, label: regions[server.region] || '地区待检测' })

describe('server list ordering', () => {
  it('normalizes persisted IDs', () => {
    expect(normalizeServerOrder([3, '2', 3, 0, 'bad'])).toEqual([3, 2])
  })

  it('sorts by newest creation time and then ID', () => {
    expect(sortServerList(servers, 'created', [], resolveRegion).map(server => server.id)).toEqual([3, 2, 1])
  })

  it('sorts by localized country and keeps newest servers first within a country', () => {
    expect(sortServerList(servers, 'country', [], resolveRegion).map(server => server.id)).toEqual([2, 3, 1])
  })

  it('inserts a new server after the last custom item from the same country', () => {
    const withNew = [...servers, { id: 4, name: 'Berlin', status: 'online', created_at: '2026-08-04T00:00:00Z', region: 'DE' }, { id: 5, name: 'Nagoya', status: 'online', created_at: '2026-08-05T00:00:00Z', region: 'JP' }]
    expect(reconcileCustomServerOrder(withNew, [1, 2, 3], resolveRegion)).toEqual([4, 1, 2, 3, 5])
  })

  it('moves a custom item relative to another item', () => {
    expect(moveServerOrder([1, 2, 3], 1, 3, 'after')).toEqual([2, 3, 1])
    expect(moveServerOrder([1, 2, 3], 3, 1)).toEqual([3, 1, 2])
  })
})

describe('server list filtering', () => {
  it('searches names, addresses, IDs and localized countries', () => {
    expect(filterServerList(servers, '203.0.113.3', 'all', 'all', resolveRegion).map(server => server.id)).toEqual([3])
    expect(filterServerList(servers, '#2', 'all', 'all', resolveRegion).map(server => server.id)).toEqual([2])
    expect(filterServerList(servers, '日本', 'all', 'all', resolveRegion).map(server => server.id)).toEqual([3, 1])
  })

  it('combines status and country filters', () => {
    expect(filterServerList(servers, '', 'online', 'JP', resolveRegion).map(server => server.id)).toEqual([3, 1])
    expect(filterServerList(servers, '', 'unenrolled', 'all', resolveRegion).map(server => server.id)).toEqual([2])
  })
})
