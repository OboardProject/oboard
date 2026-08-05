import { describe, expect, it } from 'vitest'
import { POPULAR_REGION_CODES, collectRegionStats, orderRegions, regionPoints } from './region-order'

describe('region points', () => {
  it('grants the fixed default points to the popular regions and zero to others', () => {
    expect(POPULAR_REGION_CODES).toEqual(['CN', 'TW', 'HK', 'MO', 'US', 'SG', 'JP', 'KR', 'CA', 'DE', 'GB'])
    for (const code of POPULAR_REGION_CODES) {
      expect(regionPoints(code)).toBe(100)
    }
    expect(regionPoints('FR')).toBe(0)
  })
})

describe('collectRegionStats', () => {
  it('uses manual region codes and falls back to detected codes', () => {
    const servers = [
      { region_mode: 'manual', region_code: 'HK', detected_region_code: 'US', status: 'online' },
      { region_mode: 'auto', region_code: '', detected_region_code: 'sg', status: 'offline' },
    ]
    const stats = collectRegionStats(servers)
    expect(stats.get('HK')).toEqual({ total: 1, online: 1 })
    expect(stats.get('SG')).toEqual({ total: 1, online: 0 })
  })

  it('counts online servers and skips servers without a valid region', () => {
    const servers = [
      { region_mode: 'auto', region_code: '', detected_region_code: '', status: 'online' },
      { region_mode: 'auto', region_code: '', detected_region_code: 'abc', status: 'online' },
      { region_mode: 'manual', region_code: 'jp', detected_region_code: '', status: 'ONLINE' },
      { region_mode: 'manual', region_code: 'JP', detected_region_code: '', status: 'offline' },
    ]
    const stats = collectRegionStats(servers)
    expect(stats.get('JP')).toEqual({ total: 2, online: 1 })
    expect(stats.has('')).toBe(false)
  })
})

describe('orderRegions', () => {
  const regions = [
    { code: 'CN', label: '中国' },
    { code: 'HK', label: '香港' },
    { code: 'FR', label: '法国' },
    { code: 'CA', label: '加拿大' },
    { code: 'DE', label: '德国' },
  ]
  const labels: Record<string, string> = { CN: '中国', HK: '香港', FR: '法国', CA: '加拿大', DE: '德国' }
  const byLabel = (code: string) => labels[code]

  it('sorts by points plus total and online counts, descending', () => {
    const stats = new Map([
      ['CN', { total: 3, online: 2 }],
      ['HK', { total: 1, online: 1 }],
      ['CA', { total: 0, online: 0 }],
      ['DE', { total: 0, online: 0 }],
      ['FR', { total: 2, online: 0 }],
    ])
    const ordered = orderRegions(regions, stats, byLabel)
    expect(ordered.map(item => item.code)).toEqual(['CN', 'HK', 'DE', 'CA', 'FR'])
    expect(ordered.find(item => item.code === 'CN')).toMatchObject({ points: 100, total: 3, online: 2, score: 105 })
    expect(ordered.find(item => item.code === 'FR')).toMatchObject({ points: 0, total: 2, online: 0, score: 2 })
  })

  it('keeps zero-server regions selectable at the tail of their points tier', () => {
    const stats = new Map([['CN', { total: 2, online: 0 }], ['HK', { total: 1, online: 1 }], ['DE', { total: 1, online: 0 }]])
    expect(orderRegions(regions, stats, byLabel).map(item => item.code)).toEqual(['HK', 'CN', 'DE', 'CA', 'FR'])
  })

  it('lets an unlisted region with many servers outrank a listed region with none', () => {
    const stats = new Map([['FR', { total: 150, online: 120 }], ['HK', { total: 0, online: 0 }]])
    expect(orderRegions(regions, stats, byLabel).map(item => item.code)).toEqual(['FR', 'DE', 'CA', 'HK', 'CN'])
  })

  it('breaks ties by the zh-CN label order', () => {
    const stats = new Map([['CN', { total: 0, online: 0 }], ['HK', { total: 0, online: 0 }]])
    expect(orderRegions(regions, stats, byLabel).map(item => item.code)).toEqual(['DE', 'CA', 'HK', 'CN', 'FR'])
  })

  it('falls back to the region code order when no label resolver is given', () => {
    const stats = new Map([['CN', { total: 0, online: 0 }], ['HK', { total: 0, online: 0 }], ['FR', { total: 0, online: 0 }]])
    expect(orderRegions(regions, stats).map(item => item.code)).toEqual(['CA', 'CN', 'DE', 'HK', 'FR'])
  })
})
