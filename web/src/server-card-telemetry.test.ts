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
  const styleSource = readFileSync(new URL('./style.css', import.meta.url), 'utf8')

  it('stacks egress v4/v6, keeps the server name visible, and middle-ellipsizes IPv6 when space is tight', () => {
    expect(mainSource).toContain('function serverCustomEntry')
    expect(mainSource).toContain('function ipv6DisplayParts')
    expect(mainSource).toContain('function ServerAddressBadge')
    expect(mainSource).not.toContain('function serverStackBadge')
    expect(mainSource).not.toContain('function serverAddressBadge')
    expect(mainSource).toContain('<span className="server-address-kicker">出口</span>')
    expect(mainSource).toContain('<span className="server-address-kicker">入口</span>')
    expect(mainSource).toContain("className={`server-address-line ${family}${copied ? ' copied' : ''}`}")
    expect(mainSource).toContain('server-address-value is-middle')
    expect(mainSource).toContain('server-address-value-start')
    expect(mainSource).toContain('server-address-value-end')
    expect(mainSource).not.toContain('server-address-family')
    expect(mainSource).toContain("const copy = async (kind: 'v4' | 'v6' | 'entry', value: string)")
    expect(mainSource).toContain("onClick={() => void copy('entry', entry)}")
    expect(mainSource).toContain('createPortal(')
    expect(mainSource).toContain('if (!entry || !blockRef.current) return')
    expect(mainSource).not.toContain('title={copied ? `已复制出口')
    expect(mainSource).toContain("const v6 = String(server.public_ipv6 || '').trim()")
    expect(mainSource).not.toContain("const v6 = String(server.public_ipv6 || server.interface_ipv6")
    expect(styleSource).toMatch(/\.server-address-stack\s*\{[^}]*flex-direction:\s*column/)
    expect(styleSource).toMatch(/\.server-card-name-row h3\s*\{[^}]*flex:\s*0 0 auto/)
    expect(styleSource).toMatch(/\.server-address-block\s*\{[^}]*flex:\s*1 1 8\.5rem/)
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
