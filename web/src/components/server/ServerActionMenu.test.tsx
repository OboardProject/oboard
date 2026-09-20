// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ServerActionMenu } from './ServerActionMenu'
import type { Server } from '../proxy-path/types'

let host: HTMLDivElement
let root: Root
beforeEach(() => {
  ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  vi.spyOn(window, 'requestAnimationFrame').mockImplementation(callback => { callback(0); return 1 })
  vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {})
})
afterEach(() => {
  act(() => root.unmount())
  host.remove()
  vi.restoreAllMocks()
})
const key = (element: Element, value: string) => act(() => {
  element.dispatchEvent(new KeyboardEvent('keydown', { key: value, bubbles: true, cancelable: true }))
})

it('opens by keyboard, skips unavailable actions, and restores focus on Escape', () => {
  act(() => root.render(<ServerActionMenu server={{ id: 1, name: '一号', status: 'offline' } as Server} role="admin" onAction={vi.fn()} />))
  const trigger = host.querySelector('button')!
  expect(trigger.style.width).toBe('44px')
  key(trigger, 'ArrowDown')
  expect(document.activeElement?.textContent).toBe('服务器资料')
  key(document.activeElement!, 'ArrowDown')
  expect(document.activeElement?.textContent).toBe('服务器设置')
  key(document.activeElement!, 'ArrowDown')
  expect(document.activeElement?.textContent).toBe('Agent 命令')
  key(document.activeElement!, 'End')
  expect(document.activeElement?.textContent).toBe('删除服务器')
  key(document.activeElement!, 'Escape')
  expect(document.querySelector('[role="menu"]')).toBeNull()
  expect(document.activeElement).toBe(trigger)
})

it('keeps viewer permissions and invokes one selected action', () => {
  const onAction = vi.fn()
  const server = { id: 1, name: '一号' } as Server
  act(() => root.render(<ServerActionMenu server={server} role="viewer" onAction={onAction} />))
  act(() => host.querySelector('button')!.click())
  expect(document.body.textContent).not.toContain('删除服务器')
  expect(document.body.textContent).not.toContain('远程终端')
  act(() => document.querySelector<HTMLButtonElement>('[role="menuitem"]')!.click())
  expect(onAction).toHaveBeenCalledExactlyOnceWith('about', server)
  expect(document.querySelector('[role="menu"]')).toBeNull()
})
