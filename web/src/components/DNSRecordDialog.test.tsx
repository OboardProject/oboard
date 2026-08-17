// @vitest-environment jsdom

import React, { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { DNSRecordDialog, dnsRecordDraftFromRecord, dnsRecordPayload, type DNSRecordDraft } from './DNSRecordDialog'

vi.mock('./ui/motion', () => ({
  MotionDialogPanel: ({ children }: { children: React.ReactNode }) => <section>{children}</section>,
}))

describe('DNSRecordDialog', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    document.body.querySelectorAll('.custom-select-menu').forEach(element => element.remove())
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('shows every editable record parameter together', () => {
    const initial = dnsRecordDraftFromRecord({ type: 'AAAA', name: 'edge.example.com', content: '2001:db8::1', comment: '边缘入口', ttl: 120, proxied: true })
    const onSubmit = vi.fn(async () => {})
    function Harness() {
      const [draft, setDraft] = useState<DNSRecordDraft>(initial)
      return <DNSRecordDialog
        zoneOptions={[{ credential: { name: '生产域名', provider: 'cloudflare' }, zone: { id: 7, zone_name: 'example.com' } }]}
        zoneID={7}
        setZoneID={() => {}}
        draft={draft}
        setDraft={setDraft}
        serverName={() => ''}
        editing
        saving={false}
        onCancel={() => {}}
        onSubmit={onSubmit}
      />
    }

    act(() => root.render(<Harness />))

    expect(container.textContent).toContain('编辑解析记录')
    expect(container.querySelector<HTMLButtonElement>('[aria-label="域名"]')?.disabled).toBe(true)
    expect(container.querySelector('[aria-label="记录类型"]')).not.toBeNull()
    expect(container.querySelector<HTMLInputElement>('[aria-label="主机记录"]')?.value).toBe('edge')
    expect(container.querySelector<HTMLInputElement>('[aria-label="记录值"]')?.value).toBe('2001:db8::1')
    expect(container.querySelector<HTMLInputElement>('[aria-label="TTL"]')?.value).toBe('1')
    expect(container.querySelector<HTMLInputElement>('[aria-label="解析备注"]')?.value).toBe('边缘入口')
    expect(container.querySelector<HTMLInputElement>('[aria-label="Cloudflare 代理"]')?.checked).toBe(true)
    expect(container.querySelector('button[type="submit"]')?.textContent).toBe('保存修改')

    act(() => container.querySelector<HTMLButtonElement>('button[type="submit"]')?.click())
    expect(onSubmit).toHaveBeenCalledTimes(1)
  })

  it('keeps record ownership metadata in a full edit payload', () => {
    const draft = dnsRecordDraftFromRecord({ type: 'a', name: 'edge.example.com', content: '192.0.2.1', comment: '入口', ttl: 60, proxied: false })
    expect(dnsRecordPayload(draft, { id: 'record-1', server_id: 9, inbound_id: 12, enabled: false })).toEqual({
      id: 'record-1',
      server_id: 9,
      inbound_id: 12,
      enabled: false,
      type: 'A',
      name: 'edge.example.com',
      content: '192.0.2.1',
      comment: '入口',
      ttl: 60,
      proxied: false,
    })
  })
})
