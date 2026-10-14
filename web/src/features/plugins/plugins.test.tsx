// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { EnvironmentForm } from './EnvironmentForm'
import { GrantEditor } from './GrantEditor'
import { PluginSource } from './PluginSource'
import { describeError, errorIssues, fieldActive, issuesByField } from './domain'
import type { EnvField, InstallationDetail, PluginUpdateCheck, ServerOption } from './types'

beforeEach(() => vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true))
afterEach(() => vi.unstubAllGlobals())

async function render(node: React.ReactNode) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => root.render(node))
  return {
    container,
    unmount: async () => { await act(async () => root.unmount()); container.remove() },
  }
}

const fields: EnvField[] = [
  { name: 'MODE', type: 'select', label: '模式', required: true, options: [{ label: '简单', value: 'simple' }, { label: '高级', value: 'advanced' }], default: 'simple' },
  { name: 'THRESHOLD', type: 'integer', label: '阈值', min: 1, max: 10, depends_on: { field: 'MODE', equals: 'advanced' } },
  { name: 'TARGET', type: 'server', label: '目标服务器', filter: ['online'] },
  { name: 'API_TOKEN', type: 'secret', label: '接口令牌' },
]

const servers: ServerOption[] = [
  { id: '1', name: 'hk-1', enrolled: true, online: true, ipv4: true, ipv6: false },
  { id: '2', name: 'jp-2', enrolled: true, online: false, ipv4: true, ipv6: true },
]

it('evaluates depends_on against the current value or the declared default', () => {
  expect(fieldActive(fields[1], {}, fields)).toBe(false)
  expect(fieldActive(fields[1], { MODE: 'advanced' }, fields)).toBe(true)
  expect(fieldActive(fields[0], {}, fields)).toBe(true)
})

it('maps structured API errors to field issues and readable text', () => {
  const error = Object.assign(new Error('阈值超出范围'), { code: 'INVALID_ENVIRONMENT', details: { issues: [{ field: 'environment.THRESHOLD', code: 'range', message: '超出范围' }] } })
  expect(issuesByField(errorIssues(error))).toEqual({ THRESHOLD: '超出范围' })
  expect(describeError('SERVER_OFFLINE')).toBe('目标服务器离线')
  expect(describeError('UNKNOWN_CODE', 'raw')).toBe('raw')
})

it('renders only active fields, never a secret value, and honours server filters', async () => {
  const onSetSecret = vi.fn()
  const view = await render(<EnvironmentForm fields={fields} values={{}} onChange={() => undefined} custom={[]} servers={servers}
    secrets={{ API_TOKEN: { configured: true } }} onSetSecret={onSetSecret} issues={{ TARGET: '服务器不存在' }} />)
  try {
    const text = view.container.textContent || ''
    expect(text).toContain('模式')
    expect(text).not.toContain('阈值')
    expect(text).toContain('已配置，内容不可查看')
    expect(text).toContain('服务器不存在')
    expect(view.container.querySelector('input[type="password"]')).toBeNull()
    const button = Array.from(view.container.querySelectorAll('button')).find(item => item.textContent === '更换')
    await act(async () => button?.click())
    expect(onSetSecret).toHaveBeenCalledWith('API_TOKEN')
  } finally {
    await view.unmount()
  }
})

it('shows the conditional field once its parent matches', async () => {
  const view = await render(<EnvironmentForm fields={fields} values={{ MODE: 'advanced', THRESHOLD: 3 }} onChange={() => undefined} custom={[]} servers={servers} />)
  try {
    const input = view.container.querySelector('input[type="number"]') as HTMLInputElement
    expect(input.value).toBe('3')
  } finally {
    await view.unmount()
  }
})

