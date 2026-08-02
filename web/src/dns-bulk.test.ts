import { describe, expect, it } from 'vitest'

import {
  failedDNSBulkServerIDs,
  hasDNSBulkPatch,
  mergeDNSBulkPolicy,
  runDNSBulkAction,
  type DNSBulkPolicy,
} from './dns-bulk'

const policy = (serverID: number): DNSBulkPolicy => ({
  server_id: serverID,
  encrypted_list_id: 10,
  bootstrap_list_id: 20,
  strategy: 'prefer_ipv4',
  auto_test: 'never',
  test_interval_seconds: 900,
})

describe('DNS bulk settings', () => {
  it('preserves untouched fields and overrides only selected fields', () => {
    expect(mergeDNSBulkPolicy(policy(1), {})).toEqual({
      encrypted_list_id: 10,
      bootstrap_list_id: 20,
      strategy: 'prefer_ipv4',
      auto_test: 'never',
      test_interval_seconds: 900,
    })
    expect(mergeDNSBulkPolicy(policy(1), { encryptedListID: 30, strategy: 'ipv6_only', hourlyTest: true })).toEqual({
      encrypted_list_id: 30,
      bootstrap_list_id: 20,
      strategy: 'ipv6_only',
      auto_test: 'periodic',
      test_interval_seconds: 3600,
    })
    expect(mergeDNSBulkPolicy(policy(1), { hourlyTest: false }).auto_test).toBe('first_apply')
    expect(hasDNSBulkPatch({})).toBe(false)
    expect(hasDNSBulkPatch({ hourlyTest: false })).toBe(true)
  })

  it('skips checks after a save failure and returns immediate task failures', async () => {
    const calls: string[] = []
    const results = await runDNSBulkAction([policy(1), policy(2)], {}, 'test', async path => {
      calls.push(path)
      if (path === '/servers/1/dns-policy') throw new Error('列表不可用')
      if (path === '/servers/2/dns-test') {
        return { task: { status: 'failed', result_json: JSON.stringify({ error: 'Agent 离线' }) } }
      }
      return {}
    })

    expect(calls).not.toContain('/servers/1/dns-test')
    expect(results).toEqual([
      { serverID: 1, ok: false, error: '保存失败：列表不可用' },
      { serverID: 2, ok: false, error: '检查失败：Agent 离线' },
    ])
    expect(failedDNSBulkServerIDs(results)).toEqual([1, 2])
  })

  it('limits concurrent server operations and preserves result order', async () => {
    let active = 0
    let maximum = 0
    const policies = Array.from({ length: 7 }, (_, index) => policy(index + 1))
    const results = await runDNSBulkAction(policies, {}, 'save', async () => {
      active++
      maximum = Math.max(maximum, active)
      await new Promise(resolve => setTimeout(resolve, 1))
      active--
      return {}
    }, 4)

    expect(maximum).toBe(4)
    expect(results.map(result => result.serverID)).toEqual([1, 2, 3, 4, 5, 6, 7])
    expect(results.every(result => result.ok)).toBe(true)
  })
})
