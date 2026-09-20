// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { NodeScopeMenu } from './NodeScopeMenu'

it('focuses the scope menu, skips disabled options and returns focus to its trigger', () => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  const host = document.createElement('div')
  const trigger = document.createElement('button')
  document.body.append(host, trigger)
  trigger.focus()
  const root = createRoot(host)
  const onSelect = vi.fn()
  try {
    act(() => root.render(<NodeScopeMenu x={9999} y={9999} node={{ key: 'inbound:1', id: 1, type: 'inbound', name: '香港', exit_region: 'HK' }} onSelect={onSelect} onClose={() => {}} />))
    const menu = document.querySelector<HTMLElement>('[role="menu"]')!
    expect(document.activeElement?.textContent).toBe('仅选择此节点')
    act(() => menu.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true })))
    expect(document.activeElement?.textContent).toBe('选择同一出口地区的全部节点')
    act(() => (document.activeElement as HTMLButtonElement).click())
    expect(onSelect).toHaveBeenCalledWith({ kind: 'exit_region' })
    expect(parseFloat(menu.style.left)).toBeLessThan(window.innerWidth)
    expect(menu.style.overflowY).toBe('auto')
  } finally {
    act(() => root.unmount())
    expect(document.activeElement).toBe(trigger)
    host.remove(); trigger.remove()
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
  }
})
