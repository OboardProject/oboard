export type RegionStat = { total: number; online: number }

export const POPULAR_REGION_CODES = ['CN', 'TW', 'HK', 'MO', 'US', 'SG', 'JP', 'KR', 'CA', 'DE', 'GB']
const POINTED_REGION_CODES = new Set(POPULAR_REGION_CODES)
const REGION_POINTS = 100
const REGION_CODE_PATTERN = /^[A-Z]{2}$/

type RegionServer = {
  region_mode?: string
  region_code?: string
  detected_region_code?: string
  effective_region_code?: string
  status?: string
}

export function regionPoints(code: string) {
  return POINTED_REGION_CODES.has(code) ? REGION_POINTS : 0
}

function regionCodeOf(server: RegionServer) {
  const value = String((server.region_mode === 'manual' ? server.region_code : (server.effective_region_code || server.detected_region_code || server.region_code)) || '').trim().toUpperCase()
  return REGION_CODE_PATTERN.test(value) ? value : ''
}

export function collectRegionStats<T extends RegionServer>(servers: readonly T[]) {
  const stats = new Map<string, RegionStat>()
  for (const server of servers) {
    const code = regionCodeOf(server)
    if (!code) continue
    const stat = stats.get(code) || { total: 0, online: 0 }
    stat.total += 1
    if (String(server.status || '').toLowerCase() === 'online') stat.online += 1
    stats.set(code, stat)
  }
  return stats
}

export function orderRegions<T extends { code: string }>(
  regions: readonly T[],
  statsByCode: ReadonlyMap<string, RegionStat>,
  labelOf: (code: string) => string = code => code,
) {
  return regions
    .map(region => {
      const stat = statsByCode.get(region.code) || { total: 0, online: 0 }
      const points = regionPoints(region.code)
      return { ...region, total: stat.total, online: stat.online, points, score: points + stat.total + stat.online }
    })
    .sort((a, b) => b.score - a.score || labelOf(a.code).localeCompare(labelOf(b.code), 'zh-CN') || a.code.localeCompare(b.code))
}

export function orderServerRegions<T extends RegionServer>(
  servers: readonly T[],
  labelOf: (code: string) => string = code => code,
) {
  const stats = collectRegionStats(servers)
  const regions = Array.from(stats, ([code, stat]) => ({ code, count: stat.total }))
  const ordered = orderRegions(regions, stats, labelOf).map(({ code, count }) => ({ code, count }))
  const categorizedCount = regions.reduce((total, region) => total + region.count, 0)

  if (categorizedCount < servers.length) ordered.push({ code: '', count: servers.length - categorizedCount })
  return ordered
}
