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
      if (path === '/subscription-plans/1/ordering') return { nodes: [], policy: { mode: 'exit_region' } }
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

  it('deletes a plan after confirming that bound users only lose the plan', async () => {
    const boundPlan = { ...plan, member_count: 2 }
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/subscription-plans') return { subscription_plans: [boundPlan] }
      if (path === '/access-changes?limit=50') return { access_changes: [] }
      if (path === '/subscription-plans/1') return {
        subscription_plan: boundPlan,
        latest_nodes: [],
        revisions: [{ id: 1, revision: 1, version_no: 1, status: 'current', speed_limit_mbps: 100, traffic_limit_bytes: 1073741824, traffic_reset_mode: 'monthly', traffic_reset_day: 1, created_at: '2026-08-11T00:00:00Z' }],
        member_count: 2,
      }
      if (path.startsWith('/assignable-nodes?')) return { nodes: [], total: 0, page: 1, page_size: 200 }
      if (path === '/subscription-plans/1/ordering') return { nodes: [], policy: { mode: 'exit_region' } }
      if (path === '/subscription-plans/1/membership-rules') return { rules: [], exclusions: [] }
      if (path === '/subscription-plans/1' && init?.method === 'DELETE') return { deleted: false, access_change_id: 44, unbound_user_count: 2 }
      throw new Error(`unexpected request: ${path} ${init?.method || 'GET'}`)
    })
    const notify = vi.fn()
    const load = vi.fn().mockResolvedValue(undefined)

    await act(async () => {
      root.render(<SubscriptionPlansPage data={{ subscription_plans: [boundPlan] }} client={{ request }} load={load} notify={notify} />)
    })
    await flushEffects()

    const editButton = Array.from(container.querySelectorAll('button')).find(button => button.textContent === '编辑')
    act(() => editButton?.click())
    await flushEffects()

    const deleteButton = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent?.includes('删除'))
    expect(deleteButton).toBeTruthy()
    act(() => deleteButton?.click())
    await flushEffects()

    expect(document.body.textContent).toContain('绑定该套餐的用户只会移除套餐')
    expect(document.body.textContent).toContain('当前有 2 个绑定用户')
    const confirmButton = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent === '删除套餐')
    expect(confirmButton).toBeTruthy()
    act(() => confirmButton?.click())
    await flushEffects()

    expect(request).toHaveBeenCalledWith('/subscription-plans/1', { method: 'DELETE' })
    expect(notify).toHaveBeenCalledWith('已开始删除，正在为 2 个用户移除套餐（变更 #44）', 'success')
  })

  it('drops stale node references before saving a normally added node', async () => {
    const latestNodes = [
      { node_type: 'inbound', node_id: 24, display_group: '', source_type: 'explicit' },
      { node_type: 'inbound', node_id: 30, display_group: '', source_type: 'explicit' },
      { node_type: 'proxy_path', node_id: 1, display_group: '', source_type: 'explicit' },
    ]
    const catalogNodes = [
      { type: 'inbound', id: 24, key: 'inbound:24', name: 'NB TYO', effective_global_name: 'NB TYO', entry_server_name: 'NB TYO', exit_region: 'JP', status: 'ok' },
      { type: 'proxy_path', id: 11, key: 'proxy_path:11', name: 'CDT | Starhub', effective_global_name: 'CDT | Starhub', entry_server_name: 'CDT', exit_region: 'SG', status: 'ok' },
    ]
    const previewBodies: any[] = []
    const applyBodies: any[] = []
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/subscription-plans') return { subscription_plans: [{ ...plan, node_count: 3 }] }
      if (path === '/access-changes?limit=50') return { access_changes: [] }
      if (path === '/subscription-plans/1') return {
        subscription_plan: { ...plan, node_count: 3 },
        latest_nodes: latestNodes,
        revisions: [{ id: 1, revision: 1, version_no: 1, status: 'current', speed_limit_mbps: 100, traffic_limit_bytes: 1073741824, traffic_reset_mode: 'monthly', traffic_reset_day: 1, created_at: '2026-08-11T00:00:00Z' }],
        member_count: 0,
      }
      if (path.startsWith('/assignable-nodes?')) return { nodes: catalogNodes, total: catalogNodes.length, page: 1, page_size: 200 }
      if (path === '/subscription-plans/1/ordering') return { nodes: [{ key: 'inbound:24' }, { key: 'inbound:30' }, { key: 'proxy_path:1' }], policy: { mode: 'exit_region' } }
      if (path === '/subscription-plans/1/membership-rules') return { rules: [], exclusions: [] }
      if (path === '/subscription-plans/1/nodes/preview') {
        const body = JSON.parse(String(init?.body || '{}'))
        previewBodies.push(body)
        if (body.nodes.some((node: any) => node.node_id === 30 || (node.node_type === 'proxy_path' && node.node_id === 1))) {
          throw new Error('node is not assignable')
        }
        return { base_revision_id: 1, expected_lock_version: 1, node_count: body.nodes.length, preview: {} }
      }
      if (path === '/subscription-plans/1/nodes/apply') {
        const body = JSON.parse(String(init?.body || '{}'))
        applyBodies.push(body)
        return { no_change: false, access_change_id: 9 }
      }
      throw new Error(`unexpected request: ${path}`)
    })

    await act(async () => {
      root.render(<SubscriptionPlansPage data={{ subscription_plans: [{ ...plan, node_count: 3 }] }} client={{ request }} load={vi.fn().mockResolvedValue(undefined)} />)
    })
    await flushEffects()

    const editButton = Array.from(container.querySelectorAll('button')).find(button => button.textContent === '编辑')
    act(() => editButton?.click())
    await flushEffects()

    const addButton = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent?.includes('添加节点'))
    act(() => addButton?.click())
    await flushEffects()

    const newNodeCheckbox = Array.from(document.body.querySelectorAll('label')).find(label => label.textContent?.includes('CDT | Starhub'))?.querySelector('input[type="checkbox"]') as HTMLInputElement | undefined
    expect(newNodeCheckbox).toBeTruthy()
    act(() => newNodeCheckbox?.click())
    const doneButton = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent?.includes('完成（新增'))
    act(() => doneButton?.click())

    const saveButton = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent?.includes('保存节点变更'))
    act(() => saveButton?.click())
    await flushEffects()

    expect(previewBodies).toHaveLength(1)
    expect(previewBodies[0].nodes).toEqual([
      { node_type: 'inbound', node_id: 24, display_group: '' },
      { node_type: 'proxy_path', node_id: 11, display_group: '' },
    ])
    expect(applyBodies).toHaveLength(1)
  })

  it('shows a failed node change without blocking further edits', async () => {
    const blockedPlan = { ...plan, lock_version: 2, latest_revision_id: 2, pending_revision_id: 2, node_count: 1 }
    const request = vi.fn(async (path: string) => {
      if (path === '/subscription-plans') return { subscription_plans: [blockedPlan] }
      if (path === '/access-changes?limit=50') return {
        access_changes: [{
          id: 17,
          change_type: 'plan_publish',
          source_plan_id: 1,
          candidate_revision_id: 2,
          status: 'failed',
          affected_user_count: 1,
          error: 'server 41 task 5028 failed',
          created_at: '2026-08-17T00:00:00Z',
          targets: [{ server_id: 41, prepare_task_id: 5028, status: 'failed' }],
        }],
      }
      if (path === '/subscription-plans/1') return {
        subscription_plan: blockedPlan,
        latest_nodes: [{ node_type: 'proxy_path', node_id: 11, display_group: '', source_type: 'explicit' }],
        revisions: [{ id: 2, revision: 2, version_no: 2, status: 'latest', speed_limit_mbps: 100, traffic_limit_bytes: 1073741824, traffic_reset_mode: 'monthly', traffic_reset_day: 1, created_at: '2026-08-17T00:00:00Z' }],
        member_count: 1,
      }
      if (path.startsWith('/assignable-nodes?')) return { nodes: [{ type: 'proxy_path', id: 11, key: 'proxy_path:11', name: 'CDT | Starhub', effective_global_name: 'CDT | Starhub', entry_server_name: 'CDT', exit_region: 'SG', status: 'ok' }], total: 1, page: 1, page_size: 200 }
      if (path === '/subscription-plans/1/ordering') return { nodes: [{ key: 'proxy_path:11' }], policy: { mode: 'exit_region' } }
      if (path === '/subscription-plans/1/membership-rules') return { rules: [], exclusions: [] }
      if (path === '/access-changes/17/cancel') return { access_change_id: 17, status: 'cancelled' }
      throw new Error(`unexpected request: ${path}`)
    })

    await act(async () => {
      root.render(<SubscriptionPlansPage data={{ subscription_plans: [blockedPlan] }} client={{ request }} load={vi.fn().mockResolvedValue(undefined)} />)
    })
    await flushEffects()

    const editButton = Array.from(container.querySelectorAll('button')).find(button => button.textContent === '编辑')
    act(() => editButton?.click())
    await flushEffects()

    expect(document.body.textContent).toContain('server 41 task 5028 failed')
    expect(document.body.textContent).toContain('新保存会自动取代这次失败')
    const addButton = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent?.includes('添加节点')) as HTMLButtonElement | undefined
    expect(addButton?.disabled).toBe(false)
    const abandonButton = Array.from(document.body.querySelectorAll('button')).find(button => button.textContent?.includes('放弃失败变更'))
    expect(abandonButton).toBeTruthy()
    act(() => abandonButton?.click())
    await flushEffects()
    expect(request).toHaveBeenCalledWith('/access-changes/17/cancel', { method: 'POST', body: '{}' })
  })

  it('opens plan detail with standalone SSH in subscription order instead of node_type order', async () => {
    const latestNodes = [
      { node_type: 'inbound', node_id: 31, display_group: '', source_type: 'explicit' },
      { node_type: 'proxy_path', node_id: 1, display_group: '', source_type: 'explicit' },
      { node_type: 'proxy_path', node_id: 2, display_group: '', source_type: 'explicit' },
    ]
    const catalogNodes = [
      { type: 'inbound', id: 31, key: 'inbound:31', name: '沪日｜SSH', effective_global_name: '沪日｜SSH', entry_server_name: '沪日', entry_protocol: 'ssh', exit_region: 'JP', status: 'ok' },
      { type: 'proxy_path', id: 1, key: 'proxy_path:1', name: '9929', effective_global_name: '9929', entry_server_name: '9929', entry_protocol: 'vless', exit_region: 'DE', status: 'ok' },
      { type: 'proxy_path', id: 2, key: 'proxy_path:2', name: '沪日｜HY2', effective_global_name: '沪日｜HY2', entry_server_name: '沪日', entry_protocol: 'hysteria2', exit_region: 'JP', status: 'ok' },
    ]
    const request = vi.fn(async (path: string) => {
      if (path === '/subscription-plans') return { subscription_plans: [{ ...plan, node_count: 3 }] }
      if (path === '/access-changes?limit=50') return { access_changes: [] }
      if (path === '/subscription-plans/1') return {
        subscription_plan: { ...plan, node_count: 3 },
        latest_nodes: latestNodes,
        revisions: [{ id: 1, revision: 1, version_no: 1, status: 'current', speed_limit_mbps: 100, traffic_limit_bytes: 1073741824, traffic_reset_mode: 'monthly', traffic_reset_day: 1, created_at: '2026-08-11T00:00:00Z' }],
        member_count: 0,
      }
      if (path.startsWith('/assignable-nodes?')) return { nodes: catalogNodes, total: catalogNodes.length, page: 1, page_size: 200 }
      if (path === '/subscription-plans/1/ordering') return {
        nodes: [{ key: 'proxy_path:1' }, { key: 'inbound:31' }, { key: 'proxy_path:2' }],
        policy: { mode: 'manual' },
      }
      if (path === '/subscription-plans/1/membership-rules') return { rules: [], exclusions: [] }
      throw new Error(`unexpected request: ${path}`)
    })

    await act(async () => {
      root.render(<SubscriptionPlansPage data={{ subscription_plans: [{ ...plan, node_count: 3 }] }} client={{ request }} load={vi.fn().mockResolvedValue(undefined)} />)
    })
    await flushEffects()

    const editButton = Array.from(container.querySelectorAll('button')).find(button => button.textContent === '编辑')
    act(() => editButton?.click())
    await flushEffects()
    await flushEffects()

    const names = Array.from(document.body.querySelectorAll('.plan-node-row strong')).map(el => el.textContent)
    expect(names).toEqual(['9929', '沪日｜SSH', '沪日｜HY2'])
  })
})
