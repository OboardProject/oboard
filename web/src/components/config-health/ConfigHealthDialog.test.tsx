// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ConfigHealthCard } from './ConfigHealthCard'
import { ConfigHealthDialog } from './ConfigHealthDialog'
import type { ConfigHealthFinding } from '../../config-health'

vi.mock('../ui/dialog', () => ({
  Dialog: ({ children, footer, title }: { children: React.ReactNode; footer?: React.ReactNode; title?: string }) => (
    <section aria-label={title}>{children}<footer>{footer}</footer></section>
  ),
}))

const normalizeFinding: ConfigHealthFinding = {
  code: 'inbound.config.invalid',
  severity: 'blocking',
  scope: 'inbound',
  resource_id: 10,
  resource_name: 'vless-a',
  server_id: 1,
  server_name: 'hk-1',
  title: '入口配置不符合当前协议模型',
  detail: 'multiplex.protocol is a client-side option',
  remedy: { kind: 'normalize', summary: '移除当前协议不支持的字段后即可恢复正常', fields: ['multiplex.protocol'] },
}

const deleteFinding: ConfigHealthFinding = {
  code: 'routing_rule.target_path.missing',
  severity: 'blocking',
  scope: 'routing_rule',
  resource_id: 900,
  resource_name: 'to-jp',
  server_id: 1,
  title: '分流规则指向的链路已不存在',
  remedy: { kind: 'delete', summary: '删除这条无法生效的分流规则', destructive: true },
}

const manualFinding: ConfigHealthFinding = {
  code: 'inbound.certificate.missing',
  severity: 'blocking',
  scope: 'inbound',
  resource_id: 11,
  resource_name: 'hy2-a',
  title: '入口绑定的证书已不存在',
  remedy: { kind: 'none', summary: '请为该入口重新选择证书' },
}

type Call = { path: string; body: any }

function makeClient(findings: ConfigHealthFinding[], cleanup?: (body: any) => any) {
  const calls: Call[] = []
  const request = vi.fn(async (path: string, init?: RequestInit) => {
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    calls.push({ path, body })
    if (path === '/config-health') {
      return {
        fingerprint: 'fp-42',
        report: { summary: { blocking: findings.length, warning: 0, notice: 0, total: findings.length }, findings },
      }
    }
    if (path === '/config-health/cleanup') {
      if (cleanup) return cleanup(body)
      return {
        fingerprint: 'fp-42',
        dry_run: !body.confirm,
        applied: body.actions.length,
        skipped: 0,
        failed: 0,
        requires_deployment: Boolean(body.confirm),
        results: body.actions.map((action: any) => ({ ...action, status: 'applied', removed_fields: ['multiplex.protocol'] })),
      }
    }
    return {}
  })
  return { client: { request }, calls }
}

describe('ConfigHealthCard', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })
  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('renders nothing when the configuration is clean', () => {
    // A permanent "all good" badge sits where the real warning will appear and
    // trains operators to skip over it, so a clean report renders no card.
    act(() => root.render(<ConfigHealthCard summary={{ blocking: 0, warning: 0, notice: 0, total: 0 }} onOpen={() => {}} />))
    expect(container.innerHTML).toBe('')
  })

  it('summarises only the severities that are present', () => {
    act(() => root.render(<ConfigHealthCard summary={{ blocking: 2, warning: 1, notice: 0, total: 3 }} onOpen={() => {}} />))
    expect(container.textContent).toContain('配置存在 3 项问题')
    expect(container.textContent).toContain('2 项会阻断下发 · 1 项行为异常')
    expect(container.textContent).not.toContain('待规范化')
  })
})

