import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const source = readFileSync(new URL('./main.tsx', import.meta.url), 'utf8')

describe('server resource history', () => {
  it('keeps live resource values visible when history recording is disabled', () => {
    expect(source).toContain("if (!server.resource_history_enabled)")
    expect(source).toContain('主控只显示 Agent 最新上报的 CPU 和内存，不保存历史数据。')
    expect(source).toContain('current: {')
  })

  it('loads bounded history windows on demand', () => {
    expect(source).toContain('`/servers/${server.id}/resource-metrics?hours=${hours}`')
    expect(source).toContain('[1, 6, 24, 48]')
  })
})
