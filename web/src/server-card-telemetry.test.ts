import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const mainSource = readFileSync(new URL('./main.tsx', import.meta.url), 'utf8')

describe('server card cpu cores', () => {
  it('shows reported cpu_cores and keeps the model name on hover', () => {
    expect(mainSource).toContain('const cores = Math.trunc(Number(server.cpu_cores))')
    expect(mainSource).not.toContain('(?:核|cores?|v?cpus?)')
    expect(mainSource).toContain("title ? ' has-tip' : ''")
    expect(mainSource).toContain('role="tooltip" className="server-metric-tip"')
    expect(mainSource).toContain('cpuModelLabel(server)')
    expect(mainSource).not.toContain('server-metric-sub" title=')
  })
})

describe('server card latency sparkline', () => {
  it('plots public latency on the same sample window as traffic instead of dropping empty buckets', () => {
    expect(mainSource).toContain('function ServerTelemetryChart({ samples, type }')
    expect(mainSource).not.toContain('samples.filter(x => x.connectivity_available !== undefined)')
    expect(mainSource).toContain("x.connectivity_available === undefined ? null : Number(x.connectivity_latency_ms || 0)")
    expect(mainSource).toContain('values.flatMap((value, index) => value == null || !Number.isFinite(value) ? [] : [{ index, value }])')
  })
})
