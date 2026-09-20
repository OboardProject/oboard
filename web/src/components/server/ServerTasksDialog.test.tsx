// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ServerTasksDialog } from './ServerTasksDialog'
import type { Server } from '../proxy-path/types'

let host: HTMLDivElement
let root: Root
beforeEach(() => {
  ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
})
afterEach(() => {
  act(() => root.unmount())
  host.remove()
})
const button = (label: string) => [...document.body.querySelectorAll('button')].find(item => item.textContent === label)!
const task = { id: 1, type: 'apply_deployment', status: 'running', result_json: '{}' }

it('keeps records during refresh, updates the selected task and retains stale data on refresh failure', async () => {
  let resolve!: (value: unknown) => void
  let reject!: (reason: Error) => void
  const client = { request: vi.fn(() => new Promise((done, fail) => { resolve = done; reject = fail })) }
  act(() => root.render(<ServerTasksDialog server={{ id: 7, name: 'Node' } as Server} client={client} onClose={vi.fn()} />))
  await act(async () => resolve({ tasks: [task] }))
  act(() => document.body.querySelector<HTMLButtonElement>('[aria-label="查看任务 #1"]')!.click())
  expect(document.body.querySelector('details')?.open).toBe(false)
  act(() => button('刷新').click())
  expect(document.body.querySelector('table')).not.toBeNull()
  expect(document.body.querySelector('.server-tasks-detail')?.textContent).toContain('执行中')
  await act(async () => resolve({ tasks: [{ ...task, status: 'succeeded' }] }))
  expect(document.body.querySelector('.server-tasks-detail')?.textContent).toContain('成功')
  act(() => button('刷新').click())
  await act(async () => reject(new Error('network unavailable')))
  expect(document.body.querySelector('table')).not.toBeNull()
  expect(document.body.textContent).toContain('以下保留上次加载的记录')
})

it('ignores a late read for the previous server and includes rollback failures in the failed filter', async () => {
  const pending: Array<(value: unknown) => void> = []
  const client = { request: vi.fn(() => new Promise(resolve => pending.push(resolve))) }
  const onClose = vi.fn()
  act(() => root.render(<ServerTasksDialog server={{ id: 7, name: 'First' } as Server} client={client} onClose={onClose} />))
  act(() => root.render(<ServerTasksDialog server={{ id: 8, name: 'Second' } as Server} client={client} onClose={onClose} />))
  await act(async () => pending[1]({ tasks: [{ ...task, id: 2, status: 'rollback_failed' }] }))
  await act(async () => pending[0]({ tasks: [task] }))
  act(() => button('失败').click())
  expect(document.body.querySelector('[aria-label="查看任务 #2"]')).not.toBeNull()
  expect(document.body.querySelector('[aria-label="查看任务 #1"]')).toBeNull()
})
