// @vitest-environment jsdom
import React, { act, useEffect, useRef, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import ts from 'typescript'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import * as update from '../../controller-update'
import * as diagnostics from '../../controller-update-diagnostics'
import * as presentation from './update-presentation'

const source = readFileSync(resolve(__dirname, '../../main.tsx'), 'utf8')
const component = source.slice(source.indexOf('function ControllerUpdatePanel('), source.indexOf('type ControllerUpdateInstallPhase ='))
const compiled = ts.transpileModule(component, { compilerOptions: { jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ES2022 } }).outputText
const Icon = () => null
const dependencies = {
  React, useEffect, useRef, useState, ...update, ...diagnostics, ...presentation,
  usePausedInterval: () => {}, localizeErrorMessage: (value: string) => value,
  formatDate: (value: string) => value,
  AnimatePresence: ({ children }: any) => children,
  Dialog: ({ isOpen, children, title }: any) => isOpen ? <div role="dialog" aria-label={title}>{children}</div> : null,
  ControllerUpdateInstallDialog: ({ phase }: any) => <div role="dialog">{phase}</div>,
  CopyBlock: ({ value }: any) => <div className="copy-block"><code>{value}</code><button className="copy-block-button">复制</button></div>,
  FormField: ({ children }: any) => <div>{children}</div>, Select: 'select', Switch: Icon,
  FileText: Icon, Sliders: Icon, RefreshCw: Icon, X: Icon, Download: Icon,
}
const Panel = new Function(...Object.keys(dependencies), compiled + ';return ControllerUpdatePanel')(...Object.values(dependencies))
let root: Root
let container: HTMLDivElement
let snapshot: any
let fleet: any
let props: any
const failure = (id: number) => ({ server_id: id, server_name: 'Icelan-' + id, attempts: 0, max_attempts: 3, auto_stopped: false, last_error: 'agentlink auth rejected: invalid agent credentials' })
beforeEach(() => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  snapshot = { channel: 'dev', status: 'current', current: { version: 'dev-def7adc6f630', build: '20260930172314' }, available: { version: 'dev-def7adc6f630', build: '20260930172314' }, update_available: false }
  fleet = { target_build: '20260929223929', enrolled: 34, current: 32, running: 0, pending: 0, offline: 2, failure_count: 1, failed_servers: [failure(1)], rolling: false, paused: false }
  props = {
    data: { settings: {}, version: { agent_expected_version: 'dev-eb9e4e181bec' } },
    client: { request: vi.fn(async (path: string) => path === '/agent-updates/status' ? fleet : path.startsWith('/servers/') ? { existing: false } : snapshot) },
    realtimeStatus: 'open', realtimeRevision: 0, realtimeResources: ['controller_update', 'agent_updates'],
    notify: vi.fn(), dialogs: { confirm: vi.fn(async () => true) },
  }
})
afterEach(() => { act(() => root.unmount()); container.remove(); delete (globalThis as any).IS_REACT_ACT_ENVIRONMENT })
async function render() { await act(async () => root.render(<Panel {...props} />)) }
async function click(text: string) {
  const button = [...container.querySelectorAll('button')].find(item => item.textContent === text)!
  await act(async () => button.click())
}
it('shows one current version, aligned metadata, and an expanded single failure', async () => {
  await render()
  expect(container.querySelectorAll('.controller-update-versions strong')).toHaveLength(1)
  expect(container.textContent).toContain('已是最新版本')
  expect([...container.querySelectorAll('dt')].map(item => item.textContent)).toEqual(['更新通道', '配套 Agent', '最后检查'])
  expect(container.querySelector('.agent-update-error-item')?.textContent).toContain('Agent 凭证无效或已失效')
  expect(container.querySelector('details')).toBeNull()
  expect(container.textContent).not.toContain('安装更新')
  expect(container.textContent).not.toContain('暂停全部')
  await click('详情')
  expect(container.querySelector('[role="dialog"]')?.textContent).toContain(failure(1).last_error)
})
it('shows a forward comparison and installation only when an update is available', async () => {
  snapshot = { ...snapshot, status: 'available', update_available: true, available: { version: 'dev-a32fd912', build: '20261001003452' } }
  await render()
  expect(container.querySelectorAll('.controller-update-versions strong')).toHaveLength(2)
  expect(container.querySelector('.controller-update-version-arrow')?.textContent).toBe('→')
  expect(container.textContent).toContain('安装更新')
  expect(container.textContent).toContain('发现新版本')
})
it('keeps same-tag newer builds visible', async () => {
  snapshot = { ...snapshot, status: 'available', update_available: true, available: { ...snapshot.current, build: '20261001003452' } }
  await render()
  expect(container.querySelectorAll('.controller-update-versions strong')).toHaveLength(2)
})
it('disables checking while a check is pending', async () => {
  await render()
  let finish: (value: any) => void = () => {}
  props.client.request.mockImplementation((path: string) => path.endsWith('/check') ? new Promise(resolve => { finish = resolve }) : Promise.resolve(fleet))
  await click('检查更新')
  const button = [...container.querySelectorAll('button')].find(item => item.textContent === '正在检查')!
  expect(button.disabled).toBe(true)
  expect(button.getAttribute('aria-busy')).toBe('true')
  expect(container.querySelectorAll('.controller-update-versions strong')).toHaveLength(1)
  await act(async () => finish(snapshot))
})
it('shows updating status and a progress entry instead of another install action', async () => {
  snapshot = { ...snapshot, status: 'installing', update_available: true }
  await render()
  expect(container.querySelector('.controller-update-head')?.textContent).toContain('正在更新')
  expect(container.textContent).toContain('查看安装进度')
  expect([...container.querySelectorAll('button')].some(button => button.textContent === '安装更新')).toBe(false)
})
it('retries only the selected server through the existing API', async () => {
  await render()
  await click('重试')
  expect(props.client.request).toHaveBeenCalledWith('/servers/1/agent-update', { method: 'POST', body: '{}' })
  expect(props.client.request).not.toHaveBeenCalledWith('/agents/update-all', expect.anything())
})
it('limits long failure lists and exposes the rest on demand', async () => {
  fleet = { ...fleet, failure_count: 12, failed_servers: Array.from({ length: 12 }, (_, index) => failure(index + 1)) }
  await render()
  expect(container.querySelectorAll('.agent-update-error-item')).toHaveLength(3)
  await click('查看全部 12 个异常')
  expect(container.querySelectorAll('.agent-update-error-item')).toHaveLength(12)
  await click('收起异常')
  expect(container.querySelectorAll('.agent-update-error-item')).toHaveLength(3)
})
it('hides the exception area on success, separates offline counts, and retains pause/resume controls', async () => {
  fleet = { ...fleet, failure_count: 0, failed_servers: [] }
  await render()
  expect(container.querySelector('.agent-update-errors')).toBeNull()
  expect(container.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('94')
  expect(container.querySelector('.segmented-progress-legend')?.textContent).toContain('离线2失败0')
  fleet = { ...fleet, running: 1, rolling: true }
  props.realtimeRevision++
  await render()
  expect(container.textContent).toContain('暂停全部')
  fleet = { ...fleet, paused: true }
  props.realtimeRevision++
  await render()
  expect(container.textContent).toContain('恢复全部')
  fleet = { ...fleet, paused: false, rolling: false, running: 0, current: 34, offline: 0 }
  props.realtimeRevision++
  await render()
  expect(container.textContent).toContain('全部完成')
  expect(container.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('100')
  fleet = { ...fleet, enrolled: 0, current: 0 }
  props.realtimeRevision++
  await render()
  expect(container.textContent).toContain('暂无已接入 Agent')
  expect(container.querySelector('[role="progressbar"]')?.getAttribute('aria-valuenow')).toBe('0')
})
it('maps known errors and preserves raw details including unknown errors', () => {
  const cases = [['invalid agent credentials', '认证失败'], ['Agent offline', '连接中断'], ['context deadline exceeded', '更新超时'], ['signature verification failed', '校验失败'], ['no space left on device', '空间不足'], ['dial tcp: connection refused', '网络连接失败'], ['unexpected EOF', '更新失败']]
  for (const [raw, title] of cases) expect(presentation.formatAgentUpdateError(raw)).toMatchObject({ raw, title })
})
