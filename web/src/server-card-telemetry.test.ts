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

describe('server card address badge', () => {
  it('puts the public IP in the former stack-badge slot and copies the clicked family', () => {
    expect(mainSource).toContain('function serverAddressBadge')
    expect(mainSource).toContain('function ServerAddressBadge')
    expect(mainSource).not.toContain('function serverStackBadge')
    expect(mainSource).toContain('<ServerAddressBadge server={server} />')
    expect(mainSource).toContain("const copy = async (kind: 'v4' | 'v6', value: string)")
    expect(mainSource).toContain("onClick={() => void copy('v4', v4)}")
    expect(mainSource).toContain("onClick={() => void copy('v6', v6)}")
    expect(mainSource).toContain('复制 IPv4 ${v4}')
    expect(mainSource).not.toContain('className={`server-stack-badge ${stack.tone}`}')
  })
})

describe('server card action menu', () => {
  const menuSource = readFileSync(new URL('./components/server/ServerActionMenu.tsx', import.meta.url), 'utf8')

  it('keeps enroll command and agent update as top-level menu actions', () => {
    expect(menuSource).toContain("label: '接入命令'")
    expect(menuSource).toContain("type: 'enroll'")
    expect(menuSource).toContain("label: '更新 Agent'")
    expect(menuSource).toContain("type: 'update-agent'")
    expect(mainSource).toContain("else if (type === 'update-agent') void updateAgent(s)")
    expect(mainSource).toContain("else if (type === 'enroll') enroll(s)")
  })
})
