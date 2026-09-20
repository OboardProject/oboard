// @vitest-environment jsdom

import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { PluginsWorkspace } from './PluginsWorkspace'

const stylesheet = readFileSync(path.resolve(__dirname, '../../style.css'), 'utf8')

async function flush() {
  await act(async () => {
    await new Promise(resolve => window.setTimeout(resolve, 0))
  })
}

describe('PluginsWorkspace', () => {
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

  it('loads plugin details and explicitly opens development tools for validation', async () => {
    const requestV2 = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/plugins') return { plugins: [{ id: 3, name: '离线通知', description: '', status: 'enabled', owner_user_id: 1, created_at: '', updated_at: '' }] }
      if (path === '/plugin-triggers') return { triggers: [{ id: 8, plugin_id: 3, revision_id: 4, name: 'offline', enabled: false, kind: 'event', binding_revision: 1 }] }
      if (path === '/plugin-runtime/status') return { status: { enabled: false, runtime_installed: true, worker_connected: false, isolation_available: false, isolation_reason: 'bubblewrap missing' } }
      if (path.startsWith('/plugins/3/runs')) return { runs: [{ id: 11, uuid: 'srun_1', plugin_id: 3, revision_id: 4, status: 'succeeded', mode: 'simulate', trigger_kind: 'manual', created_at: '2026-09-07T00:00:00Z' }] }
      if (path === '/plugins/3') return { plugin: { id: 3, name: '离线通知', status: 'enabled' }, draft: { id: 5, source: 'function main(){return {ok:true}}', manifest: { capabilities: ['servers.status'] } }, published: { id: 4 }, revisions: [] }
      if (path === '/plugins/3/versions') return { versions: [] }
      if (path === '/plugin-triggers?plugin_id=3') return { triggers: [] }
      if (path === '/plugin-grants?plugin_id=3') return { grants: [] }
      if (path === '/plugins/3/revisions') return { revision: { id: 5 } }
      if (path === '/plugins/3/validate') return { valid: true }
      if (path === '/plugin-grants') return { grant: { id: 9 } }
      return {}
    })
    const notify = vi.fn()
    await act(async () => {
      root.render(<PluginsWorkspace tab="plugins" data={{ session: { role: 'admin' }, servers: [{ id: 2, name: 'node-a' }] }} client={{ requestV2 }} notify={notify} onNavigate={vi.fn()} />)
    })
    await flush()
    expect(container.textContent).toContain('离线通知')
    expect(container.textContent).toContain('已关闭')
    const open = Array.from(container.querySelectorAll('button')).find(item => item.textContent?.includes('离线通知'))
    await act(async () => { open?.click() })
    await flush()
    expect(document.body.textContent).toContain('当前版本')
    const develop = Array.from(document.body.querySelectorAll('button')).find(item => item.textContent === '开发')
    await act(async () => { develop?.click() })
    await flush()
    const editor = Array.from(document.body.querySelectorAll('button')).find(item => item.textContent === '打开源码编辑器')
    await act(async () => { editor?.click() })
    await flush()
    expect(document.body.textContent).toContain('发布版本')
    expect(document.body.textContent).toContain('由管理员审核授权')
    const validate = Array.from(document.body.querySelectorAll('button')).find(item => item.textContent === '校验')
    await act(async () => { validate?.click() })
    await flush()
    expect(notify).toHaveBeenCalled()
  })

  it('shows a host install command instead of enable when the runtime is missing', async () => {
    const requestV2 = vi.fn(async (path: string) => {
      if (path === '/plugins') return { plugins: [] }
      if (path === '/plugin-triggers') return { triggers: [] }
      if (path === '/plugin-runtime/status') {
        return {
          status: {
            enabled: false,
            runtime_installed: false,
            worker_connected: false,
            isolation_available: false,
            install_command: "curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/plugins/install.sh | sudo env OBOARD_ACTION=enable-plugins VERSION=dev sh",
          },
        }
      }
      return {}
    })
    await act(async () => {
      root.render(<PluginsWorkspace tab="plugins" data={{ session: { role: 'admin' }, servers: [] }} client={{ requestV2 }} notify={vi.fn()} onNavigate={vi.fn()} />)
    })
    await flush()
    expect(container.textContent).toContain('未安装')
    expect(container.textContent).toContain('安装运行环境')
    const enable = Array.from(container.querySelectorAll('button')).find(item => item.textContent === '启用执行')
    expect(enable).toBeUndefined()
    const install = Array.from(container.querySelectorAll('button')).find(item => item.textContent === '安装运行环境')
    await act(async () => { install?.click() })
    await flush()
    expect(document.body.textContent).toContain('OBOARD_ACTION=enable-plugins')
    expect(document.body.textContent).toContain('安装不会自动开启插件')
  })

  it('renders the runtime card immediately and distinguishes the selected tab', async () => {
    let resolveRuntime: (value: unknown) => void = () => {}
    const runtimePending = new Promise(resolve => { resolveRuntime = resolve })
    const requestV2 = vi.fn(async (path: string) => {
      if (path === '/plugin-runtime/status') return await runtimePending
      if (path === '/plugins') return { plugins: [] }
      if (path === '/plugin-triggers') return { triggers: [] }
      return {}
    })
    await act(async () => {
      root.render(<PluginsWorkspace tab="plugins" data={{ session: { role: 'admin' }, servers: [] }} client={{ requestV2 }} notify={vi.fn()} onNavigate={vi.fn()} />)
    })
    expect(container.textContent).toContain('运行环境')
    expect(container.textContent).toContain('正在检查运行环境')
    expect(container.textContent).not.toContain('未安装')
    const tabs = Array.from(container.querySelectorAll('[role="tab"]'))
    expect(tabs.map(item => item.textContent)).toEqual(['插件库', '触发器', '执行记录'])
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

  it('keeps plugin tabs off the global primary button fill', () => {
    expect(stylesheet).toMatch(/\.ui-tabs-list button[^}]*background:\s*transparent\s*;/s)
    expect(stylesheet).toMatch(/\.ui-tabs-list button\.active[^}]*background:\s*var\(--surface-solid\)\s*;/s)
    expect(stylesheet).toMatch(/\.plugin-runtime-status\s*\{[^}]*background:\s*var\(--surface-solid\)/s)
  })
})
