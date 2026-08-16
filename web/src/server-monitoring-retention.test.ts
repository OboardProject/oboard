import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const settingsSource = readFileSync(new URL('./components/AgentSettingsPanel.tsx', import.meta.url), 'utf8')
const mainSource = readFileSync(new URL('./main.tsx', import.meta.url), 'utf8')

describe('server monitoring retention', () => {
  it('saves the shared monitoring retention setting from the Agent settings panel', () => {
    expect(settingsSource).toContain('const monitoringRetentionOptions = [1, 3, 7, 15, 30]')
    expect(settingsSource).toContain('server_monitoring_retention_days: days')
    expect(settingsSource).toContain('server_monitoring_retention_days) || 7')
    expect(settingsSource).toContain('id="server-monitoring-retention-days"')
    expect(settingsSource).toContain('aria-describedby="server-monitoring-retention-help"')
  })

  it('uses aggregated regional history for every visible latency window', () => {
    expect(mainSource).not.toContain('/latency-probe?limit=512')
    expect(mainSource).toContain("['1h', '6h', '12h', '24h', '7d', '30d']")
    expect(mainSource).toContain('regionalProbes={response.regional_latency_points || []}')
    expect(mainSource).toContain('windowEndAt={response.window.to}')
    expect(mainSource).toContain('response?.retention_days) || 7')
  })
})
