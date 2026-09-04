import { describe, expect, it } from 'vitest'

import {
  compareDNSPolicyStatus,
  dnsPolicyStatus,
  dnsPolicyStatusTone,
  dnsRelativeTime,
  filterDNSPolicyRows,
  isDNSSelectionStale,
  latestDNSSuccessAt,
  matchesDNSPolicyStatusFilter,
  summarizeDNSPolicyStatuses,
  type DNSPolicyStatus,
  type DNSStatusPolicy,
} from './dns-status'

const lists = [
  { id: 1, kind: 'encrypted', revision: 4 },
  { id: 2, kind: 'bootstrap', revision: 7 },
]

function policy(patch: Partial<DNSStatusPolicy> = {}): DNSStatusPolicy {
  return {
    server_id: 1,
    encrypted_list_id: 1,
    bootstrap_list_id: 2,
    encrypted_selection_revision: 4,
    bootstrap_selection_revision: 7,
    last_success_at: '2026-09-04T16:36:00Z',
    last_error: '',
    needs_benchmark: false,
    ...patch,
  }
}

describe('DNS policy status', () => {
  it('reports a converged policy as normal', () => {
    expect(dnsPolicyStatus(policy(), lists)).toBe('ok')
  })

  it('reports the last error before anything else', () => {
    expect(dnsPolicyStatus(policy({ last_error: 'timeout', needs_benchmark: true }), lists)).toBe('failed')
  })

  it('reports a policy that never succeeded as waiting for its first test', () => {
    expect(dnsPolicyStatus(policy({ last_success_at: '', needs_benchmark: true }), lists)).toBe('untested')
    expect(dnsPolicyStatus(undefined, lists)).toBe('untested')
  })

  it('reports an edited list or a server-side flag as waiting for a retest', () => {
    expect(dnsPolicyStatus(policy({ needs_benchmark: true }), lists)).toBe('stale')
    expect(dnsPolicyStatus(policy({ encrypted_selection_revision: 3 }), lists)).toBe('stale')
    expect(dnsPolicyStatus(policy({ bootstrap_selection_revision: 6 }), lists)).toBe('stale')
  })

  it('ignores revisions of lists the policy is not bound to', () => {
    expect(isDNSSelectionStale(policy({ encrypted_list_id: 99 }), lists)).toBe(false)
  })

  it('maps every status to a pill tone', () => {
    expect(dnsPolicyStatusTone('ok')).toBe('ok')
    expect(dnsPolicyStatusTone('failed')).toBe('danger')
    expect(dnsPolicyStatusTone('stale')).toBe('warning')
    expect(dnsPolicyStatusTone('untested')).toBe('warning')
  })
})

describe('DNS policy aggregation', () => {
  it('splits statuses into normal, pending, and failed', () => {
    const statuses: DNSPolicyStatus[] = ['ok', 'ok', 'stale', 'untested', 'failed']
    expect(summarizeDNSPolicyStatuses(statuses)).toEqual({ total: 5, ok: 2, pending: 2, failed: 1 })
    expect(summarizeDNSPolicyStatuses([])).toEqual({ total: 0, ok: 0, pending: 0, failed: 0 })
  })

  it('orders servers that need attention before healthy ones', () => {
    const statuses: DNSPolicyStatus[] = ['ok', 'untested', 'failed', 'stale']
    expect([...statuses].sort(compareDNSPolicyStatus)).toEqual(['failed', 'stale', 'untested', 'ok'])
  })

  it('returns the newest successful test time', () => {
    expect(latestDNSSuccessAt([
      policy({ last_success_at: '2026-09-04T10:00:00Z' }),
      policy({ last_success_at: '2026-09-04T16:36:00Z' }),
      policy({ last_success_at: '' }),
      policy({ last_success_at: 'not-a-time' }),
    ])).toBe('2026-09-04T16:36:00Z')
    expect(latestDNSSuccessAt([policy({ last_success_at: '' })])).toBe('')
  })
})

describe('DNS policy filtering', () => {
  const rows = [
    { serverName: 'SJC', status: 'ok' as DNSPolicyStatus, encryptedListID: 1, bootstrapListID: 2 },
    { serverName: 'StarLink', status: 'failed' as DNSPolicyStatus, encryptedListID: 1, bootstrapListID: 3 },
    { serverName: 'IXP', status: 'stale' as DNSPolicyStatus, encryptedListID: 5, bootstrapListID: 2 },
  ]

  it('treats every non-normal status as attention', () => {
    expect(matchesDNSPolicyStatusFilter('ok', 'attention')).toBe(false)
    expect(matchesDNSPolicyStatusFilter('stale', 'attention')).toBe(true)
    expect(matchesDNSPolicyStatusFilter('ok', 'all')).toBe(true)
    expect(matchesDNSPolicyStatusFilter('failed', 'stale')).toBe(false)
  })

  it('keeps the pending bucket free of failures so it matches the overview count', () => {
    expect(matchesDNSPolicyStatusFilter('stale', 'pending')).toBe(true)
    expect(matchesDNSPolicyStatusFilter('untested', 'pending')).toBe(true)
    expect(matchesDNSPolicyStatusFilter('failed', 'pending')).toBe(false)
    expect(matchesDNSPolicyStatusFilter('ok', 'pending')).toBe(false)
  })

  it('matches server names case-insensitively', () => {
    expect(filterDNSPolicyRows(rows, { query: '  star ' }).map(row => row.serverName)).toEqual(['StarLink'])
  })

  it('combines status and list filters', () => {
    expect(filterDNSPolicyRows(rows, { status: 'attention', encryptedListID: 1 }).map(row => row.serverName)).toEqual(['StarLink'])
    expect(filterDNSPolicyRows(rows, { bootstrapListID: 2 }).map(row => row.serverName)).toEqual(['SJC', 'IXP'])
    expect(filterDNSPolicyRows(rows, {})).toHaveLength(3)
  })
})

describe('DNS relative time', () => {
  const now = new Date('2026-09-05T12:00:00Z')

  it('renders coarse buckets', () => {
    expect(dnsRelativeTime('2026-09-05T11:59:30Z', now)).toBe('刚刚')
    expect(dnsRelativeTime('2026-09-05T11:58:00Z', now)).toBe('2 分钟前')
    expect(dnsRelativeTime('2026-09-05T09:00:00Z', now)).toBe('3 小时前')
    expect(dnsRelativeTime('2026-09-02T12:00:00Z', now)).toBe('3 天前')
  })

  it('returns nothing for missing or unparsable stamps', () => {
    expect(dnsRelativeTime('', now)).toBe('')
    expect(dnsRelativeTime(undefined, now)).toBe('')
    expect(dnsRelativeTime('not-a-time', now)).toBe('')
  })
})
