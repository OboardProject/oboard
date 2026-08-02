import { describe, expect, it } from 'vitest'

import { dnsSelectionLabel, dnsTagListLabel } from './dns-display'

describe('DNS display formatting', () => {
  it('renders missing and empty selections as waiting for a check', () => {
    expect(dnsSelectionLabel(null)).toBe('等待检查')
    expect(dnsSelectionLabel(undefined)).toBe('等待检查')
    expect(dnsSelectionLabel([])).toBe('等待检查')
  })

  it('renders selected candidates and tolerates incomplete entries', () => {
    expect(dnsSelectionLabel([{ tag: 'cloudflare' }, { tag: 'google' }])).toBe('cloudflare · google')
    expect(dnsSelectionLabel([{ tag: '' }, {}])).toBe('等待检查')
  })

  it('renders missing benchmark tags with the requested fallback', () => {
    expect(dnsTagListLabel(null, '无可用项')).toBe('无可用项')
    expect(dnsTagListLabel([], '无可用项')).toBe('无可用项')
    expect(dnsTagListLabel(['cloudflare', 'google'], '无可用项')).toBe('cloudflare · google')
  })
})
