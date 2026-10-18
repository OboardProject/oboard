// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ServerActionMenu } from './ServerActionMenu'
import type { Server } from '../proxy-path/types'

let host: HTMLDivElement
let root: Root
let sheet = false
beforeEach(() => {
  ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  sheet = false
  vi.spyOn(window, 'requestAnimationFrame').mockImplementation(callback => { callback(0); return 1 })
  vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {})
  vi.stubGlobal('matchMedia', vi.fn().mockImplementation(() => ({ matches: sheet, addEventListener: vi.fn(), removeEventListener: vi.fn() })))
})
afterEach(() => {
  act(() => root.unmount())
  host.remove()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})
const key = (element: Element, value: string) => act(() => {
  element.dispatchEvent(new KeyboardEvent('keydown', { key: value, bubbles: true, cancelable: true }))
})
const labels = () => [...document.querySelectorAll('[role="menuitem"] .fleet-menu-text')].map(item => item.textContent)

it('opens by keyboard, skips unavailable actions, and restores focus on Escape', () => {
  act(() => root.render(<ServerActionMenu server={{ id: 1, name: '一号', status: 'offline' } as Server} role="admin" onAction={vi.fn()} />))
  const trigger = host.querySelector('button')!
  key(trigger, 'ArrowDown')
  expect(labels()).toEqual(['服务器资料', '负载与延迟', '任务记录', '服务器设置', '远程终端', '网络工具', '拨测任务', 'Agent 命令', '删除服务器'])
  expect(document.activeElement?.querySelector('.fleet-menu-text')?.textContent).toBe('服务器资料')
  key(document.activeElement!, 'ArrowDown')
  key(document.activeElement!, 'ArrowDown')
  key(document.activeElement!, 'ArrowDown')
  expect(document.activeElement?.textContent).toBe('服务器设置')
  key(document.activeElement!, 'ArrowDown')
  expect(document.activeElement?.textContent).toBe('网络工具')
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
  expect(labels()).toEqual(['服务器资料', '负载与延迟', '任务记录', '网络工具'])
  act(() => document.querySelector<HTMLButtonElement>('[role="menuitem"]')!.click())
  expect(onAction).toHaveBeenCalledExactlyOnceWith('about', server)
  expect(document.querySelector('[role="menu"]')).toBeNull()
})

it('offers agent maintenance and delivery retry only when they apply', () => {
  const server = { id: 2, name: '二号', status: 'online', agent_id: 'a2', users_confirmed: false } as Server
  act(() => root.render(<ServerActionMenu server={server} role="operator" onAction={vi.fn()} />))
  act(() => host.querySelector('button')!.click())
  expect(labels()).toEqual(expect.arrayContaining(['更新 Agent', 'Agent 维护与日志', '重试授权下发']))
})

it('renders as a bottom sheet with the server name on narrow screens', () => {
  sheet = true
  act(() => root.render(<ServerActionMenu server={{ id: 3, name: '三号' } as Server} role="admin" onAction={vi.fn()} />))
  act(() => host.querySelector('button')!.click())
  expect(document.querySelector('.fleet-menu.is-sheet .fleet-menu-sheet-head strong')?.textContent).toBe('三号')
  act(() => document.querySelector<HTMLButtonElement>('[aria-label="关闭操作菜单"]')!.click())
  expect(document.querySelector('[role="menu"]')).toBeNull()
})
