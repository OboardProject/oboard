// @vitest-environment jsdom

import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NodeScopeActionDialog } from './NodeScopeActionDialog'

const mockNode = {
  key: 'node:inbound:1',
  type: 'inbound',
  id: 1,
  name: '🇭🇰 香港 01',
}

const mockScope = {
  kind: 'node',
}

const mockPlans = [
  { id: 1, name: '基础套餐' },
  { id: 2, name: 'VIP套餐' },
]

const mockUsers = [
  { id: 101, username: 'alice', nickname: '爱丽丝' },
  { id: 102, username: 'bob', nickname: '鲍勃' },
]

async function flushEffects() {
  await act(async () => {
    await new Promise(resolve => window.setTimeout(resolve, 0))
  })
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}

describe('NodeScopeActionDialog', () => {
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

  it('removes a plan immediately, deduplicates fast clicks, and reports queued apply', async () => {
    const preview = deferred<{ preview: Record<string, never>; expected_revision: number; expected_lock_version: number; base_revision_id: number; node_count: number }>()
    let detailRequests = 0
    const client = {
      request: vi.fn(async (path: string) => {
        if (path === '/assignable-node-scopes/preview') {
          return {
            scope: { kind: 'node' }, count: 1,
            node_refs: [{ node_type: 'inbound', node_id: 1 }],
            sample_nodes: [{ key: 'node:inbound:1', name: '🇭🇰 香港 01' }],
            warnings: [], selection_hash: 'hash-1',
          }
        }
        if (path === '/assignable-nodes/inbound/1') {
          detailRequests++
          return detailRequests === 1
            ? { plans: [{ plan_id: 1, name: '基础套餐', display_group: '香港' }], authorizations: [] }
            : { plans: [], authorizations: [] }
        }
        if (path === '/subscription-plans/1/nodes/preview') return preview.promise
        if (path === '/subscription-plans/1/nodes/apply') return { reconcile_queued: true }
        return {}
      }),
    }
    const notify = vi.fn()
    const onDone = vi.fn()

    act(() => {
      root.render(
        <NodeScopeActionDialog
          open={true}
          node={mockNode}
          scope={mockScope}
          plans={mockPlans}
          users={mockUsers}
          client={client}
          notify={notify}
          onClose={vi.fn()}
          onDone={onDone}
        />
      )
    })
    await flushEffects()

    const removeButton = document.querySelector('button[aria-label="从套餐【基础套餐】移出此节点"]') as HTMLButtonElement
    expect(removeButton).toBeTruthy()
    act(() => {
      removeButton.click()
      removeButton.click()
    })

    expect(document.querySelector('button[aria-label="从套餐【基础套餐】移出此节点"]')).toBeNull()
    expect(document.querySelector('[role="status"]')?.textContent).toContain('正在从套餐【基础套餐】移出')
    await flushEffects()
    expect(client.request.mock.calls.filter(([path]) => path === '/subscription-plans/1/nodes/preview')).toHaveLength(1)

    preview.resolve({ preview: {}, expected_revision: 2, expected_lock_version: 2, base_revision_id: 1, node_count: 1 })
    await flushEffects()

    expect(client.request.mock.calls.filter(([path]) => path === '/subscription-plans/1/nodes/apply')).toHaveLength(1)
    expect(document.querySelector('[role="status"]')?.textContent).toContain('移出操作已保存，正在应用')
    expect(notify).toHaveBeenCalledWith('已保存从套餐【基础套餐】移出节点的操作，正在应用', 'success')
    expect(onDone).toHaveBeenCalledTimes(1)
  })

  it('reconciles desired state when an apply response is lost after save', async () => {
    let detailRequests = 0
    const client = {
      request: vi.fn(async (path: string) => {
        if (path === '/assignable-node-scopes/preview') {
          return {
            scope: { kind: 'node' }, count: 1,
            node_refs: [{ node_type: 'inbound', node_id: 1 }],
            sample_nodes: [{ key: 'node:inbound:1', name: '🇭🇰 香港 01' }],
            warnings: [], selection_hash: 'hash-1',
          }
        }
        if (path === '/assignable-nodes/inbound/1') {
          detailRequests++
          return detailRequests === 1
            ? { plans: [{ plan_id: 1, name: '基础套餐' }], authorizations: [] }
            : { plans: [], authorizations: [] }
        }
        if (path === '/subscription-plans/1/nodes/preview') {
          return { preview: {}, expected_revision: 2, expected_lock_version: 2, base_revision_id: 1, node_count: 1 }
        }
        if (path === '/subscription-plans/1/nodes/apply') throw new Error('网络连接中断')
        if (path === '/subscription-plans/1') return { latest_nodes: [] }
        return {}
      }),
    }
    const notify = vi.fn()

    act(() => {
      root.render(
        <NodeScopeActionDialog
          open={true}
          node={mockNode}
          scope={mockScope}
          plans={mockPlans}
          users={mockUsers}
          client={client}
          notify={notify}
          onClose={vi.fn()}
          onDone={vi.fn()}
        />
      )
    })
    await flushEffects()

    const removeButton = document.querySelector('button[aria-label="从套餐【基础套餐】移出此节点"]') as HTMLButtonElement
    act(() => removeButton.click())
    await flushEffects()

    expect(document.querySelector('button[aria-label="从套餐【基础套餐】移出此节点"]')).toBeNull()
    expect(document.querySelector('[role="status"]')?.textContent).toContain('已同步套餐【基础套餐】的最新状态')
    expect(notify).toHaveBeenCalledWith('节点已从套餐【基础套餐】移出', 'success')
  })

  it('renders plans list, union explanation, and opens user authorization dialog', async () => {
    const requestedPaths: string[] = []
    const client = {
      request: vi.fn(async (path: string) => {
        requestedPaths.push(path)
        if (path === '/assignable-node-scopes/preview') {
          return {
            scope: { kind: 'node' },
            count: 1,
            node_refs: [{ node_type: 'inbound', node_id: 1 }],
            sample_nodes: [{ key: 'node:inbound:1', name: '🇭🇰 香港 01' }],
            warnings: [],
            selection_hash: 'hash-1',
          }
        }
        if (path === '/assignable-nodes/inbound/1') {
          return {
            plans: [{ plan_id: 1, name: '基础套餐', display_group: '香港' }],
            authorizations: [{
              id: 501,
              user_id: 101,
              username: 'alice',
              nickname: '爱丽丝',
              effect: 'allow',
              status: 'active',
              reason: '测试授权',
              effective: true,
              plan_includes: true,
              plan_id: 1,
              plan_name: '基础套餐',
            }],
          }
        }
        if (path === '/user-node-exceptions/501') {
          return { access_change_id: 88, access_change_status: 'preparing' }
        }
        return {}
      }),
    }

    const notify = vi.fn()
    const onClose = vi.fn()
    const onDone = vi.fn()

    act(() => {
      root.render(
        <NodeScopeActionDialog
          open={true}
          node={mockNode}
          scope={mockScope}
          plans={mockPlans}
          users={mockUsers}
          client={client}
          notify={notify}
          onClose={onClose}
          onDone={onDone}
        />
      )
    })

    await flushEffects()

    // 1. Verify dialog rendered with node title
    expect(document.body.textContent).toContain('节点操作：🇭🇰 香港 01')

    // 2. Verify assigned plans list shows '基础套餐' with remove button
    expect(document.body.textContent).toContain('基础套餐')
    expect(document.body.textContent).toContain('香港')
    const removeBtn = document.querySelector('button[aria-label="从套餐【基础套餐】移出此节点"]')
    expect(removeBtn).toBeTruthy()

    // 3. Verify union explanation is prominently displayed
    expect(document.body.textContent).toContain('授权规则说明')
    expect(document.body.textContent).toContain('并集')

    // 4. Existing direct authorization is listed and marks plan overlap.
    expect(document.body.textContent).toContain('爱丽丝')
    expect(document.body.textContent).toContain('套餐也包含')
    expect(document.body.textContent).toContain('测试授权')
    const revokeButton = document.querySelector('button[aria-label="撤销 alice 的单独授权"]') as HTMLButtonElement | null
    expect(revokeButton).toBeTruthy()

    // 5. Revoking explains that plan access remains, then calls the existing two-phase delete API.
    act(() => revokeButton?.click())
    await flushEffects()
    expect(document.body.textContent).toContain('撤销单独授权')
    expect(document.body.textContent).toContain('仍会通过套餐继续获得此节点')
    const confirmRevoke = Array.from(document.querySelectorAll('button')).find(b => b.textContent?.includes('撤销授权'))
    act(() => confirmRevoke?.click())
    await flushEffects()
    expect(client.request).toHaveBeenCalledWith('/user-node-exceptions/501', { method: 'DELETE' })

    // 6. Click the add button to open the secondary authorization modal.
    const authBtn = Array.from(document.querySelectorAll('button')).find(b => b.textContent?.includes('添加用户'))
    expect(authBtn).toBeTruthy()
    act(() => authBtn?.click())
    await flushEffects()

    expect(document.body.textContent).toContain('授权用户 · 🇭🇰 香港 01')
    const searchInput = document.querySelector('input[placeholder="搜索用户（用户名 / 昵称）"]')
    expect(searchInput).toBeTruthy()
    expect(document.body.textContent).toContain('alice')
    expect(document.body.textContent).toContain('bob')
  })
})
