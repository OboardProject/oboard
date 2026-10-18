// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { Server } from '../../proxy-path/types'
import { FilterPopover, FleetSummary, RegionFilter, fleetStats, serverNeedsAttention } from './FleetToolbar'

let host: HTMLDivElement
let root: Root

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
})

afterEach(async () => {
  await act(async () => root.unmount())
  host.remove()
  vi.unstubAllGlobals()
})

const servers = [
  { id: 1, status: 'online', agent_id: 'a', agent_build: '1', network_download_bps: 100, network_upload_bps: 50, traffic_upload_bytes: 10, traffic_download_bytes: 20 },
  { id: 2, status: 'offline', agent_id: 'b', agent_build: '2', network_download_bps: 999 },
  { id: 3, status: 'offline' },
  { id: 4, status: 'online', agent_id: 'd', agent_build: '2', authorization_confirmed: false },
] as Server[]

it('summarises the fleet without counting offline traffic or offline servers as attention', () => {
  const stats = fleetStats(servers, '2')
  expect(stats).toMatchObject({ total: 4, online: 2, offline: 1, unenrolled: 1, attention: 2, downloadBps: 100, uploadBps: 50, trafficBytes: 30 })
  expect(serverNeedsAttention(servers[0], '2')).toBe(true)
  expect(serverNeedsAttention(servers[1], '2')).toBe(false)
  expect(serverNeedsAttention(servers[2], '2')).toBe(false)
})

it('toggles a status filter from the summary', async () => {
  const onStatusFilter = vi.fn()
  await act(async () => root.render(<FleetSummary stats={fleetStats(servers, '2')} statusFilter="offline" onStatusFilter={onStatusFilter} />))
  const offline = host.querySelector<HTMLButtonElement>('.fleet-stat.is-offline')!
  expect(offline.getAttribute('aria-pressed')).toBe('true')
  await act(async () => offline.click())
  expect(onStatusFilter).toHaveBeenLastCalledWith('all')
  await act(async () => host.querySelector<HTMLButtonElement>('.fleet-stat.is-attention')!.click())
  expect(onStatusFilter).toHaveBeenLastCalledWith('attention')
})

it('opens filters in a floating popover and resets status and sort together', async () => {
  const onStatus = vi.fn()
  const onSort = vi.fn()
  await act(async () => root.render(<FilterPopover statusFilter="online" onStatusFilterChange={onStatus} sortMode="custom" onSortModeChange={onSort} />))
  await act(async () => host.querySelector<HTMLButtonElement>('.fleet-filter-trigger')!.click())
  const panel = document.querySelector<HTMLElement>('.fleet-filter-panel')!
  expect(panel.parentElement).not.toBe(host)
  expect(panel.textContent).toContain('拖动卡片左侧手柄')
  await act(async () => [...panel.querySelectorAll('button')].find(button => button.textContent === '离线')!.click())
  expect(onStatus).toHaveBeenLastCalledWith('offline')
  await act(async () => [...panel.querySelectorAll('button')].find(button => button.textContent === '重置')!.click())
  expect(onStatus).toHaveBeenLastCalledWith('all')
  expect(onSort).toHaveBeenLastCalledWith('created')
})

it('searches regions and returns focus to the trigger after choosing one', async () => {
  const onChange = vi.fn()
  const regions = [{ code: 'JP', label: '日本', count: 3 }, { code: 'US', label: '美国', count: 2 }]
  await act(async () => root.render(<RegionFilter value="all" onChange={onChange} regions={regions} total={5} />))
  const trigger = host.querySelector<HTMLButtonElement>('.fleet-region-trigger')!
  expect(trigger.getAttribute('aria-label')).toBe('按地区筛选：全部地区，5 台')
  await act(async () => trigger.click())
  const input = document.querySelector<HTMLInputElement>('.fleet-region-panel input')!
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    setter.call(input, 'us')
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
  const options = [...document.querySelectorAll<HTMLButtonElement>('.fleet-region-panel [role="option"]')]
  expect(options.map(option => option.textContent)).toEqual(['美国2'])
  await act(async () => options[0].click())
  expect(onChange).toHaveBeenCalledExactlyOnceWith('US')
  expect(document.activeElement).toBe(trigger)
  expect(document.querySelector('.fleet-region-panel')).toBeNull()
})