it('keeps a defaulted numeric field empty while it is being edited', async () => {
  const onChange = vi.fn()
  const defaultedFields: EnvField[] = [{ name: 'LIMIT', type: 'number', label: '限额', default: 20 }]
  const view = await render(<EnvironmentForm fields={defaultedFields} values={{}} onChange={onChange} custom={[]} servers={servers} />)
  try {
    const input = view.container.querySelector('input[type="number"]') as HTMLInputElement
    expect(input.value).toBe('20')
    await act(async () => {
      const setNativeValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
      setNativeValue?.call(input, '')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    expect(onChange).toHaveBeenLastCalledWith({ LIMIT: undefined })
  } finally {
    await view.unmount()
  }
})

it('validates custom variable names before they reach the Controller', async () => {
  const view = await render(<EnvironmentForm fields={fields} values={{}} onChange={() => undefined} custom={[{ name: 'OBOARD_TOKEN', type: 'string' }, { name: 'MODE', type: 'string' }]}
    onCustomChange={() => undefined} customTypes={['string', 'server']} servers={servers} />)
  try {
    const text = view.container.textContent || ''
    expect(text).toContain('OBOARD_ 前缀为系统保留')
    expect(text).toContain('名称与已有变量重复')
  } finally {
    await view.unmount()
  }
})

it('grants server scope from explicit selection and warns about environment servers outside it', async () => {
  const detail = {
    permissions: [{ capability: 'network.ping', group: 'network', label: 'Ping', description: '', risk: 'medium', resource: 'server' }],
    http_hosts: [],
    forbidden: ['Shell 与命令执行'],
  } as unknown as InstallationDetail
  const onSave = vi.fn()
  const view = await render(<GrantEditor detail={detail} servers={servers} channels={[]} users={[]} plans={[]} envServerIDs={['2']} onSave={onSave} />)
  try {
    expect(view.container.textContent).toContain('Shell 与命令执行')
    expect(view.container.textContent).toContain('不在任何授权范围内')
    const toggle = view.container.querySelector('input[type="checkbox"]') as HTMLInputElement
    await act(async () => toggle.click())
    expect(view.container.textContent).not.toContain('不在任何授权范围内')
    const save = Array.from(view.container.querySelectorAll('button')).find(item => item.textContent === '保存授权') as HTMLButtonElement
    await act(async () => save.click())
    expect(onSave).toHaveBeenCalledWith({ capabilities: { 'network.ping': { servers: [2] } } })
  } finally {
    await view.unmount()
  }
})

const githubDetail = {
  id: 7, plugin_id: 'acme.demo', name: 'Demo', description: '', enabled: true, published: true, has_draft: false, version: '1.0.0', package_id: 11,
  publisher_identity: 'local', source_kind: 'github', source_repository: 'https://github.com/acme/demo', source_ref: 'stable', source_commit: 'a'.repeat(40),
  instances: [], created_at: '', updated_at: '',
} as unknown as InstallationDetail

const noDiff = { added_capabilities: [], removed_capabilities: [], added_hosts: [], removed_hosts: [], added_methods: [], added_events: [], added_secrets: [], added_environment: [], removed_environment: [], changed_environment: [], new_required: [], added_pages: [], added_actions: [], resources_expanded: false, expanded: false }
const updateCheck = (diff = noDiff): PluginUpdateCheck => ({
  status: 'update_available', repository: 'https://github.com/acme/demo', ref: 'stable', current_version: '1.0.0', current_commit: 'a'.repeat(40), latest_commit: 'b'.repeat(40), checked_at: '2026-10-14T00:00:00Z',
  preview: { plugin_id: 'acme.demo', name: 'Demo', version: '1.1.0', sha256: 'c'.repeat(64), diff, blocked: '' } as unknown as PluginUpdateCheck['preview'],
})
const findButton = (text: string) => Array.from(document.querySelectorAll('button')).find(item => item.textContent?.includes(text))

it('records the GitHub origin and updates in one click when no permission was added', async () => {
  const onApply = vi.fn().mockResolvedValue(true)
  const view = await render(<PluginSource detail={githubDetail} isAdmin busy={false} onCheck={async () => updateCheck()} onApply={onApply} />)
  try {
    const text = view.container.textContent || ''
    expect(text).toContain('acme/demo')
    expect(text).toContain('stable')
    expect(text).toContain('有新版本 1.1.0')
    await act(async () => findButton('更新到 1.1.0')!.click())
    expect(onApply).toHaveBeenCalledTimes(1)
    expect(onApply.mock.calls[0][0].latest_commit).toBe('b'.repeat(40))
    expect(document.body.textContent).not.toContain('确认更新')
  } finally { await view.unmount() }
})

it('asks for a review before an update that expands permissions', async () => {
  const onApply = vi.fn().mockResolvedValue(true)
  const view = await render(<PluginSource detail={githubDetail} isAdmin busy={false} onCheck={async () => updateCheck({ ...noDiff, added_capabilities: ['http.request'], expanded: true })} onApply={onApply} />)
  try {
    await act(async () => findButton('更新到 1.1.0')!.click())
    expect(onApply).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('新版本扩大了权限')
    await act(async () => findButton('确认更新')!.click())
    expect(onApply).toHaveBeenCalledTimes(1)
  } finally { await view.unmount() }
})

it('shows the source to non-administrators without contacting GitHub, and nothing for uploads', async () => {
  const onCheck = vi.fn()
  const viewer = await render(<PluginSource detail={githubDetail} isAdmin={false} busy={false} onCheck={onCheck} onApply={vi.fn()} />)
  try {
    expect(viewer.container.textContent).toContain('acme/demo')
    expect(onCheck).not.toHaveBeenCalled()
  } finally { await viewer.unmount() }
  const upload = await render(<PluginSource detail={{ ...githubDetail, source_kind: 'upload', source_repository: undefined }} isAdmin busy={false} onCheck={onCheck} onApply={vi.fn()} />)
  try {
    expect(upload.container.textContent).toBe('')
    expect(onCheck).not.toHaveBeenCalled()
  } finally { await upload.unmount() }
})
