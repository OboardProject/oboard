// @vitest-environment jsdom
import { act, type SelectHTMLAttributes } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { NetworkDNSTab } from './NetworkDNSTab'
import type { Server } from '../../proxy-path/types'

vi.mock('../../ui/select', () => ({ Select: (props: SelectHTMLAttributes<HTMLSelectElement>) => <select {...props} /> }))
let host: HTMLDivElement
let root: Root
const server = { id: 1, name: '一号' } as Server
const lists = [{ id: 1, name: '基础', kind: 'bootstrap', revision: 1, candidates: [], enabled: true }]
const policy = { server_id: 1, encrypted_list_id: 0, bootstrap_list_id: 1, revision: 1, strategy: 'auto', auto_test: 'first_apply', test_interval_seconds: 3600, encrypted_selected: [], bootstrap_selected: [], last_error: '' }
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
const strategy = () => host.querySelectorAll('select')[2]
const changeStrategy = (value: string) => act(() => {
  strategy().value = value
  strategy().dispatchEvent(new Event('change', { bubbles: true }))
})
const button = (text: string) => [...host.querySelectorAll('button')].find(item => item.textContent === text)!

it('preserves dirty fields on list refresh and blocks an observed concurrent revision until explicitly reloaded', () => {
  const client = { request: vi.fn() }
  const render = (nextPolicy = policy, nextLists = lists) => act(() => root.render(<NetworkDNSTab server={server} policy={nextPolicy} lists={nextLists} benchmarks={[]} client={client} />))
  render()
  changeStrategy('prefer_ipv6')
  render(policy, [...lists, { ...lists[0], id: 2 }])
  expect(strategy().value).toBe('prefer_ipv6')
  render({ ...policy, revision: 2, strategy: 'ipv4_only' })
  expect(strategy().value).toBe('prefer_ipv6')
  expect(host.textContent).toContain('您的输入已保留')
  expect(button('仅保存').disabled).toBe(true)
  act(() => button('放弃当前草稿，载入最新配置').click())
  expect(strategy().value).toBe('ipv4_only')
  expect(button('仅保存').disabled).toBe(false)
  expect(client.request).not.toHaveBeenCalled()
})

it('submits one snapshot and retains edits made while saving, ignoring stale policy refreshes', async () => {
  let resolve!: (value: unknown) => void
  const client = { request: vi.fn((_path: string, _init: RequestInit) => new Promise(done => { resolve = done })) }
  const render = (nextLists = lists) => act(() => root.render(<NetworkDNSTab server={server} policy={policy} lists={nextLists} benchmarks={[]} client={client} />))
  render()
  changeStrategy('prefer_ipv6')
  act(() => { button('仅保存').click(); button('仅保存').click() })
  expect(client.request).toHaveBeenCalledTimes(1)
  expect(JSON.parse(String(client.request.mock.calls[0][1].body)).strategy).toBe('prefer_ipv6')
  changeStrategy('ipv4_only')
  await act(async () => resolve({ dns_policy: { ...policy, strategy: 'prefer_ipv6', revision: 2 } }))
  render([...lists])
  expect(strategy().value).toBe('ipv4_only')
})

it('disables periodic testing in read-only mode', () => {
  act(() => root.render(<NetworkDNSTab server={server} policy={policy} lists={lists} benchmarks={[]} client={{ request: vi.fn() }} disabled />))
  const toggle = host.querySelector<HTMLInputElement>('[role="switch"]')!
  expect(toggle.disabled).toBe(true)
  expect(button('仅保存').disabled).toBe(true)
})
