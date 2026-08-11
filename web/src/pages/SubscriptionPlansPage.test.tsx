// @vitest-environment jsdom

import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SubscriptionPlansPage } from './SubscriptionPlansPage'

const plan = {
  id: 1,
  name: '标准套餐',
  description: '',
  enabled: true,
  revision: 1,
  lock_version: 1,
  current_revision_id: 1,
  latest_revision_id: 1,
  latest_version_created_at: '2026-08-11T00:00:00Z',
  node_count: 0,
  member_count: 0,
  speed_limit_mbps: 100,
  traffic_limit_bytes: 1073741824,
  traffic_reset_mode: 'monthly',
  traffic_reset_day: 1,
}

async function flushEffects() {
  await act(async () => {
    await new Promise(resolve => window.setTimeout(resolve, 0))
  })
}

describe('SubscriptionPlansPage', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.querySelectorAll('.dialog-root').forEach(element => element.remove())
    container.remove()
    document.body.style.overflow = ''
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('keeps user assignment inside the selected plan detail', async () => {
    const request = vi.fn(async (path: string) => {
      if (path === '/subscription-plans') return { subscription_plans: [plan] }
      if (path === '/access-changes?limit=50') return { access_changes: [] }
      if (path === '/subscription-plans/1') return {
        subscription_plan: plan,
        latest_nodes: [],
        revisions: [{ id: 1, revision: 1, version_no: 1, status: 'current', speed_limit_mbps: 100, traffic_limit_bytes: 1073741824, traffic_reset_mode: 'monthly', traffic_reset_day: 1, created_at: '2026-08-11T00:00:00Z' }],
        member_count: 0,
      }
      if (path.startsWith('/assignable-nodes?')) return { nodes: [], total: 0, page: 1, page_size: 200 }
      if (path === '/subscription-plans/1/membership-rules') return { rules: [], exclusions: [] }
      if (path === '/users') return { users: [{ id: 7, username: 'alice', nickname: 'Alice', status: 'active' }] }
      throw new Error(`unexpected request: ${path}`)
    })

    await act(async () => {
      root.render(<SubscriptionPlansPage data={{ subscription_plans: [plan] }} client={{ request }} load={vi.fn().mockResolvedValue(undefined)} />)
    })
    await flushEffects()

    const editButton = Array.from(container.querySelectorAll('button')).find(button => button.textContent === '编辑')
    expect(editButton).toBeTruthy()
    act(() => editButton?.click())
    await flushEffects()

    const assignButton = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent?.includes('分配用户'))
    expect(assignButton).toBeTruthy()
    act(() => assignButton?.click())
    await flushEffects()

    expect(request).toHaveBeenCalledWith('/users')
    expect(document.body.textContent).toContain('将此套餐分配给用户')
    expect(document.body.textContent).toContain('alice')
  })
})
