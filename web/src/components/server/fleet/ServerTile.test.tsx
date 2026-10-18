// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { Server } from '../../proxy-path/types'
import { ServerAddresses } from './ServerAddresses'
import { ServerRow, ServerTile } from './ServerTile'

let host: HTMLDivElement
let root: Root
let copy: ReturnType<typeof vi.fn>

const server = {
  id: 7,
  name: 'Tokyo',
  status: 'online',
  agent_id: 'agent-7',
  agent_build: '20260101000000',
  cpu: 'AMD EPYC',
  cpu_cores: 2,
  cpu_usage_percent: 33.6,
  memory_used_bytes: 512 * 1024 ** 2,
  memory_total_bytes: 1024 ** 3,
  disk_bytes: 15 * 1024 ** 3,
  disk_total_bytes: 20 * 1024 ** 3,
  traffic_upload_bytes: 600 * 1024 ** 3,
  traffic_download_bytes: 1100 * 1024 ** 3,
  traffic_limit_bytes: 2 * 1024 ** 4,
  public_ipv4: '203.0.113.10',
  public_ipv6: '2001:db8::1:2',
  entry_ip_mode: 'custom',
  entry_address: '198.51.100.20',
  time_check_status: 'skewed',
} as Server

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  copy = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true })
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: copy } })
})

afterEach(async () => {
  await act(async () => root.unmount())
  host.remove()
  vi.unstubAllGlobals()
})

it('copies egress and entry addresses without opening the surrounding surface', async () => {
  const open = vi.fn()
  await act(async () => root.render(<div onClick={open}><ServerAddresses server={server} /></div>))
  const buttons = [...host.querySelectorAll<HTMLButtonElement>('.fleet-address')]
  expect(buttons.map(button => button.dataset.kind)).toEqual(['v4', 'v6', 'entry'])
  for (const button of buttons) await act(async () => button.click())
  expect(copy.mock.calls.map(call => call[0])).toEqual(['203.0.113.10', '2001:db8::1:2', '198.51.100.20'])
  expect(open).not.toHaveBeenCalled()
})

it('shows clear card metrics and routes card, name and chip clicks to separate actions', async () => {
  const onAction = vi.fn()
  await act(async () => root.render(<ServerTile server={server} role="admin" expectedBuild="20260201000000" onAction={onAction} />))
  const tile = host.querySelector('.fleet-tile')!
  expect(tile.textContent).toContain('2 核')
  expect(tile.textContent).toContain('34%')
  expect(tile.textContent).toContain('1.7 TB / 2.0 TB')
  expect(host.querySelector('.fleet-meter[title="AMD EPYC"]')).not.toBeNull()
  const disk = [...host.querySelectorAll('.fleet-meter')].find(item => item.textContent?.startsWith('磁盘'))!
  expect(disk.querySelector('.fleet-meter-fill')?.classList.contains('is-warning')).toBe(true)

  await act(async () => (tile.querySelector('.fleet-meter') as HTMLElement).click())
  expect(onAction).toHaveBeenLastCalledWith('resource-details', server)
  await act(async () => host.querySelector<HTMLButtonElement>('.fleet-name')!.click())
  expect(onAction).toHaveBeenLastCalledWith('inspect', server)
  const chips = [...host.querySelectorAll<HTMLButtonElement>('button.fleet-chip')]
  expect(chips.map(chip => chip.textContent)).toEqual(['时间偏差过大', 'Agent 待更新'])
  await act(async () => chips[1].click())
  expect(onAction).toHaveBeenLastCalledWith('update-agent', server)
  await act(async () => host.querySelector<HTMLButtonElement>('.fleet-target-button')!.click())
  expect(onAction).toHaveBeenLastCalledWith('monitoring-target', server)
  expect(onAction).toHaveBeenCalledTimes(4)
})

it('dims offline metrics and hides admin-only controls from viewers', async () => {
  const offline = { ...server, status: 'offline', last_seen_at: new Date(Date.now() - 3 * 3600_000).toISOString() } as Server
  await act(async () => root.render(<ServerRow server={offline} role="viewer" onAction={vi.fn()} />))
  expect(host.querySelector('.fleet-status')?.textContent).toBe('离线')
  expect(host.querySelector('.fleet-status')?.getAttribute('title')).toBe('3 小时前离线')
  expect(host.querySelector('.fleet-cell-cpu .fleet-compact-meter')?.classList.contains('is-muted')).toBe(true)
  expect(host.querySelector('.fleet-target-button')).toBeNull()
})

it('does not open details when a menu item inside a portal is clicked', async () => {
  const onAction = vi.fn()
  await act(async () => root.render(<ServerTile server={server} role="admin" onAction={onAction} />))
  await act(async () => host.querySelector<HTMLButtonElement>('.fleet-menu-trigger')!.click())
  const settings = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(item => item.textContent === '服务器设置')!
  await act(async () => settings.click())
  expect(onAction).toHaveBeenCalledExactlyOnceWith('edit', server)
})
