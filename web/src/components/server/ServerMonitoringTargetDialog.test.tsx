// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ServerMonitoringTargetDialog } from './ServerMonitoringTargetDialog'
import type { Server } from '../proxy-path/types'

let host: HTMLDivElement
let root: Root
beforeEach(() => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  host = document.createElement('div')
  document.body.appendChild(host)
  root = createRoot(host)
})
afterEach(() => {
  act(() => root.unmount())
  host.remove()
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
})

it('loads assigned tasks, restores the saved default, saves only the selection and can reopen it', async () => {
  const server = { id: 7, name: 'Node', monitoring_target_task_id: 4 } as Server
  const tasks = [{ id: 4, name: 'Website', method: 'http', enabled: true, server_ids: [7] }, { id: 5, name: 'Other node', method: 'tcp', enabled: true, server_ids: [8] }, { id: 6, name: 'Disabled', method: 'tcp', enabled: false, server_ids: [7] }]
  const request = vi.fn(async (_path: string, init?: RequestInit) => init?.method === 'PATCH' ? { server: { ...server, ...JSON.parse(String(init.body)) } } : { tasks })
  const client = { request }
  const onSaved = vi.fn()
  const onClose = vi.fn()
  await act(async () => root.render(<ServerMonitoringTargetDialog server={server} client={client} onSaved={onSaved} onClose={onClose} />))
  expect(request).toHaveBeenCalledWith('/servers/7/latency-probe?limit=1', expect.objectContaining({ signal: expect.any(AbortSignal) }))
  const selector = document.body.querySelector<HTMLButtonElement>('[aria-label="监控目标"]')!
  expect(selector.textContent).toContain('Website')
  await act(async () => selector.click())
  const options = [...document.body.querySelectorAll<HTMLButtonElement>('[role="option"]')]
  expect(options.map(option => option.textContent)).toEqual(['公网探测', 'Website · HTTP'])
  await act(async () => options[0].click())
  const save = [...document.body.querySelectorAll('button')].find(button => button.textContent === '保存为默认')!
  await act(async () => save.click())
  expect(request).toHaveBeenLastCalledWith('/servers/7', { method: 'PATCH', body: '{"monitoring_target_task_id":0}' })
  expect(onSaved).toHaveBeenCalledWith(expect.objectContaining({ monitoring_target_task_id: 0 }))
  expect(onClose).toHaveBeenCalledOnce()
  await act(async () => root.render(<ServerMonitoringTargetDialog key="reopen" server={onSaved.mock.calls[0][0]} client={client} onSaved={onSaved} onClose={onClose} />))
  expect(document.body.querySelector('[aria-label="监控目标"]')?.textContent).toContain('公网探测')
})

it('keeps the dialog open with an accessible error when saving fails', async () => {
  const onClose = vi.fn()
  const onSaved = vi.fn()
  const client = { request: vi.fn(async (_path: string, init?: RequestInit) => { if (init?.method === 'PATCH') throw new Error('服务器保存失败'); return { tasks: [] } }) }
  await act(async () => root.render(<ServerMonitoringTargetDialog server={{ id: 7, name: 'Node' } as Server} client={client} onSaved={onSaved} onClose={onClose} />))
  await act(async () => [...document.body.querySelectorAll('button')].find(button => button.textContent === '保存为默认')!.click())
  expect(document.body.querySelector('[role="alert"]')?.textContent).toContain('服务器保存失败')
  expect(onClose).not.toHaveBeenCalled()
  expect(onSaved).not.toHaveBeenCalled()
})
