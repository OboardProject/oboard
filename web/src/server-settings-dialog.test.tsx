// @vitest-environment jsdom
import * as React from 'react'
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ServerSettingsDialog } from './main'

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

const baseDraft = { name: 'server-1', region_mode: 'auto', entry_ip_mode: 'auto', entry_address: '', listen_mode: 'auto', listen_ip: '0.0.0.0', ip_stack: 'auto', udp_inbound_mode: 'allow', port_range_start: 10000, port_range_end: 20000, internal_port_range_start: 30000, internal_port_range_end: 59999, auto_renew_enabled: true, renewal_cycle: '', display_tags: [] }

function Harness({ onSubmit }: { onSubmit: (draft: any) => Promise<void> }) {
  const [draft, setDraft] = useState<any>(baseDraft)
  return <ServerSettingsDialog mode="create" draft={draft} update={patch => setDraft((old: any) => ({ ...old, ...patch }))} onCancel={vi.fn()} onSubmit={() => onSubmit(draft)} />
}

const tab = (label: string) => [...document.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find(item => item.textContent?.startsWith(label))!
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>('button')].find(item => item.textContent === label)!

it('navigates sections and sends a missing renewal cycle back to billing before creating', async () => {
  const onSubmit = vi.fn().mockResolvedValue(undefined)
  await act(async () => root.render(<Harness onSubmit={onSubmit} />))
  expect(tab('基本信息').getAttribute('aria-selected')).toBe('true')
  expect(document.querySelectorAll('[role="tab"]')).toHaveLength(5)

  await act(async () => button('下一项').click())
  expect(tab('计费与流量').getAttribute('aria-selected')).toBe('true')
  await act(async () => tab('主机与安全').click())
  expect(document.querySelector('[role="tabpanel"]')?.textContent).toContain('时间校准')
  expect([...document.querySelectorAll('.ss-group-title')].map(title => title.textContent)).toEqual(['主机'])

  await act(async () => button('创建').click())
  expect(onSubmit).not.toHaveBeenCalled()
  expect(tab('计费与流量').getAttribute('aria-selected')).toBe('true')
  expect(document.querySelector('#server-renewal-cycle-error')?.textContent).toContain('必须选择续期周期')
  expect(tab('计费与流量').querySelector('.ss-nav-alert')).not.toBeNull()
})

it('blocks saving without a server name', async () => {
  const onSubmit = vi.fn().mockResolvedValue(undefined)
  await act(async () => root.render(<ServerSettingsDialog mode="create" draft={{ ...baseDraft, name: '  ', auto_renew_enabled: false }} update={vi.fn()} onCancel={vi.fn()} onSubmit={onSubmit} />))
  expect(button('创建').disabled).toBe(true)
  expect(tab('基本信息').querySelector('.ss-nav-alert')).not.toBeNull()
})
