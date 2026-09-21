// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { StealthTransportSettings } from './StealthTransportSettings'

function mount(ui: React.ReactElement) {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  return {
    host, root,
    async render() { await act(async () => root.render(ui)) },
    done() {
      act(() => root.unmount())
      host.remove()
      ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
    },
  }
}

it('saves the trimmed stealth transport config', async () => {
  const onSave = vi.fn(async () => {})
  const view = mount(<StealthTransportSettings
    value={{ enabled: false, listen_address: '0.0.0.0:24443', public_address: '', active: false, source: 'settings' }}
    onSave={onSave}
  />)
  try {
    await view.render()
    const switchInput = view.host.querySelector('[role="switch"]') as HTMLInputElement
    await act(async () => switchInput.click())
    const inputs = [...view.host.querySelectorAll('input')] as HTMLInputElement[]
    const listen = inputs.find(input => input.getAttribute('aria-label') === '安全进程监听地址')!
    const publicAddress = inputs.find(input => input.getAttribute('aria-label') === '安全进程 Agent 连接地址')!
    const setValue = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!
    await act(async () => {
      setValue.call(listen, ' 0.0.0.0:24443 ')
      listen.dispatchEvent(new Event('input', { bubbles: true }))
      setValue.call(publicAddress, ' agent.example.com:24443 ')
      publicAddress.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => ([...view.host.querySelectorAll('button')] as HTMLButtonElement[])
      .find(button => button.type === 'submit')!.click())
    expect(onSave).toHaveBeenCalledWith({ enabled: true, listen_address: '0.0.0.0:24443', public_address: 'agent.example.com:24443' })
    expect(view.host.querySelector('[role="status"]')?.textContent).toContain('已保存')
  } finally {
    view.done()
  }
})

it('surfaces save failures and transport status without losing the draft', async () => {
  const onSave = vi.fn(async () => { throw new Error('无法监听安全传输端口：地址已被占用') })
  const view = mount(<StealthTransportSettings
    value={{ enabled: true, listen_address: '0.0.0.0:24443', public_address: 'agent.example.com:24443', active: true, error: '', source: 'settings' }}
    onSave={onSave}
  />)
  try {
    await view.render()
    expect(view.host.textContent).toContain('监听中')
    await act(async () => ([...view.host.querySelectorAll('button')] as HTMLButtonElement[])
      .find(button => button.type === 'submit')!.click())
    expect(view.host.querySelector('[role="alert"]')?.textContent).toContain('无法监听安全传输端口')
    const listen = ([...view.host.querySelectorAll('input')] as HTMLInputElement[])
      .find(input => input.getAttribute('aria-label') === '安全进程监听地址')!
    expect(listen.value).toBe('0.0.0.0:24443')
    expect(listen.disabled).toBe(false)
  } finally {
    view.done()
  }
})

it('marks a configured but inactive transport and explains the environment source', async () => {
  const view = mount(<StealthTransportSettings
    value={{ enabled: true, listen_address: '0.0.0.0:24443', public_address: 'agent.example.com:24443', active: false, error: '安全传输运行环境尚未初始化', source: 'environment' }}
    onSave={vi.fn(async () => {})}
  />)
  try {
    await view.render()
    expect(view.host.textContent).toContain('未生效')
    expect(view.host.textContent).toContain('环境变量')
    expect(view.host.querySelector('[role="alert"]')?.textContent).toContain('安全传输运行环境尚未初始化')
  } finally {
    view.done()
  }
})

it('renders as a collapsible disclosure with question-mark help buttons', async () => {
  const view = mount(<StealthTransportSettings
    value={{ enabled: false, listen_address: '0.0.0.0:24443', public_address: '', active: false }}
    onSave={vi.fn(async () => {})}
  />)
  try {
    await view.render()
    const disclosure = view.host.querySelector('details.settings-disclosure')
    expect(disclosure).not.toBeNull()
    const helpButtons = view.host.querySelectorAll('.form-field-help')
    expect(helpButtons.length).toBeGreaterThanOrEqual(3)
  } finally {
    view.done()
  }
})

