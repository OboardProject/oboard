// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, it, expect, vi } from 'vitest'
import { Tasks } from './Tasks'
import { createAPIClientFactory } from '../../api-client'
import { redactTaskJSON, taskSummaryFromPayload } from './domain'

describe('task module', () => {
  it('loads the bounded task list independently and filters categories', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ tasks: [{ id: 1, type: 'apply_deployment', status: 'succeeded', server_id: 2, config_version: 7 }, { id: 2, type: 'remote_exec', status: 'pending', server_id: 2 }] }), { headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetch)
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
    const client = createAPIClientFactory(path => path, () => new Error('request failed'))('cookie')
    const container = document.createElement('div')
    const root = createRoot(container)
    try {
      await act(async () => root.render(<Tasks client={client} servers={[{ id: 2, name: '测试节点' }]} />))
      expect(fetch.mock.calls[0][0]).toContain('/agent-tasks?limit=300')
      expect(container.textContent).toContain('测试节点')
      expect(container.textContent).toContain('版本 7')
      const filter = container.querySelector('[aria-label="任务分类"]')!
      const buttons = Array.from(filter.querySelectorAll('button'))
      await act(async () => buttons.find(button => button.textContent?.includes('远程'))!.click())
      expect(container.querySelector('.task-card-list')?.textContent).toContain('远程命令')
    } finally {
      act(() => root.unmount())
      vi.unstubAllGlobals()
    }
  })
  it('retains task summaries and recursive secret redaction', () => {
    expect(taskSummaryFromPayload('apply_core_config', { skipped: true })).toBe('配置未变化，已跳过')
    expect(redactTaskJSON({ nested: [{ password: 'secret', config: 'abc', status: 'ok' }] })).toEqual({ nested: [{ password: '***', config: '[config 3 B]', status: 'ok' }] })
  })
})