describe('ConfigHealthDialog', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })
  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.restoreAllMocks()
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
  })

  const mount = async (node: React.ReactElement) => {
    await act(async () => { root.render(node) })
    await act(async () => { await Promise.resolve() })
  }
  const click = async (element: Element | null | undefined) => {
    await act(async () => { (element as HTMLElement | undefined)?.click() })
    await act(async () => { await Promise.resolve() })
  }
  const checkboxFor = (finding: ConfigHealthFinding) =>
    container.querySelector<HTMLInputElement>(`input[aria-label="选择：${finding.title}"]`)
  const buttonNamed = (label: string) =>
    Array.from(container.querySelectorAll('button')).find(button => button.textContent === label)

  it('groups findings by scope and offers no checkbox for a manual-only problem', async () => {
    const { client } = makeClient([normalizeFinding, manualFinding, deleteFinding])
    await mount(<ConfigHealthDialog client={client} canCleanup onClose={() => {}} />)

    expect(container.querySelector('section[aria-label="入口"]')).toBeTruthy()
    expect(container.querySelector('section[aria-label="分流"]')).toBeTruthy()
    expect(checkboxFor(manualFinding)?.disabled).toBe(true)
    expect(checkboxFor(normalizeFinding)?.disabled).toBe(false)
    expect(container.textContent).toContain('需手动处理')
  })

  it('previews the exact fields a repair removes without writing anything', async () => {
    const { client, calls } = makeClient([normalizeFinding])
    await mount(<ConfigHealthDialog client={client} canCleanup onClose={() => {}} />)

    await click(checkboxFor(normalizeFinding))
    await click(buttonNamed('预览改动'))

    expect(container.textContent).toContain('将移除 multiplex.protocol')
    const cleanupCalls = calls.filter(call => call.path === '/config-health/cleanup')
    expect(cleanupCalls).toHaveLength(1)
    expect(cleanupCalls[0].body.confirm).toBe(false)
    // The report fingerprint rides along so the server can refuse a view whose
    // findings have changed.
    expect(cleanupCalls[0].body.fingerprint).toBe('fp-42')
  })

  it('requires a second confirmation before a destructive cleanup', async () => {
    const { client, calls } = makeClient([deleteFinding])
    await mount(<ConfigHealthDialog client={client} canCleanup onClose={() => {}} />)

    await click(checkboxFor(deleteFinding))
    await click(buttonNamed('确认删除 1 项'))

    expect(calls.filter(call => call.path === '/config-health/cleanup')).toHaveLength(0)
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('删除后无法恢复')

    await click(buttonNamed('清理选中的 1 项'))
    const cleanupCalls = calls.filter(call => call.path === '/config-health/cleanup')
    expect(cleanupCalls).toHaveLength(1)
    expect(cleanupCalls[0].body.confirm).toBe(true)
  })

  it('applies a non-destructive repair without an extra confirmation', async () => {
    const { client, calls } = makeClient([normalizeFinding])
    const onCleaned = vi.fn()
    await mount(<ConfigHealthDialog client={client} canCleanup onClose={() => {}} onCleaned={onCleaned} />)

    await click(checkboxFor(normalizeFinding))
    await click(buttonNamed('清理选中的 1 项'))

    expect(onCleaned).toHaveBeenCalled()
    const cleanupCalls = calls.filter(call => call.path === '/config-health/cleanup')
    expect(cleanupCalls[0].body.actions).toEqual([{ code: normalizeFinding.code, scope: 'inbound', resource_id: 10 }])
    expect(container.textContent).toContain('已处理 1 项，需要重新下发才会生效')
  })

  it('leaves the cleanup action closed to a non-admin operator', async () => {
    const { client } = makeClient([normalizeFinding])
    await mount(<ConfigHealthDialog client={client} canCleanup={false} onClose={() => {}} />)

    expect(checkboxFor(normalizeFinding)?.disabled).toBe(true)
    expect(buttonNamed('仅管理员可清理')?.disabled).toBe(true)
  })

  it('surfaces a changed-report conflict and re-reads the report instead of retrying', async () => {
    let attempts = 0
    const { client, calls } = makeClient([normalizeFinding], () => {
      attempts += 1
      throw new Error('配置在你查看之后发生了变化，请重新体检后再清理')
    })
    await mount(<ConfigHealthDialog client={client} canCleanup onClose={() => {}} />)

    await click(checkboxFor(normalizeFinding))
    await click(buttonNamed('清理选中的 1 项'))
    await act(async () => { await Promise.resolve() })

    expect(attempts).toBe(1)
    expect(container.textContent).toContain('配置在你查看之后发生了变化')
    expect(calls.filter(call => call.path === '/config-health').length).toBe(2)
  })

  it('states that the configuration is clean rather than showing an empty list', async () => {
    const { client } = makeClient([])
    await mount(<ConfigHealthDialog client={client} canCleanup onClose={() => {}} />)
    expect(container.textContent).toContain('入口、链路、分流与 DNS 策略均未发现不规范配置。')
  })
})
