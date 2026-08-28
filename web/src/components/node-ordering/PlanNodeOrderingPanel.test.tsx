// @vitest-environment jsdom

import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PlanNodeOrderingPanel } from './PlanNodeOrderingPanel'

async function flushEffects() {
  await act(async () => {
    await new Promise(resolve => window.setTimeout(resolve, 0))
  })
}

const plan = {
  id: 1,
  name: '标准套餐',
  lock_version: 1,
  current_revision_id: 1,
  latest_revision_id: 1,
}

function orderingNode(key: string, name: string, protocol: string, region: string, position: number) {
  return {
    key,
    node_type: key.startsWith('inbound:') ? 'inbound' : 'proxy_path',
    node_id: Number(key.split(':')[1]),
    name,
    group: 'default',
    entry_protocol: protocol,
    entry_region: region,
    exit_region: region,
    manual_position: position,
    effective_position: position,
    renderable: true,
  }
}

describe('PlanNodeOrderingPanel', () => {
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

  it('lets a standalone SSH inbound move among mixed nodes and saves that order', async () => {
    const sshKey = 'inbound:31'
    const nodes = [
      orderingNode('proxy_path:1', '🇩🇪 9929', 'vless', 'DE', 0),
      orderingNode('proxy_path:2', '🇭🇰 WAWO', 'vless', 'HK', 1),
      orderingNode('proxy_path:3', '🇯🇵 NB TYO', 'vless', 'JP', 2),
      orderingNode(sshKey, '🇯🇵 沪日｜SSH', 'ssh', 'JP', 3),
      orderingNode('proxy_path:4', '🇯🇵 沪日｜HY2', 'hysteria2', 'JP', 4),
    ]
    const savedBodies: any[] = []
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/subscription-plans/1/ordering') {
        return {
          plan_id: 1,
          lock_version: 1,
          base_revision_id: 1,
          revision_id: 1,
          version_created_at: '2026-08-29T00:00:00Z',
          read_only: false,
          is_current: true,
          is_latest: true,
          pending_revision_id: 0,
          policy: {
            version: 2,
            mode: 'manual',
            manual_seed: 'exit_region',
            exit_region_order: ['DE', 'HK', 'JP'],
            entry_region_order_mode: 'inherit_exit',
            entry_region_order: [],
            entry_order: [],
            new_node_placement: 'by_template',
            unmatched_placement: 'append',
          },
          nodes,
          unplaced_count: 0,
          warnings: [],
        }
      }
      if (path === '/subscription-plans/1/ordering/versions') {
        savedBodies.push(JSON.parse(String(init?.body || '{}')))
        return { revision: { created_at: '2026-08-29T00:00:01Z' }, effective_immediately: true }
      }
      throw new Error(`unexpected request: ${path}`)
    })

    await act(async () => {
      root.render(<PlanNodeOrderingPanel plan={plan} data={{ subscription_plans: [plan] }} client={{ request }} />)
    })
    await flushEffects()

    const sshRow = Array.from(container.querySelectorAll('.sortable-row')).find(row => row.textContent?.includes('沪日｜SSH')) as HTMLElement | undefined
    expect(sshRow).toBeTruthy()
    const moveTop = sshRow?.querySelector('button[aria-label="移到顶部"]') as HTMLButtonElement | undefined
    expect(moveTop?.disabled).toBe(false)
    const moveUp = sshRow?.querySelector('button[aria-label="上移"]') as HTMLButtonElement | undefined
    expect(moveUp?.disabled).toBe(false)
    const moveDown = sshRow?.querySelector('button[aria-label="下移"]') as HTMLButtonElement | undefined
    expect(moveDown?.disabled).toBe(false)
    act(() => moveTop?.click())
    await flushEffects()

    const saveButton = Array.from(container.querySelectorAll('button')).find(button => button.textContent?.includes('保存为新版本'))
    act(() => saveButton?.click())
    await flushEffects()

    expect(savedBodies).toHaveLength(1)
    expect(savedBodies[0].manual_node_order).toEqual([
      sshKey,
      'proxy_path:1',
      'proxy_path:2',
      'proxy_path:3',
      'proxy_path:4',
    ])
  })
})
