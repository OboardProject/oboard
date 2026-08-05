import { describe, expect, it } from 'vitest'

import { getDashboardAttention } from './dashboard-attention'

const task = (overrides: Record<string, unknown>) => ({
  id: 1,
  server_id: 1,
  type: 'update_agent',
  status: 'succeeded',
  result_json: '{}',
  config_version: 0,
  created_at: '2026-08-06T10:00:00Z',
  updated_at: '2026-08-06T10:00:10Z',
  ...overrides,
})

describe('getDashboardAttention', () => {
  it('reports only the failure count from the first merged task group', () => {
    const attention = getDashboardAttention({
      summary: { servers_total: 3, servers_online: 3, failed_tasks: 234 },
      agent_tasks: [
        task({ id: 12, server_id: 3, status: 'succeeded', updated_at: '2026-08-06T10:05:12Z' }),
        task({ id: 11, server_id: 2, status: 'failed', updated_at: '2026-08-06T10:05:11Z' }),
        task({ id: 10, server_id: 1, status: 'failed', updated_at: '2026-08-06T10:05:10Z' }),
        task({ id: 9, server_id: 1, status: 'failed', created_at: '2026-08-06T09:00:00Z', updated_at: '2026-08-06T09:00:10Z' }),
      ],
    })

    expect(attention.parts).toEqual(['2 个任务失败'])
  })

  it('does not surface failures from older groups when the first task group succeeded', () => {
    const attention = getDashboardAttention({
      summary: { servers_total: 1, servers_online: 1, failed_tasks: 234 },
      agent_tasks: [
        task({ id: 20, type: 'apply_core_config', updated_at: '2026-08-06T11:00:00Z' }),
        task({ id: 19, type: 'apply_core_config', status: 'failed', updated_at: '2026-08-06T10:00:00Z' }),
      ],
    })

    expect(attention.parts).toEqual([])
  })
})
