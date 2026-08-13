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
          }
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
    expect(document.body.textContent).toContain('分组：香港')
    expect(document.body.textContent).toContain('移出')

    // 3. Verify union explanation is prominently displayed
    expect(document.body.textContent).toContain('授权规则说明')
    expect(document.body.textContent).toContain('并集')

    // 4. Click '授权用户' button to open secondary modal
    const authBtn = Array.from(document.querySelectorAll('button')).find(b => b.textContent?.includes('授权用户'))
    expect(authBtn).toBeTruthy()
    act(() => {
      authBtn?.click()
    })
    await flushEffects()

    // 5. Verify secondary modal opened with user search
    expect(document.body.textContent).toContain('授权用户 · 🇭🇰 香港 01')
    const searchInput = document.querySelector('input[placeholder="搜索用户（用户名 / 昵称）"]')
    expect(searchInput).toBeTruthy()
    expect(document.body.textContent).toContain('alice')
    expect(document.body.textContent).toContain('bob')
  })
})
