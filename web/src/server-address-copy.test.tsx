// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ServerAddressBadge } from './main'
import type { Server } from './components/proxy-path/types'

const server = {
  id: 1,
  name: '测试服务器',
  status: 'online',
  public_ipv4: '203.0.113.10',
  entry_ip_mode: 'custom',
  entry_address: '198.51.100.20',
} as Server

let host: HTMLDivElement
let root: Root
let copy: ReturnType<typeof vi.fn>
let openInspector: ReturnType<typeof vi.fn>

beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  copy = vi.fn().mockResolvedValue(undefined)
  openInspector = vi.fn()
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true })
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: copy } })
})

afterEach(async () => {
  await act(async () => root.unmount())
  host.remove()
  document.querySelector('.server-address-entry-tip')?.remove()
  vi.unstubAllGlobals()
})

function renderAddress(hover: boolean) {
  vi.stubGlobal('matchMedia', vi.fn().mockImplementation(() => ({ matches: hover })))
  act(() => root.render(<div onClick={openInspector}><ServerAddressBadge server={server} /></div>))
  return host.querySelector<HTMLButtonElement>('.server-address-line.v4')!
}

it('copies an exit IP without opening the surrounding server card', async () => {
  const address = renderAddress(false)
  await act(async () => address.click())
  expect(copy).toHaveBeenCalledExactlyOnceWith(server.public_ipv4)
  expect(openInspector).not.toHaveBeenCalled()
})

it('does not turn a touch hover on an exit IP into the entry tooltip', async () => {
  const address = renderAddress(false)
  await act(async () => {
    address.dispatchEvent(new MouseEvent('mouseover', { bubbles: true, relatedTarget: document.body }))
  })
  expect(document.querySelector('.server-address-entry-tip')).toBeNull()
  const entry = host.querySelector<HTMLButtonElement>('.server-address-entry-inline button')
  expect(entry).not.toBeNull()
  await act(async () => entry!.click())
  expect(copy).toHaveBeenCalledExactlyOnceWith(server.entry_address)
  expect(openInspector).not.toHaveBeenCalled()
})

it('copies an entry IP from the hover tooltip without opening the server card', async () => {
  const address = renderAddress(true)
  await act(async () => {
    address.dispatchEvent(new MouseEvent('mouseover', { bubbles: true, relatedTarget: document.body }))
  })
  const entry = document.querySelector<HTMLButtonElement>('.server-address-entry-tip button')
  expect(entry).not.toBeNull()
  await act(async () => entry!.click())
  expect(copy).toHaveBeenCalledExactlyOnceWith(server.entry_address)
  expect(openInspector).not.toHaveBeenCalled()
})
