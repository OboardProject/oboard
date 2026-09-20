// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PackageInstallDialog, encodePackage, MAX_PACKAGE_BYTES } from './PackageInstallDialog'
import { PluginDetails } from './PluginDetails'
import { PluginPageRenderer } from './PluginPageRenderer'
import { PluginRunDialog } from './PluginRunDialog'
import { schemaDefaults } from './SchemaForm'
import type { Plugin, PluginPackageVersion, PluginUIDocument } from './types'

vi.mock('../../components/ui/dialog', () => ({ Dialog: ({ isOpen, title, children }: any) => isOpen ? <section role="dialog" aria-label={title}><h2>{title}</h2>{children}</section> : null }))

let container: HTMLDivElement
let root: Root
beforeEach(() => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(() => { act(() => root.unmount()); container.remove(); vi.useRealTimers() })
async function render(view: React.ReactNode) { await act(async () => { root.render(view) }) }
async function click(text: string) {
  const button = Array.from(container.querySelectorAll('button')).find(el => el.textContent === text)
  expect(button, `button ${text}`).toBeTruthy()
  await act(async () => { button!.click() })
}
async function fill(input: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
async function check(label: string) {
  const el = Array.from(container.querySelectorAll('label')).find(el => el.textContent?.trim() === label)?.querySelector('input')
  expect(el, label).toBeTruthy()
  await act(async () => { el!.click() })
}
const sha = 'a'.repeat(64)
const commit = 'b'.repeat(40)
const preview = { metadata: { plugin_id: 'demo', name: '巡检助手', version: '1.0.0', description: '节点检查' }, sha256: sha, capabilities: { added: ['servers.status'], removed: [], unchanged: [] }, has_ui: true, source: { kind: 'github', repository: 'acme/demo', commit } }
const installedPackage = { installation: { plugin_id: 3, installed: true }, version: { plugin_id: 3, revision_id: 4, sha256: sha, source_kind: 'github', source_commit: commit } }

describe('package installation', () => {
  it('previews a GitHub ref, installs only its immutable commit and displayed digest', async () => {
    const request = vi.fn().mockResolvedValueOnce(preview).mockResolvedValueOnce(installedPackage)
    const installed = vi.fn()
    await render(<PackageInstallDialog request={request} onClose={vi.fn()} onInstalled={installed} />)
    expect(Array.from(container.querySelectorAll('button')).some(button => button.textContent === '确认安装')).toBe(false)
    await fill(container.querySelector('input[type=url]')!, 'https://github.com/acme/demo')
    await fill(container.querySelectorAll('input')[1], 'release/v1')
    await click('检查安装包')
    expect(request.mock.calls[0][0]).toBe('/plugins/github/preview')
    expect(JSON.parse(request.mock.calls[0][1].body)).toEqual({ repository_url: 'https://github.com/acme/demo', ref: 'release/v1' })
    expect(container.textContent).toContain(commit)
    expect(installed).not.toHaveBeenCalled()
    await click('确认安装')
    expect(request.mock.calls[1][0]).toBe('/plugins/github/install')
    expect(JSON.parse(request.mock.calls[1][1].body)).toEqual({ repository_url: 'https://github.com/acme/demo', commit, expected_sha256: sha, confirm: true })
    expect(installed).toHaveBeenCalledOnce()
  })

  it('invalidates confirmation when the source changes and rejects unpinned previews', async () => {
    const request = vi.fn().mockResolvedValueOnce(preview).mockResolvedValueOnce({ ...preview, source: { kind: 'github', repository: 'acme/other', commit: '' } })
    await render(<PackageInstallDialog request={request} onClose={vi.fn()} onInstalled={vi.fn()} />)
    await fill(container.querySelector('input[type=url]')!, 'https://github.com/acme/demo')
    await click('检查安装包')
    expect(container.textContent).toContain('确认安装')
    await fill(container.querySelector('input[type=url]')!, 'https://github.com/acme/other')
    expect(Array.from(container.querySelectorAll('button')).some(button => button.textContent === '确认安装')).toBe(false)
    await click('检查安装包')
    expect(container.querySelector('[role=alert]')?.textContent).toContain('固定提交')
    expect(Array.from(container.querySelectorAll('button')).some(button => button.textContent === '确认安装')).toBe(false)
  })

  it('does not report success after a rejected install or accept non-GitHub addresses', async () => {
    const request = vi.fn().mockResolvedValueOnce(preview).mockRejectedValueOnce(new Error('包摘要已变化'))
    const installed = vi.fn()
    await render(<PackageInstallDialog request={request} onClose={vi.fn()} onInstalled={installed} />)
    await fill(container.querySelector('input[type=url]')!, 'https://example.com/acme/demo')
    await click('检查安装包')
    expect(request).not.toHaveBeenCalled()
    await fill(container.querySelector('input[type=url]')!, 'https://github.com/acme/demo')
    await click('检查安装包')
    await click('确认安装')
    expect(container.querySelector('[role=alert]')?.textContent).toBe('包摘要已变化')
    expect(installed).not.toHaveBeenCalled()
  })

  it.each([
    {},
    { ...installedPackage, version: { ...installedPackage.version, sha256: 'c'.repeat(64) } },
    { ...installedPackage, version: { ...installedPackage.version, source_commit: 'c'.repeat(40) } },
  ])('does not claim installation success without matching persisted evidence', async response => {
    const request = vi.fn().mockResolvedValueOnce(preview).mockResolvedValueOnce(response)
    const installed = vi.fn()
    await render(<PackageInstallDialog request={request} onClose={vi.fn()} onInstalled={installed} />)
    await fill(container.querySelector('input[type=url]')!, 'https://github.com/acme/demo')
    await click('检查安装包'); await click('确认安装')
    expect(installed).not.toHaveBeenCalled()
    expect(container.querySelector('[role=alert]')?.textContent).toContain('安装结果无法确认')
  })

  it.each([{ ...preview, sha256: '' }, { ...preview, metadata: undefined }])('rejects an incomplete preview before confirmation', async response => {
    const request = vi.fn().mockResolvedValue(response)
    await render(<PackageInstallDialog request={request} onClose={vi.fn()} onInstalled={vi.fn()} />)
    await fill(container.querySelector('input[type=url]')!, 'https://github.com/acme/demo')
    await click('检查安装包')
    expect(container.querySelector('[role=alert]')?.textContent).toContain('无法确认安装')
    expect(Array.from(container.querySelectorAll('button')).some(button => button.textContent === '确认安装')).toBe(false)
  })

  it('uses the server package_zip field and an 8 MiB bound without reading oversized files', async () => {
    const arrayBuffer = vi.fn().mockResolvedValue(Uint8Array.from([80, 75, 3, 4]).buffer)
    const file = { size: 4, arrayBuffer } as unknown as File
    expect(await encodePackage(file)).toBe('UEsDBA==')
    arrayBuffer.mockClear()
    await expect(encodePackage({ size: MAX_PACKAGE_BYTES + 1, arrayBuffer } as unknown as File)).rejects.toThrow('8 MiB')
    expect(arrayBuffer).not.toHaveBeenCalled()
    const request = vi.fn().mockResolvedValueOnce({ ...preview, source: undefined }).mockResolvedValueOnce({ ...installedPackage, version: { ...installedPackage.version, source_kind: 'upload', source_commit: '' } })
    await render(<PackageInstallDialog request={request} onClose={vi.fn()} onInstalled={vi.fn()} />)
    await click('上传 ZIP')
    const input = container.querySelector('input[type=file]')!
    Object.defineProperty(input, 'files', { value: [file] })
    await act(async () => { input.dispatchEvent(new Event('change', { bubbles: true })) })
    await click('检查安装包')
    expect(request.mock.calls[0][0]).toBe('/plugins/packages/preview')
    expect(JSON.parse(request.mock.calls[0][1].body)).toEqual({ package_zip: 'UEsDBA==' })
    await click('确认安装')
    expect(request.mock.calls[1][0]).toBe('/plugins/packages/install')
    expect(JSON.parse(request.mock.calls[1][1].body)).toEqual({ package_zip: 'UEsDBA==', expected_sha256: sha, confirm: true })
  })
})

const item: Plugin = { id: 3, name: '巡检助手', status: 'disabled', description: '', owner_user_id: 1, created_at: '', updated_at: 'now' }
const version: PluginPackageVersion = { plugin_id: 3, revision_id: 4, version: '1.0.0', sha256: sha, manifest: { capabilities: ['servers.status', 'network.request'], config_schema: { type: 'object', required: ['enabled'], properties: { enabled: { type: 'boolean' }, label: { type: 'string', default: '巡检' } } }, network: { allowed_origins: ['https://notify.example'], allowed_methods: ['POST'], max_response_bytes: 1024 }, secrets: [{ name: 'notify_token', purpose: '通知凭据' }] }, ui: null, source_kind: 'upload', source_repository: '', source_commit: '', created_at: '' }
function detailRequest() {
  return vi.fn(async (path: string, init?: RequestInit): Promise<any> => {
    if (init) return {}
    if (path === '/plugins/3') return { plugin: item, published: { id: 4 }, revisions: [] }
    if (path === '/plugins/3/versions') return { versions: [version, { ...version, revision_id: 5, version: '2.0.0' }] }
    if (path === '/plugins/3/config') return { installation: { plugin_id: 3, active_revision_id: 4, installed: true, config: { enabled: true, label: '保存的配置' } } }
    if (path === '/plugin-triggers?plugin_id=3') return { triggers: [] }
    if (path === '/plugins/3/runs?limit=50') return { runs: [] }
    if (path === '/plugin-grants?plugin_id=3') return { grants: [{ id: 8, revision_id: 4, capabilities: ['servers.status'] }] }
    throw new Error(`unexpected request ${path}`)
  })
}
async function renderDetails(request: ReturnType<typeof detailRequest>, isAdmin = true) {
  await render(<PluginDetails plugin={item} request={request} isAdmin={isAdmin} servers={[{ id: 2, name: 'node-a' }]} onClose={vi.fn()} onChanged={vi.fn()} onDevelop={vi.fn()} />)
}

describe('plugin configuration and grants', () => {
  it('loads configuration separately, edits in a dialog, and saves typed values', async () => {
    const request = detailRequest()
    await renderDetails(request)
    await click('配置插件')
    expect(container.querySelector<HTMLInputElement>('#config-label')?.value).toBe('保存的配置')
    await fill(container.querySelector('#config-label')!, '新配置')
    await click('保存配置')
    const call = request.mock.calls.find(([path, init]) => path === '/plugins/3/config' && init?.method === 'PUT')!
    expect(JSON.parse(call[1]!.body as string)).toEqual({ config: { enabled: true, label: '新配置' } })
    expect(schemaDefaults(version.manifest.config_schema!)).toEqual({ enabled: false, label: '巡检' })
  })

  it('starts with no grants selected and sends only reviewed server/network/secret scope', async () => {
    const request = detailRequest()
    await renderDetails(request)
    await click('权限')
    await click('授予当前版本权限')
    expect(Array.from(container.querySelectorAll<HTMLInputElement>('input[type=checkbox]')).every(input => !input.checked)).toBe(true)
    await check('network.request')
    const approve = () => Array.from(container.querySelectorAll('button')).find(button => button.textContent === '确认授权')!
    expect(approve().disabled).toBe(true)
    await check('https://notify.example'); await check('POST'); await check('node-a')
    await check('密钥：通知凭据 (notify_token)')
    await click('确认授权')
    const call = request.mock.calls.find(([path, init]) => path === '/plugin-grants' && init?.method === 'POST')!
    expect(JSON.parse(call[1]!.body as string)).toEqual({ plugin_id: 3, revision_id: 4, capabilities: ['network.request'], resource_scope: { servers: { mode: 'selected', ids: [2] } }, constraints: { network: { allowed_origins: ['https://notify.example'], allowed_methods: ['POST'], max_response_bytes: 1024 }, secrets: ['notify_token'] } })
  })

  it('writes declared secrets without fetching plaintext, and hides secret/grant actions from operators', async () => {
    const request = detailRequest()
    await renderDetails(request)
    await click('管理密钥')
    const secret = container.querySelector<HTMLInputElement>('input[type=password]')!
    expect(secret.value).toBe('')
    await fill(secret, 'new-private-value')
    await click('保存密钥')
    const secretCalls = request.mock.calls.filter(([path]) => path.includes('/secrets/'))
    expect(secretCalls).toHaveLength(1)
    expect(secretCalls[0][0]).toBe('/plugins/3/secrets/notify_token')
    expect(secretCalls[0][1]?.method).toBe('PUT')
    expect(container.textContent).not.toContain('new-private-value')
    await renderDetails(request, false)
    expect(container.textContent).not.toContain('管理密钥')
    await click('权限')
    expect(container.textContent).not.toContain('授予当前版本权限')
  })

  it('deletes secrets through the write-only PUT contract', async () => {
    const request = detailRequest()
    await renderDetails(request)
    await click('管理密钥'); await click('删除该名称密钥')
    const call = request.mock.calls.find(([path]) => path === '/plugins/3/secrets/notify_token')!
    expect(call[1]?.method).toBe('PUT')
    expect(JSON.parse(call[1]!.body as string)).toEqual({ value: '' })
  })

  it('runs the installed page revision instead of the latest published package', async () => {
    const base = detailRequest()
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/plugins/3') return { plugin: { ...item, status: 'enabled' }, published: { id: 5 }, revisions: [] }
      if (path === '/plugins/3/versions') return { versions: [{ ...version, revision_id: 5 }, { ...version, ui: documentUI }] }
      if (path === '/plugins/3/runs' && init?.method === 'POST') return { run: { id: 9, status: 'failed', error_code: 'permission_denied' } }
      return base(path, init)
    })
    await renderDetails(request)
    await click('插件页面'); await click('查询')
    const call = request.mock.calls.find(([path]) => path === '/plugins/3/runs')!
    expect(JSON.parse(call[1]!.body as string).revision_id).toBe(4)
    expect(container.querySelector('[role=status]')?.textContent).toContain('失败')
    expect(container.querySelector('[role=status]')?.textContent).not.toContain('已完成')
  })

  it('confirms version activation and uses keep_state when uninstalling', async () => {
    const request = detailRequest()
    await renderDetails(request)
    await click('版本')
    const activate = Array.from(container.querySelectorAll('button')).find(button => button.textContent === '激活版本' && !button.disabled)!
    await act(async () => { activate.click() })
    expect(request.mock.calls.some(([path]) => path.includes('/activate'))).toBe(false)
    await click('确认激活')
    expect(request.mock.calls.find(([path]) => path.endsWith('/activate'))![0]).toBe('/plugins/3/versions/activate')
    expect(JSON.parse(request.mock.calls.find(([path]) => path.endsWith('/activate'))![1]!.body as string)).toEqual({ revision_id: 5, confirm: true })
    await click('概览'); await click('卸载'); await click('确认卸载')
    expect(JSON.parse(request.mock.calls.find(([path]) => path.endsWith('/uninstall'))![1]!.body as string)).toEqual({ keep_state: true, confirm: true })
  })
})

