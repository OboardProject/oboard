// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ServerNetworkDialog } from './ServerNetworkDialog'
import { ServerSystemDialog } from './ServerSystemDialog'
import type { Server } from '../proxy-path/types'

let host: HTMLDivElement
let root: Root
const server = { id: 7, name: 'Do not overwrite this name', region_code: 'JP', status: 'online', agent_id: 'agent-7' } as Server
const button = (label: string) => [...document.body.querySelectorAll('button')].find(item => item.textContent === label)!
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

it('sends only fields owned by network, MTU and system forms', async () => {
  const client = { request: vi.fn(async (_path: string, _init?: RequestInit) => ({ server, mtu_detections: [] })) }
  await act(async () => root.render(<ServerNetworkDialog key="network" server={server} initialTab="settings" data={{}} client={client} role="admin" onClose={vi.fn()} />))
  await act(async () => button('保存网络设置').click())
  await act(async () => root.render(<ServerNetworkDialog key="mtu" server={server} initialTab="mtu" data={{}} client={client} role="admin" onClose={vi.fn()} />))
  await act(async () => button('保存设置').click())
  await act(async () => root.render(<ServerSystemDialog key="system" server={server} initialTab="settings" data={{}} client={client} role="admin" controllerURL="https://controller.invalid" onClose={vi.fn()} />))
  await act(async () => button('保存系统设置').click())
  const writes = client.request.mock.calls
  const patches = writes.filter(([, init]) => init?.method === 'PATCH').map(([, init]) => JSON.parse(String(init?.body)))
  expect(patches).toHaveLength(3)
  for (const patch of patches) {
    expect(patch).not.toHaveProperty('name')
    expect(patch).not.toHaveProperty('region_code')
    expect(patch).not.toHaveProperty('agent_id')
    expect(patch).not.toHaveProperty('id')
  }
  expect(patches[0]).toHaveProperty('listen_mode')
  expect(patches[1]).toHaveProperty('mtu_mode')
  expect(patches[2]).toHaveProperty('time_correction_mode')
})

it('keeps DNS and MTU controls read-only for viewers', async () => {
  const client = { request: vi.fn(async () => ({ mtu_detections: [] })) }
  await act(async () => root.render(<ServerNetworkDialog key="dns" server={server} initialTab="dns" data={{}} client={client} role="viewer" onClose={vi.fn()} />))
  expect(button('仅保存').disabled).toBe(true)
  expect(document.body.querySelector<HTMLInputElement>('[role="switch"]')?.disabled).toBe(true)
  await act(async () => root.render(<ServerNetworkDialog key="mtu" server={server} initialTab="mtu" data={{}} client={client} role="viewer" onClose={vi.fn()} />))
  expect(button('保存设置').disabled).toBe(true)
  expect(button('立即检测 MTU').disabled).toBe(true)
})
