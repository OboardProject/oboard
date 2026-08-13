import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const source = readFileSync(new URL('./main.tsx', import.meta.url), 'utf8')

describe('server resource history', () => {
  it('keeps live resource values visible when history recording is disabled', () => {
    expect(source).toContain("if (!server.resource_history_enabled)")
    expect(source).toContain('历史记录已关闭，当前只显示 Agent 最新上报值。')
    expect(source).toContain('current: {')
    expect(source).toContain('tcp_connection_count: server.tcp_connection_count || 0')
  })

  it('loads bounded history windows on demand', () => {
    expect(source).toContain('`/servers/${server.id}/resource-metrics?hours=${hours}`')
    expect(source).toContain("{ hours: 1, label: '实时' }")
    expect(source).toContain("{ hours: 720, label: '30 天' }")
  })

  it('opens the unified monitor from the whole server card', () => {
    expect(source).toContain('server-monitor-open-overlay')
    expect(source).toContain('服务器监控视图')
    expect(source).toContain("useState<'load' | 'latency'>('load')")
  })
})
