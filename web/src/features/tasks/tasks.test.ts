import { describe, expect, it, vi } from 'vitest'
import { fetchTasks } from './api'
import { redactTaskJSON, taskSummaryFromPayload } from './domain'
import type { APIClient } from '../../shared/api-client'

describe('task module boundary', () => {
  it('retains the 300 record query and empty response behavior', async () => {
    const request = vi.fn().mockResolvedValue({})
    expect(await fetchTasks({ request } as unknown as APIClient)).toEqual([])
    expect(request).toHaveBeenCalledWith('/agent-tasks?limit=300')
  })
  it('redacts nested task secrets without changing source data', () => {
    const source = { users: [{ password: 'private', name: 'user' }], token: 'private' }
    expect(redactTaskJSON(source)).toEqual({ users: [{ password: '***', name: 'user' }], token: '***' })
    expect(source.token).toBe('private')
    expect(taskSummaryFromPayload('apply_core_config', { skipped: true })).toBe('配置未变化，已跳过')
  })
})