const documentUI: PluginUIDocument = { pages: [{ id: 'status', title: '巡检', components: [
  { id: 'intro', type: 'text', text: '<img src=x onerror=alert(1)>' },
  { id: 'count', type: 'stat', title: '节点总数', text: '2' },
  { id: 'nodes', type: 'table', columns: [{ key: 'name', label: '名称' }], action: { label: '查询', params: { action: 'list' } } },
  { id: 'edit', type: 'form', title: '巡检参数', fields: [{ name: 'count', label: '次数', type: 'number', required: true }, { name: 'verbose', label: '详细模式', type: 'boolean', required: true }, { name: 'mode', label: '检查方式', type: 'select', options: ['tcp', 'http'] }], action: { label: '提交检查', params: { action: 'inspect' } } },
] }] }

describe('declarative plugin pages', () => {
  it('continues tracking accepted node actions after the JavaScript run finishes', async () => {
    vi.useFakeTimers()
    let actionStatus = 'accepted'
    const request = vi.fn(async (path: string) => {
      if (path === '/plugin-runs/9') return { run: { id: 9, revision_id: 4, status: 'succeeded' } }
      if (path.endsWith('/logs')) return { logs: [] }
      if (path.endsWith('/actions')) return { actions: [{ id: 12, kind: 'diagnose', status: actionStatus }] }
      throw new Error(`unexpected request ${path}`)
    })
    await render(<PluginRunDialog id={9} request={request} onClose={vi.fn()} />)
    expect(container.textContent).toContain('已受理')
    actionStatus = 'failed'
    await act(async () => { await vi.advanceTimersByTimeAsync(2000) })
    expect(container.textContent).toContain('#12 · diagnose · 失败')
    expect(request.mock.calls.filter(([path]) => path.endsWith('/actions'))).toHaveLength(2)
    await act(async () => { await vi.advanceTimersByTimeAsync(4000) })
    expect(request.mock.calls.filter(([path]) => path.endsWith('/actions'))).toHaveLength(2)
  })

  it('renders host components as escaped text and waits for terminal results', async () => {
    vi.useFakeTimers()
    const request = vi.fn().mockResolvedValueOnce({ run: { id: 9, status: 'queued' } }).mockResolvedValueOnce({ run: { id: 9, status: 'succeeded', result: { rows: [{ name: '<script>unsafe</script>' }] } } })
    await render(<PluginPageRenderer document={documentUI} pluginID={3} revisionID={4} request={request} />)
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('script')).toBeNull()
    expect(container.textContent).toContain('<img src=x')
    await click('查询')
    expect(container.querySelector('[role=status]')?.textContent).toContain('排队中')
    expect(container.textContent).not.toContain('已完成')
    expect(JSON.parse(request.mock.calls[0][1].body)).toMatchObject({ revision_id: 4, params: { action: 'list' } })
    await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
    expect(container.querySelector('[role=status]')?.textContent).toContain('已完成')
    expect(container.querySelector('td')?.textContent).toBe('<script>unsafe</script>')
    expect(container.querySelector('script')).toBeNull()
  })

  it('uses a modal for forms, preserves false booleans and surfaces execution denial', async () => {
    const request = vi.fn().mockRejectedValue(new Error('当前用户无权执行'))
    await render(<PluginPageRenderer document={documentUI} pluginID={3} revisionID={4} request={request} />)
    expect(container.querySelector('form')).toBeNull()
    await click('提交检查')
    await fill(container.querySelector('input[type=number]')!, '3')
    await act(async () => { container.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
    expect(JSON.parse(request.mock.calls[0][1].body)).toMatchObject({ revision_id: 4, params: { count: 3, verbose: false, action: 'inspect' }, env: {} })
    expect(container.textContent).toContain('当前用户无权执行')
    expect(container.querySelector('[role=status]')).toBeNull()
  })

  it('does not allow an unresolved revision to fall back to the latest version', async () => {
    const request = vi.fn()
    await render(<PluginPageRenderer document={documentUI} pluginID={3} revisionID={0} request={request} />)
    await click('查询')
    expect(request).not.toHaveBeenCalled()
  })

  it('clears stale results and success when the next execution is denied', async () => {
    const request = vi.fn().mockResolvedValueOnce({ run: { id: 9, status: 'succeeded', result: { rows: [{ name: 'old-result' }] } } }).mockRejectedValueOnce(new Error('授权已撤销'))
    await render(<PluginPageRenderer document={documentUI} pluginID={3} revisionID={4} request={request} />)
    await click('查询')
    expect(container.querySelector('td')?.textContent).toBe('old-result')
    await click('查询')
    expect(container.querySelector('td')).toBeNull()
    expect(container.querySelector('[role=status]')).toBeNull()
    expect(container.querySelector('[role=alert]')?.textContent).toBe('授权已撤销')
  })

  it('shows polling failure without claiming completion and links to the real run', async () => {
    vi.useFakeTimers()
    const request = vi.fn().mockResolvedValueOnce({ run: { id: 9, status: 'queued' } }).mockRejectedValueOnce(new Error('读取执行结果失败'))
    const onViewRun = vi.fn()
    await render(<PluginPageRenderer document={documentUI} pluginID={3} revisionID={4} request={request} onViewRun={onViewRun} />)
    await click('查询')
    await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
    expect(container.querySelector('[role=alert]')?.textContent).toBe('读取执行结果失败')
    expect(container.querySelector('[role=status]')?.textContent).not.toContain('已完成')
    await click('查看执行记录')
    expect(onViewRun).toHaveBeenCalledWith(9)
  })

  it('prevents actions for disabled plugins and bounds table output', async () => {
    const request = vi.fn().mockResolvedValue({ run: { id: 9, status: 'succeeded', result: Array.from({ length: 201 }, (_, i) => ({ name: `node-${i}` })) } })
    await render(<PluginPageRenderer document={documentUI} pluginID={3} revisionID={4} request={request} disabled />)
    await click('查询')
    expect(request).not.toHaveBeenCalled()
    await render(<PluginPageRenderer document={documentUI} pluginID={3} revisionID={4} request={request} />)
    await click('查询')
    expect(container.querySelectorAll('td')).toHaveLength(200)
    expect(container.textContent).toContain('仅展示前 200 行')
  })
})
