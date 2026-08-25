import { describe, expect, it } from 'vitest'

import { dashboardServerTrafficBytes } from './dashboard-traffic'

describe('dashboardServerTrafficBytes', () => {
  it('aggregates the current-period traffic reported by every server', () => {
    expect(dashboardServerTrafficBytes([
      { traffic_upload_bytes: 1_024, traffic_download_bytes: 2_048 },
      { traffic_upload_bytes: 4_096, traffic_download_bytes: 8_192 },
    ])).toBe(15_360)
  })

  it('treats missing or invalid counters as zero', () => {
    expect(dashboardServerTrafficBytes([
      {},
      { traffic_upload_bytes: '512', traffic_download_bytes: Number.NaN },
      { traffic_upload_bytes: -1, traffic_download_bytes: Number.POSITIVE_INFINITY },
    ])).toBe(512)
  })
})
