// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { ProxyDSCPSettings } from './ProxyDSCPSettings'

async function fixture(global: boolean, single: boolean) {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  const host = document.createElement('div'); document.body.appendChild(host)
  const root = createRoot(host)
  const request = vi.fn(async (path: string) => path === '/servers' ? [{ id: 7, name: '节点甲' }] : {})
  const requestV2 = vi.fn(async (_path: string, init?: RequestInit) => { if (init?.method === 'POST') { const body = JSON.parse(init.body as string); if ('enabled' in body) single = body.enabled } return { enabled: single, effective_enabled: global || single, state: 'partial', report: { excluded: ['hysteria2'] } } })
  await act(async () => root.render(<ProxyDSCPSettings data={{ settings: { proxy_dscp_enabled: global } }} client={{ request, requestV2 }} load={async () => {}} notify={vi.fn()} />))
  const close = () => { act(() => root.unmount()); host.remove(); (globalThis as any).IS_REACT_ACT_ENVIRONMENT = false }
  return { host, root, request, requestV2, close }
}
it('saves the global switch without calling server updates', async () => {
  const f = await fixture(false, false)
  try {
    const toggle = f.host.querySelector<HTMLInputElement>('input[aria-label="全局启用代理 DSCP"]')!
    expect(toggle.checked).toBe(false)
    await act(async () => toggle.click())
    expect(f.request).toHaveBeenCalledWith('/settings', expect.objectContaining({ method: 'POST', body: JSON.stringify({ proxy_dscp_enabled: true }) }))
    expect(f.requestV2).not.toHaveBeenCalled()
  } finally { f.close() }
})
it('uses independent server settings and displays actual coverage', async () => {
  const f = await fixture(false, true)
  try {
    await act(async () => Array.from(f.host.querySelectorAll('button')).find(b => b.textContent?.includes('管理服务器'))!.click())
    const toggle = document.querySelector<HTMLInputElement>('input[aria-label="节点甲代理 DSCP"]')!
    expect(toggle.checked).toBe(true); expect(toggle.disabled).toBe(false)
    expect(document.body.textContent).toContain('部分支持')
    await act(async () => toggle.click())
    expect(f.requestV2).toHaveBeenCalledWith('/servers/7/proxy-dscp', expect.objectContaining({ method: 'POST', body: expect.stringContaining('"enabled":false') }))
  } finally { f.close() }
})
it('global enable locks individual switches without erasing their saved values', async () => {
  const f = await fixture(true, false)
  try {
    await act(async () => Array.from(f.host.querySelectorAll('button')).find(b => b.textContent?.includes('管理服务器'))!.click())
    const toggle = document.querySelector<HTMLInputElement>('input[aria-label="节点甲代理 DSCP"]')!
    expect(toggle.checked).toBe(true); expect(toggle.disabled).toBe(true)
    expect(document.body.textContent).toContain('关闭全局后可编辑')
    expect(f.requestV2.mock.calls.every(([, init]) => !init?.method)).toBe(true)
  } finally { f.close() }
})

it('does not open help on dialog entry but keeps keyboard help available', async () => {
  const f = await fixture(false, false)
  try {
    await act(async () => Array.from(f.host.querySelectorAll('button')).find(b => b.textContent?.includes('管理服务器'))!.click())
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!
    expect(document.activeElement).toBe(dialog.querySelector('button[aria-label="关闭"]'))
    expect(document.querySelector('[role="tooltip"]')).toBeNull()
    const help = dialog.querySelector<HTMLButtonElement>('button[aria-label="代理 DSCP 覆盖范围说明"]')!
    await act(async () => help.focus())
    expect(document.querySelector('[role="tooltip"]')?.textContent).toContain('HY2（Hysteria2）暂不支持')
    await act(async () => help.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(document.querySelector('[role="tooltip"]')).toBeNull()
    expect(document.querySelector('[role="dialog"]')).not.toBeNull()
  } finally { f.close() }
})
