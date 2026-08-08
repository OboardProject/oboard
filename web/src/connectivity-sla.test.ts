import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { connectivityBucketTone, connectivityRequestPath, connectivitySlaDisplay, formatConnectivityDuration } from './connectivity-sla'

describe('connectivity API contract', () => {
  it('uses the canonical connectivity endpoint for every fixed window', () => {
    expect(connectivityRequestPath(7)).toBe('/servers/7/connectivity?window=24h')
    expect(connectivityRequestPath(7, '7d')).toBe('/servers/7/connectivity?window=7d')
    expect(connectivityRequestPath(7, '30d')).toBe('/servers/7/connectivity?window=30d')
  })

  it('does not present missing observation as zero percent', () => {
    expect(connectivitySlaDisplay(null)).toBe('—')
    expect(connectivitySlaDisplay(undefined)).toBe('—')
    expect(connectivitySlaDisplay(0)).toBe('0.00%')
    expect(connectivitySlaDisplay(99.956)).toBe('99.96%')
  })

  it('uses backend bucket SLA and preserves unknown buckets', () => {
    expect(connectivityBucketTone(null, 3600, 0)).toBe('none')
    expect(connectivityBucketTone(0, 0, 3600)).toBe('down')
    expect(connectivityBucketTone(99.9, 300, 3300)).toBe('great')
  })

  it('formats duration fields without converting unknown to availability', () => {
    expect(formatConnectivityDuration(0)).toBe('0 秒')
    expect(formatConnectivityDuration(43)).toBe('43 秒')
    expect(formatConnectivityDuration(900)).toBe('15 分钟')
    expect(formatConnectivityDuration(4500)).toBe('1 小时 15 分')
  })
})

describe('connectivity dialog data source', () => {
  const source = readFileSync(new URL('./main.tsx', import.meta.url), 'utf8')

  it('does not request heartbeat metrics for SLA', () => {
    expect(source).not.toContain('/metrics?limit=1440')
    expect(source).not.toContain('buildSlaTimeline')
  })

  it('renders authoritative coverage, buckets, latency points, and SLA explanation', () => {
    expect(source).toContain('response.summary.coverage_percent')
    expect(source).toContain('response?.buckets')
    expect(source).toContain('response?.latency_points')
    expect(source).toContain('延迟不参与 SLA')
  })
})
