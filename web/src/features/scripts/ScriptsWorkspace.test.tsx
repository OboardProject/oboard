// @vitest-environment jsdom

import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { ScriptsWorkspace } from './ScriptsWorkspace'

const stylesheet = readFileSync(path.resolve(__dirname, '../../style.css'), 'utf8')

async function flush() {
  await act(async () => {
    await new Promise(resolve => window.setTimeout(resolve, 0))
  })
}

describe('ScriptsWorkspace', () => {
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
  })

  it('loads scripts and opens the editor for draft, validate and grant actions', async () => {
    const requestV2 = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/scripts') return { scripts: [{ id: 3, name: '离线通知', description: '', status: 'enabled', owner_user_id: 1, created_at: '', updated_at: '' }] }
      if (path === '/script-triggers') return { triggers: [{ id: 8, script_id: 3, revision_id: 4, name: 'offline', enabled: false, kind: 'event', binding_revision: 1 }] }
      if (path === '/script-runtime/status') return { status: { enabled: false, runtime_installed: true, worker_connected: false, isolation_available: false, isolation_reason: 'bubblewrap missing' } }
      if (path.startsWith('/scripts/3/runs')) return { runs: [{ id: 11, uuid: 'srun_1', script_id: 3, revision_id: 4, status: 'succeeded', mode: 'simulate', trigger_kind: 'manual', created_at: '2026-09-07T00:00:00Z' }] }
      if (path === '/scripts/3') return { script: { id: 3, name: '离线通知', status: 'enabled' }, draft: { id: 5, source: 'function main(){return {ok:true}}', manifest: { capabilities: ['servers.status'] } }, published: { id: 4 }, revisions: [] }
      if (path === '/scripts/3/revisions') return { revision: { id: 5 } }
      if (path === '/scripts/3/validate') return { valid: true }
      if (path === '/script-grants') return { grant: { id: 9 } }
      return {}
    })
    const notify = vi.fn()
    await act(async () => {
      root.render(<ScriptsWorkspace tab="scripts" data={{ session: { role: 'admin' }, servers: [{ id: 2, name: 'node-a' }] }} client={{ requestV2 }} notify={notify} onNavigate={vi.fn()} />)
    })
    await flush()
    expect(container.textContent).toContain('离线通知')
    expect(container.textContent).toContain('已关闭')
    const open = Array.from(container.querySelectorAll('button')).find(item => item.textContent?.includes('离线通知'))
    await act(async () => { open?.click() })
    for (let i = 0; i < 15 && !document.body.textContent?.includes('发布版本'); i++) {
      await flush()
    }
    expect(document.body.textContent).toContain('发布版本')
    expect(document.body.textContent).toContain('批准当前发布版本')
    const validate = Array.from(document.body.querySelectorAll('button')).find(item => item.textContent === '校验')
    await act(async () => { validate?.click() })
    await flush()
    expect(notify).toHaveBeenCalled()
  })

  it('shows a host install command instead of enable when the runtime is missing', async () => {
    const requestV2 = vi.fn(async (path: string) => {
      if (path === '/scripts') return { scripts: [] }
      if (path === '/script-triggers') return { triggers: [] }
      if (path === '/script-runtime/status') {
        return {
          status: {
            enabled: false,
            runtime_installed: false,
            worker_connected: false,
            isolation_available: false,
            install_command: "curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env OBOARD_ACTION=enable-scripts VERSION=dev sh",
          },
        }
      }
      return {}
    })
    await act(async () => {
      root.render(<ScriptsWorkspace tab="scripts" data={{ session: { role: 'admin' }, servers: [] }} client={{ requestV2 }} notify={vi.fn()} onNavigate={vi.fn()} />)
    })
    await flush()
    expect(container.textContent).toContain('未安装')
    expect(container.textContent).toContain('安装运行环境')
    const enable = Array.from(container.querySelectorAll('button')).find(item => item.textContent === '启用执行')
    expect(enable).toBeUndefined()
    const install = Array.from(container.querySelectorAll('button')).find(item => item.textContent === '安装运行环境')
    await act(async () => { install?.click() })
    await flush()
    expect(document.body.textContent).toContain('OBOARD_ACTION=enable-scripts')
    expect(document.body.textContent).toContain('安装不会自动开启脚本')
  })

  it('renders the runtime card immediately and distinguishes the selected tab', async () => {
    let resolveRuntime: (value: unknown) => void = () => {}
    const runtimePending = new Promise(resolve => { resolveRuntime = resolve })
    const requestV2 = vi.fn(async (path: string) => {
      if (path === '/script-runtime/status') return await runtimePending
      if (path === '/scripts') return { scripts: [] }
      if (path === '/script-triggers') return { triggers: [] }
      return {}
    })
    await act(async () => {
      root.render(<ScriptsWorkspace tab="scripts" data={{ session: { role: 'admin' }, servers: [] }} client={{ requestV2 }} notify={vi.fn()} onNavigate={vi.fn()} />)
    })
    expect(container.textContent).toContain('运行环境')
    expect(container.textContent).toContain('正在检查运行环境')
    expect(container.textContent).not.toContain('未安装')
    const tabs = Array.from(container.querySelectorAll('[role="tab"]'))
    expect(tabs.map(item => item.textContent)).toEqual(['脚本库', '触发器', '执行记录'])
    expect(tabs[0].getAttribute('aria-selected')).toBe('true')
    expect(tabs[0].className).toContain('active')
    expect(tabs[1].getAttribute('aria-selected')).toBe('false')
    expect(tabs[1].className).not.toContain('active')
    expect(tabs[2].className).not.toContain('active')
    await act(async () => {
      resolveRuntime({ status: { enabled: false, runtime_installed: false, worker_connected: false, isolation_available: false } })
    })
    await flush()
    expect(container.textContent).toContain('未安装')
    expect(container.textContent).not.toContain('正在检查运行环境')
  })

  it('keeps script tabs off the global primary button fill', () => {
    expect(stylesheet).toMatch(/\.ui-tabs-list button[^}]*background:\s*transparent\s*!important/s)
    expect(stylesheet).toMatch(/\.ui-tabs-list button\.active[^}]*background:\s*var\(--primary\)\s*!important/s)
    expect(stylesheet).toMatch(/\.script-runtime-status\s*\{[^}]*background:\s*var\(--surface-solid\)/s)
  })
})
