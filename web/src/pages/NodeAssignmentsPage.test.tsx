// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NodeAssignmentsPage } from './NodeAssignmentsPage'

const node = (name: string) => ({ key: 'inbound:1', type: 'inbound', id: 1, name, source_name: name, plans: [], status: 'ok', effective_users: 0, allow_exceptions: 0, deny_exceptions: 0 })
const flush = async () => { await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) }) }

describe('NodeAssignmentsPage reading context', () => {
  let container: HTMLDivElement
  let root: Root
  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container)
  })
  afterEach(() => { act(() => root.unmount()); container.remove() })

  it('requests the selected page rather than silently showing page one', async () => {
    const request = vi.fn(async (path: string) => {
      const page = new URL(path, 'http://localhost').searchParams.get('page')
      return { nodes: [node(`page-${page}`)], total: 120, page: Number(page), page_size: 50 }
    })
    await act(async () => root.render(<NodeAssignmentsPage data={{ session: { role: 'viewer' } }} client={{ request }} load={vi.fn()} />))
    await flush()
    act(() => Array.from(container.querySelectorAll('button')).find(button => button.textContent === '下一页')?.click())
    await flush()
    expect(container.textContent).toContain('page-2')
    expect(container.textContent).toContain('第 2 / 3 页')
  })

  it('ignores a late search response and keeps current results', async () => {
    let finishOld!: (value: unknown) => void
    const old = new Promise(resolve => { finishOld = resolve })
    const request = vi.fn(async (path: string) => {
      const query = new URL(path, 'http://localhost').searchParams.get('query')
      if (query === 'old') return old
      return { nodes: [node(query || 'initial')], total: 1, page: 1, page_size: 50 }
    })
    await act(async () => root.render(<NodeAssignmentsPage data={{ session: { role: 'viewer' } }} client={{ request }} load={vi.fn()} />))
    await flush()
    const search = container.querySelector<HTMLInputElement>('[aria-label="搜索节点"]')!
    const change = (value: string) => act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(search, value)
      search.dispatchEvent(new Event('input', { bubbles: true }))
    })
    change('old'); await flush(); change('new'); await flush()
    await act(async () => { finishOld({ nodes: [node('stale-result')], total: 1 }); await old })
    expect(container.textContent).toContain('new')
    expect(container.textContent).not.toContain('stale-result')
  })
  it('shows applied search as a removable chip and a batch bar for selected nodes', async () => {
    const request = vi.fn(async (path: string) => {
      const query = new URL(path, 'http://localhost').searchParams.get('query') || ''
      return { nodes: [{ ...node(`东京${query}`), key: 'inbound:1' }, { ...node('香港'), key: 'inbound:2', id: 2 }], total: 2, page: 1, page_size: 50 }
    })
    await act(async () => root.render(<NodeAssignmentsPage data={{ session: { role: 'admin' }, subscription_plans: [] }} client={{ request }} load={vi.fn()} />))
    await flush()
    expect(container.querySelector('.nw-batch-bar')).toBeNull()
    act(() => container.querySelector<HTMLInputElement>('.nw-catalog-table [aria-label="选择 香港"]')!.click())
    expect(container.querySelector('.nw-batch-bar')?.textContent).toContain('已选 1 个节点')

    const search = container.querySelector<HTMLInputElement>('[aria-label="搜索节点"]')!
    act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(search, 'A')
      search.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await flush()
    const chip = container.querySelector<HTMLButtonElement>('[aria-label="移除搜索 A"]')!
    expect(chip.textContent).toContain('搜索：A')
    act(() => chip.click())
    await flush()
    expect(container.querySelector('[aria-label="移除搜索 A"]')).toBeNull()
    expect(new URL(String(request.mock.calls.at(-1)?.[0]), 'http://localhost').searchParams.get('query')).toBeNull()
  })

})
