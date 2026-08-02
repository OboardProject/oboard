import { describe, expect, it } from 'vitest'

import { filterDNSBenchmarkGroups, groupDNSBenchmarkResults } from './dns-benchmark-history'

type Record = { id: number; server_id: number; created_at: string; status: string }

const records: Record[] = [
  { id: 2, server_id: 10, created_at: '2026-08-01T08:00:00Z', status: 'failed' },
  { id: 4, server_id: 20, created_at: '2026-08-02T09:00:00Z', status: 'succeeded' },
  { id: 3, server_id: 10, created_at: '2026-08-02T08:00:00Z', status: 'succeeded' },
  { id: 1, server_id: 30, created_at: 'not-a-time', status: 'failed' },
]

describe('DNS benchmark history', () => {
  it('groups by server ID and sorts groups and records by newest result', () => {
    const groups = groupDNSBenchmarkResults(records, [
      { id: 10, name: 'Tokyo Edge' },
      { id: 20, name: 'Singapore Edge' },
    ])

    expect(groups.map(group => group.serverID)).toEqual([20, 10, 30])
    expect(groups[1].records.map(record => record.id)).toEqual([3, 2])
    expect(groups[2].serverName).toBe('#30')
  })

  it('filters groups by server name or numeric ID', () => {
    const groups = groupDNSBenchmarkResults(records, [
      { id: 10, name: 'Tokyo Edge' },
      { id: 20, name: 'Singapore Edge' },
    ])

    expect(filterDNSBenchmarkGroups(groups, 'TOKYO').map(group => group.serverID)).toEqual([10])
    expect(filterDNSBenchmarkGroups(groups, '#20').map(group => group.serverID)).toEqual([20])
    expect(filterDNSBenchmarkGroups(groups, '30').map(group => group.serverID)).toEqual([30])
    expect(filterDNSBenchmarkGroups(groups, 'missing')).toEqual([])
  })
})
