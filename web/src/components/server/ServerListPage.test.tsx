// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ServerListPage } from './ServerListPage'

describe('server list paging', () => {
  let container: HTMLDivElement
  let root: Root
  const originalScrollIntoView = Element.prototype.scrollIntoView
  const servers = Array.from({ length: 1000 }, (_, index) => ({ id: index + 1, name: `server-${index + 1}` }))
  const renderItem = vi.fn((server: typeof servers[number], index: number) => <article key={server.id} data-index={index}>{server.name}</article>)
  const render = (items = servers, view: 'grid' | 'list' = 'grid', filter = '') => {
    act(() => root.render(<ServerListPage key={filter} items={items} view={view} renderItem={renderItem} />))
  }
  const next = () => container.querySelector<HTMLButtonElement>('.server-list-pagination-controls > button:last-child')!

  beforeEach(() => {
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    renderItem.mockClear()
    Element.prototype.scrollIntoView = vi.fn()
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.restoreAllMocks()
    Element.prototype.scrollIntoView = originalScrollIntoView
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('only renders 24 cards from a thousand servers, preserving global sort indexes on the next page', () => {
    render()
    expect(renderItem).toHaveBeenCalledTimes(24)
    expect(container.querySelectorAll('article')).toHaveLength(24)
    expect(container.querySelector('article')?.textContent).toBe('server-1')
    act(() => next().click())
    expect(container.querySelector('article')?.textContent).toBe('server-25')
    expect(container.querySelector('article')?.getAttribute('data-index')).toBe('24')
    expect(container.querySelectorAll('article')).toHaveLength(24)
    act(() => container.querySelector<HTMLButtonElement>('nav[aria-label="服务器分页底部"] .server-list-pagination-controls > button:last-child')!.click())
    expect(container.querySelector('article')?.textContent).toBe('server-49')
    expect(document.activeElement?.getAttribute('aria-label')).toBe('服务器页码顶部')
  })

  it('keeps the page for telemetry updates and view changes, but searches across the whole fleet', () => {
    render()
    act(() => next().click())
    render(servers.map(server => ({ ...server, name: `${server.name}-updated` })), 'list')
    expect(container.querySelector('.server-list article')?.textContent).toBe('server-25-updated')
    render(servers.filter(server => server.id === 999), 'list', '999')
    expect(container.querySelector('article')?.textContent).toBe('server-999')
    expect(container.querySelectorAll('article')).toHaveLength(1)
    expect(container.querySelector('nav')).toBeNull()
  })

  it('jumps to the last page and clamps the page when servers are deleted', () => {
    render()
    act(() => container.querySelector<HTMLButtonElement>('[aria-label="服务器页码顶部"]')!.click())
    const options = document.body.querySelectorAll<HTMLButtonElement>('[role="option"]')
    act(() => options[options.length - 1].click())
    expect(container.querySelectorAll('article')).toHaveLength(16)
    expect(container.querySelector('article')?.textContent).toBe('server-985')
    expect(next().disabled).toBe(true)
    render(servers.slice(0, 25))
    expect(container.querySelectorAll('article')).toHaveLength(1)
    expect(container.querySelector('article')?.textContent).toBe('server-25')
    act(() => container.querySelector<HTMLButtonElement>('nav button')!.click())
    expect(container.querySelector('article')?.textContent).toBe('server-1')
  })
})
