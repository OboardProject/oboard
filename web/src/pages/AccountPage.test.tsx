// @vitest-environment jsdom

import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AccountPage } from './AccountPage'

const dummyUser = {
  id: 101,
  username: 'noxsk',
  nickname: '无聊の猫',
  role: 'admin',
  totp_enabled: false,
  subscription_age_enabled: false,
  subscription_age_public_key: '',
  subscription_age_policy: 'optional',
}

const dummyData = {
  current_user: dummyUser,
  passkeys: [],
  ssh_accesses: [
    { node_id: 'inbound:1', inbound_id: 1, name: 'Softbank', address: '1.2.3.4', port: 22, username: 'u101' },
    { node_id: 'inbound:2', inbound_id: 2, name: 'Direct', address: '5.6.7.8', port: 22, username: 'u101' },
  ],
}

function DummyPanel({ title, children }: any) {
  return (
    <div data-testid="account-panel">
      <h1>{title}</h1>
      {children}
    </div>
  )
}

function DummyDialog() {
  return null
}

describe('AccountPage component unit & interaction tests', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)

    // Mock Element.prototype.scrollIntoView
    Element.prototype.scrollIntoView = vi.fn()
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('renders user summary, profile card, security card and advanced settings card', async () => {
    const mockClient = { request: vi.fn().mockResolvedValue({ totp_enabled: false, recovery_codes_remaining: 0, passkeys: [], passkey_supported: true }) }
    const mockLoad = vi.fn().mockResolvedValue(undefined)

    await act(async () => {
      root.render(
        <AccountPage
          data={dummyData}
          client={mockClient}
          load={mockLoad}
          useDialogs={() => ({ prompt: vi.fn(), confirm: vi.fn() })}
          passkeyAvailable={() => true}
          createPasskeyCredential={vi.fn()}
          copyText={vi.fn().mockResolvedValue(true)}
          formatDate={(d) => d}
          localizeErrorMessage={(msg) => String(msg)}
          Panel={DummyPanel}
          TOTPSetupDialog={DummyDialog}
          RecoveryCodesDialog={DummyDialog}
        />
      )
    })

    expect(container.textContent).toContain('无聊の猫')
    expect(container.textContent).toContain('@noxsk')
    expect(container.textContent).toContain('管理员')
    expect(container.textContent).toContain('2FA 未开启')
    expect(container.textContent).toContain('通行密钥 0 个')
    expect(container.textContent).toContain('Age 未开启')
  })

  it('makes username readonly and disables save profile button when unchanged', async () => {
    const mockClient = { request: vi.fn().mockResolvedValue({ totp_enabled: false, recovery_codes_remaining: 0, passkeys: [], passkey_supported: true }) }
    const mockLoad = vi.fn().mockResolvedValue(undefined)

    await act(async () => {
      root.render(
        <AccountPage
          data={dummyData}
          client={mockClient}
          load={mockLoad}
          useDialogs={() => ({ prompt: vi.fn(), confirm: vi.fn() })}
          passkeyAvailable={() => true}
          createPasskeyCredential={vi.fn()}
          copyText={vi.fn().mockResolvedValue(true)}
          formatDate={(d) => d}
          localizeErrorMessage={(msg) => String(msg)}
          Panel={DummyPanel}
          TOTPSetupDialog={DummyDialog}
          RecoveryCodesDialog={DummyDialog}
        />
      )
    })

    const usernameInput = container.querySelector<HTMLInputElement>('input[value="noxsk"]')
    expect(usernameInput).not.toBeNull()
    expect(usernameInput?.hasAttribute('readonly')).toBe(true)
    expect(usernameInput?.hasAttribute('disabled')).toBe(false)

    const saveBtn = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent?.trim() === '保存'
    )
    expect(saveBtn?.disabled).toBe(true)
  })

  it('expands password form and validates password mismatch', async () => {
    const mockClient = { request: vi.fn().mockResolvedValue({ totp_enabled: false, recovery_codes_remaining: 0, passkeys: [], passkey_supported: true }) }
    const mockLoad = vi.fn().mockResolvedValue(undefined)

    await act(async () => {
      root.render(
        <AccountPage
          data={dummyData}
          client={mockClient}
          load={mockLoad}
          useDialogs={() => ({ prompt: vi.fn(), confirm: vi.fn() })}
          passkeyAvailable={() => true}
          createPasskeyCredential={vi.fn()}
          copyText={vi.fn().mockResolvedValue(true)}
          formatDate={(d) => d}
          localizeErrorMessage={(msg) => String(msg)}
          Panel={DummyPanel}
          TOTPSetupDialog={DummyDialog}
          RecoveryCodesDialog={DummyDialog}
        />
      )
    })

    // Find password modify button
    const changePassBtn = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent?.trim() === '修改密码'
    )
    expect(changePassBtn).not.toBeUndefined()

    // Click to expand
    await act(async () => {
      changePassBtn?.click()
    })

    expect(container.textContent).toContain('当前密码')
    expect(container.textContent).toContain('确认新密码')

    // Find password inputs
    const passInputs = container.querySelectorAll<HTMLInputElement>('input[type="password"]')
    expect(passInputs.length).toBe(3)
    expect(passInputs[0].getAttribute('autocomplete')).toBe('current-password')
    expect(passInputs[1].getAttribute('autocomplete')).toBe('new-password')
    expect(passInputs[2].getAttribute('autocomplete')).toBe('new-password')
  })

  it('expands Age settings and toggles textarea visibility based on switch', async () => {
    const mockClient = { request: vi.fn().mockResolvedValue({ totp_enabled: false, recovery_codes_remaining: 0, passkeys: [], passkey_supported: true }) }
    const mockLoad = vi.fn().mockResolvedValue(undefined)

    await act(async () => {
      root.render(
        <AccountPage
          data={dummyData}
          client={mockClient}
          load={mockLoad}
          useDialogs={() => ({ prompt: vi.fn(), confirm: vi.fn() })}
          passkeyAvailable={() => true}
          createPasskeyCredential={vi.fn()}
          copyText={vi.fn().mockResolvedValue(true)}
          formatDate={(d) => d}
          localizeErrorMessage={(msg) => String(msg)}
          Panel={DummyPanel}
          TOTPSetupDialog={DummyDialog}
          RecoveryCodesDialog={DummyDialog}
        />
      )
    })

    // Age row should be collapsed by default
    expect(container.querySelector('.age-public-key-textarea')).toBeNull()

    // Click Age configure button
    const configAgeBtn = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent?.trim() === '配置'
    )
    await act(async () => {
      configAgeBtn?.click()
    })

    // Now Age panel is open, but textarea is hidden because switch is OFF
    expect(container.textContent).toContain('为 Mihomo 开启 Age 加密')
    expect(container.querySelector('.age-public-key-textarea')).toBeNull()

    // Turn ON switch
    const switchInput = container.querySelector<HTMLInputElement>('input[role="switch"]')
    await act(async () => {
      switchInput?.click()
    })

    // Textarea should now be visible
    const textarea = container.querySelector<HTMLTextAreaElement>('.age-public-key-textarea')
    expect(textarea).not.toBeNull()
    expect(textarea?.placeholder).toBe('age1...')
  })
  it('copies the deployed SSH link for the selected branch and device', async () => {
    const url = 'ssh://opaque-user:current-secret@1.2.3.4:2222'
    const request = vi.fn().mockImplementation(async (path: string) =>
      path === '/node-library/share' ? { url } : { passkeys: [] })
    const copyText = vi.fn().mockResolvedValue(true)
    await act(async () => {
      root.render(<AccountPage
        data={{ ...dummyData, current_user: { ...dummyUser, proxy_password: 'old-password' },
          ssh_accesses: [{ ...dummyData.ssh_accesses[0], node_id: 'proxy_path:7', device_id: 'device-a' }] }}
        client={{ request }} load={vi.fn()} useDialogs={() => ({ prompt: vi.fn(), confirm: vi.fn() })}
        passkeyAvailable={() => true} createPasskeyCredential={vi.fn()} copyText={copyText}
        formatDate={d => d} localizeErrorMessage={String} Panel={DummyPanel}
        TOTPSetupDialog={DummyDialog} RecoveryCodesDialog={DummyDialog}
      />)
    })
    await act(async () => {
      container.querySelector<HTMLButtonElement>('#setting-row-ssh button')?.click()
    })
    await act(async () => {
      container.querySelector<HTMLButtonElement>('.copy-btn')?.click()
    })
    expect(request).toHaveBeenCalledWith('/node-library/share', {
      method: 'POST', body: JSON.stringify({ node_id: 'proxy_path:7', device_id: 'device-a' }),
    })
    expect(copyText).toHaveBeenCalledExactlyOnceWith(url)
  })

})
