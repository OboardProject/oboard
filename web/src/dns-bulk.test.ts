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
    const results = await runDNSBulkAction([policy(1), policy(2)], { strategy: 'ipv6_only' }, 'test', async path => {
      calls.push(path)
      if (path === '/servers/1/dns-policy') throw new Error('列表不可用')
      if (path === '/servers/2/dns-test') {
        return { task: { status: 'failed', result_json: JSON.stringify({ error: 'Agent 离线' }) } }
      }
      return {}
    })

    expect(calls).not.toContain('/servers/1/dns-test')
    expect(results).toEqual([
      { serverID: 1, status: 'failed', message: '保存失败：列表不可用' },
      { serverID: 2, status: 'failed', message: '测试失败：Agent 离线' },
    ])
    expect(failedDNSBulkServerIDs(results)).toEqual([1, 2])
  })

  it('runs server operations serially and preserves result order', async () => {
    let active = 0
    let maximum = 0
    const policies = Array.from({ length: 7 }, (_, index) => policy(index + 1))
    const results = await runDNSBulkAction(policies, {}, 'test', async () => {
      active++
      maximum = Math.max(maximum, active)
      await new Promise(resolve => setTimeout(resolve, 1))
      active--
      return {}
    })

    expect(maximum).toBe(1)
    expect(results.map(result => result.serverID)).toEqual([1, 2, 3, 4, 5, 6, 7])
    expect(results.every(result => result.status === 'succeeded')).toBe(true)
  })

  it('does not save when no selected field changes the policy', async () => {
    const calls: string[] = []
    await runDNSBulkAction([policy(1)], {}, 'test', async path => {
      calls.push(path)
      return {}
    })
    await runDNSBulkAction([policy(2)], { strategy: 'prefer_ipv4' }, 'test', async path => {
      calls.push(path)
      return {}
    })

    expect(calls).toEqual(['/servers/1/dns-test', '/servers/2/dns-test'])
  })

  it('retries an idempotent policy save once after a transport failure', async () => {
    let attempts = 0
    const results = await runDNSBulkAction([policy(1)], { strategy: 'ipv6_only' }, 'save', async () => {
      attempts++
      if (attempts === 1) throw new TypeError('Failed to fetch')
      return {}
    })

    expect(attempts).toBe(2)
    expect(results).toEqual([{ serverID: 1, status: 'succeeded', message: '' }])
  })

  it('localizes a policy save transport failure after the retry is exhausted', async () => {
    let attempts = 0
    const results = await runDNSBulkAction([policy(1)], { strategy: 'ipv6_only' }, 'save', async () => {
      attempts++
      throw new TypeError('Failed to fetch')
    })

    expect(attempts).toBe(2)
    expect(results).toEqual([{
      serverID: 1,
      status: 'failed',
      message: '保存失败：无法连接控制器，请检查网络后重试',
    }])
  })

  it('does not retry a DNS test when its response status is unknown', async () => {
    let attempts = 0
    const results = await runDNSBulkAction([policy(1)], {}, 'test', async () => {
      attempts++
      throw new TypeError('Failed to fetch')
    })

    expect(attempts).toBe(1)
    expect(results).toEqual([{
      serverID: 1,
      status: 'failed',
      message: '测试状态未知：与控制器的连接中断，请先查看测试记录',
    }])
  })

  it('saves an unavailable server policy and skips its DNS test', async () => {
    const calls: string[] = []
    const results = await runDNSBulkAction([policy(1)], { hourlyTest: true }, 'test', async path => {
      calls.push(path)
      return {}
    }, () => '服务器离线，DNS 策略已保存，测试已跳过')

    expect(calls).toEqual(['/servers/1/dns-policy'])
    expect(results).toEqual([{
      serverID: 1,
      status: 'skipped',
      message: '服务器离线，DNS 策略已保存，测试已跳过',
    }])
    expect(failedDNSBulkServerIDs(results)).toEqual([])
  })

  it('treats a newly offline immediate task result as skipped', async () => {
    const results = await runDNSBulkAction([policy(1)], {}, 'test', async () => ({
      task: {
        status: 'failed',
        result_json: JSON.stringify({ error: '服务器离线，任务无法下发', offline: true }),
      },
    }))

    expect(results).toEqual([{
      serverID: 1,
      status: 'skipped',
      message: 'DNS 策略已保存，测试已跳过：服务器离线，任务无法下发',
    }])
    expect(failedDNSBulkServerIDs(results)).toEqual([])
  })
})
