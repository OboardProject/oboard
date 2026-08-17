// @vitest-environment jsdom

import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NodeWorkspacePage } from './NodeWorkspacePage'

async function flushEffects() {
  await act(async () => { await new Promise(resolve => window.setTimeout(resolve, 0)) })
}

describe('NodeWorkspacePage', () => {
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
    container.remove()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('loads a role-none user workspace and exposes the three accessible tabs', async () => {
    const workspace = {
      subject: { id: 7, username: 'alice' },
      node_groups: [
        { id: 1, kind: 'oboard', system_key: 'oboard', name: 'OBoard', node_count: 1 },
        { id: 2, kind: 'manual', name: '机场 B', node_count: 2 },
        { id: 3, kind: 'manual', name: '自建 C', node_count: 3 },
      ],
      node_sources: [],
      subscription_outputs: [{ id: 2, name: '默认组合', is_default: true, enabled: true, group_ids: [1, 2, 3] }],
    }
    const request = vi.fn(async (path: string) => {
      if (path === '/node-workspace') return workspace
      if (path === '/node-library') return { nodes: [{ id: 'proxy_path:1', group_id: 1, name: '香港 01', protocol: 'vless', source: 'oboard', copyable: true }] }
      throw new Error(`unexpected request: ${path}`)
    })
    await act(async () => {
      root.render(<NodeWorkspacePage data={{ session: { role: 'none' }, current_user: { id: 7 } }} client={{ request }} load={vi.fn().mockResolvedValue(undefined)} />)
    })
    await flushEffects()

    const tabs = Array.from(container.querySelectorAll('[role="tab"]'))
    expect(tabs.map(tab => tab.textContent)).toEqual(['节点库', '节点组', '组合订阅'])
    expect(tabs[0].getAttribute('aria-selected')).toBe('true')
    expect(container.textContent).toContain('香港 01')

    act(() => (tabs[1] as HTMLButtonElement).click())
    expect(tabs[1].getAttribute('aria-selected')).toBe('true')
    expect(container.textContent).toContain('系统组')

    act(() => (tabs[2] as HTMLButtonElement).click())
    expect(container.textContent).toContain('默认组合')
    expect(container.textContent).toContain('复制订阅')
    expect(Array.from(container.querySelectorAll('.output-group-row > span')).map(item => item.textContent)).toEqual(['1. OBoard', '2. 机场 B', '3. 自建 C'])
    act(() => (container.querySelector('[aria-label="上移 自建 C"]') as HTMLButtonElement).click())
    expect(Array.from(container.querySelectorAll('.output-group-row > span')).map(item => item.textContent)).toEqual(['1. OBoard', '2. 自建 C', '3. 机场 B'])
  })

  it('switches to the administrator global view when the session role arrives after mount', async () => {
    const workspace = {
      subject: { id: 1, username: 'admin' },
      node_groups: [{ id: 1, kind: 'oboard', system_key: 'oboard', name: 'OBoard', node_count: 0 }],
      node_sources: [],
      subscription_outputs: [{ id: 2, name: '默认组合', is_default: true, enabled: true, group_ids: [1] }],
    }
    const request = vi.fn(async (path: string) => {
      if (path === '/node-workspace') return workspace
      if (path === '/node-library') return { nodes: [] }
      if (path.startsWith('/assignable-nodes?')) return { nodes: [], total: 0, page: 1, page_size: 50 }
      throw new Error(`unexpected request: ${path}`)
    })
    const load = vi.fn().mockResolvedValue(undefined)
    await act(async () => {
      root.render(<NodeWorkspacePage data={{ current_user: { id: 1 } }} client={{ request }} load={load} />)
    })
    await flushEffects()
    await act(async () => {
      root.render(<NodeWorkspacePage data={{ session: { role: 'admin' }, current_user: { id: 1 }, users: [{ id: 1, username: 'admin', status: 'active' }], servers: [], subscription_plans: [] }} client={{ request }} load={load} />)
    })
    await flushEffects()

    expect(container.querySelector('[aria-label="节点管理模式"] [aria-pressed="true"]')?.textContent).toBe('全部节点')
    expect(container.querySelector('[role="tablist"]')).toBeNull()
  })
})
