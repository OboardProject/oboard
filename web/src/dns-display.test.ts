import { describe, expect, it } from 'vitest'

import { dnsRecordDetail, dnsSelectionLabel, dnsTagListLabel } from './dns-display'

describe('DNS display formatting', () => {
  it('shows only the linked server for controller-generated record comments', () => {
    const comment = 'OBoard: 入口 9929-anytls-10477 / 服务器 9929'
    expect(dnsRecordDetail(comment, 300, '新名称')).toBe('服务器 新名称')
    expect(dnsRecordDetail(comment, 300)).toBe('服务器 9929')
  })

  it('preserves custom comments and TTL details', () => {
    expect(dnsRecordDetail('手动设置', 300, '东京')).toBe('手动设置 · 服务器 东京')
    expect(dnsRecordDetail('', 1)).toBe('TTL 1')
  })

  it('renders missing and empty selections as waiting for a test', () => {
    expect(dnsSelectionLabel(null)).toBe('等待测试')
    expect(dnsSelectionLabel(undefined)).toBe('等待测试')
    expect(dnsSelectionLabel([])).toBe('等待测试')
  })

  it('renders selected candidates and tolerates incomplete entries', () => {
    expect(dnsSelectionLabel([{ tag: 'cloudflare' }, { tag: 'google' }])).toBe('cloudflare · google')
    expect(dnsSelectionLabel([{ tag: '' }, {}])).toBe('等待测试')
  })

  it('renders missing benchmark tags with the requested fallback', () => {
    expect(dnsTagListLabel(null, '无可用项')).toBe('无可用项')
    expect(dnsTagListLabel([], '无可用项')).toBe('无可用项')
    expect(dnsTagListLabel(['cloudflare', 'google'], '无可用项')).toBe('cloudflare · google')
  })
})
