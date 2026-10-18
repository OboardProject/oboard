// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { DialogContext } from '../../components/ui/dialog-context'
import { InstanceDialog } from './InstanceDialog'
import type { InstallationDetail, RequestFn } from './types'

vi.mock('../../components/ui/dialog', () => ({
  Dialog: ({ isOpen, children, footer }: any) => isOpen ? <section>{children}{footer}</section> : null,
}))

it('ties errors to validated values across edits, secret saves and rejected submissions', async () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const instance = {
    id: 1, revision: 2, name: 'ECS', status: 'configuration_required', enabled: false,
    values: {}, custom: [], secrets: {}, schedules: [], state: {},
    issues: ['REGION', 'INSTANCE'].map(field => ({ field, code: 'CONFIGURATION_REQUIRED', message: '必填项尚未填写' })),
  }
  const notify = vi.fn()
  const request = vi.fn(async (path: string, options?: RequestInit) => {
    if (path.endsWith('/environment')) throw Object.assign(new Error('配置无效'), {
      details: { issues: [{ field: 'environment.REGION', code: 'INVALID_ENVIRONMENT', message: '地域无效' }] },
    })
    if (path.includes('/secrets/')) return { configured: true }
    return instance
  })
  const installation = { permissions: [], manifest: { environment: [
    { name: 'REGION', type: 'string', label: 'ECS 地域 ID', required: true },
    { name: 'INSTANCE', type: 'string', label: 'ECS 实例 ID', required: true },
    { name: 'TOKEN', type: 'secret', label: '密钥' },
  ] } } as unknown as InstallationDetail
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  const click = async (text: string) => {
    const button = [...container.querySelectorAll('button')].find(item => item.textContent === text)!
    expect(button).toBeTruthy()
    await act(async () => button.click())
  }
  const input = (name: string) => {
    const label = [...container.querySelectorAll('label')].find(item => item.textContent?.includes(name))!
    return document.getElementById(label.htmlFor) as HTMLInputElement
  }
  const type = async (element: HTMLInputElement, value: string) => {
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(element, value)
      element.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }
  try {
    await act(async () => root.render(<DialogContext.Provider value={{ confirm: vi.fn(async () => true), alert: vi.fn(), prompt: vi.fn() }}><InstanceDialog instanceID={1} installation={installation} request={request as RequestFn}
      servers={[]} channels={[]} users={[]} plans={[]} customTypes={[]} canConfigure canAuthorize canExecute={false}
      notify={notify} onChanged={vi.fn()} onOpenRun={vi.fn()} onClose={vi.fn()} /></DialogContext.Provider>))
    expect(input('ECS 地域 ID').getAttribute('aria-invalid')).toBe('true')
    await type(input('ECS 地域 ID'), 'cn-hongkong')
    expect(input('ECS 地域 ID').hasAttribute('aria-invalid')).toBe(false)
    expect(input('ECS 地域 ID').hasAttribute('aria-describedby')).toBe(false)
    expect(input('ECS 实例 ID').getAttribute('aria-invalid')).toBe('true')
    await click('设置')
    await type(container.querySelector('input[type="password"]')!, 'test-secret')
    await click('保存')
    expect(input('ECS 地域 ID').value).toBe('cn-hongkong')
    expect(input('ECS 地域 ID').hasAttribute('aria-invalid')).toBe(false)
    expect(container.textContent).toContain('有未保存的更改')
    const footerSave = [...container.querySelectorAll('button')].filter(item => item.textContent === '保存').at(-1)!
    await act(async () => footerSave.click())
    const save = request.mock.calls.find(([path]) => path.endsWith('/environment'))!
    expect(JSON.parse(String(save[1]?.body)).values).toEqual({ REGION: 'cn-hongkong' })
    expect(input('ECS 地域 ID').getAttribute('aria-invalid')).toBe('true')
    expect(container.textContent).toContain('地域无效')
    expect(notify).toHaveBeenCalledWith('配置无效', 'error')
    expect(container.textContent).not.toContain('配置无效')
    await type(input('ECS 地域 ID'), 'cn-shanghai')
    expect(input('ECS 地域 ID').hasAttribute('aria-invalid')).toBe(false)
    expect(container.textContent).not.toContain('地域无效')
  } finally {
    await act(async () => root.unmount())
    container.remove()
    vi.unstubAllGlobals()
  }
})
