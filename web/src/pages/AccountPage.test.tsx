// @vitest-environment jsdom

import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AccountPage, type AccountPageProps } from './AccountPage'

const user = { id: 101, username: 'account-user', nickname: '测试账户', role: 'admin', totp_enabled: false, subscription_age_enabled: false, subscription_age_public_key: '', subscription_age_policy: 'optional' }
const authentication = { totp_enabled: false, recovery_codes_remaining: 0, passkeys: [], passkey_supported: true }
const sshAccess = { node_id: 'proxy_path:7', device_id: 'device-a', inbound_id: 1, name: '东京入口', address: '1.2.3.4', port: 2222, username: 'opaque-user' }

function DummyDialog() { return null }
function button(text: string, scope: ParentNode = document.body) {
  const result = Array.from(scope.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.trim() === text)
  expect(result, `button ${text}`).toBeDefined()
  return result!
}
async function click(target: HTMLElement) { await act(async () => { target.click() }) }
async function input(target: HTMLInputElement | HTMLTextAreaElement, value: string) {
  await act(async () => {
    const prototype = target instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
    Object.getOwnPropertyDescriptor(prototype, 'value')!.set!.call(target, value)
    target.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
async function submit(id: string) {
  await act(async () => { document.getElementById(id)!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
}

describe('AccountPage', () => {
  let container: HTMLDivElement
  let root: Root
  let props: AccountPageProps
  let request: ReturnType<typeof vi.fn>
  let prompt: ReturnType<typeof vi.fn>
  let confirm: ReturnType<typeof vi.fn>

  beforeEach(() => {
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    Element.prototype.scrollIntoView = vi.fn()
    request = vi.fn().mockResolvedValue(authentication)
    prompt = vi.fn().mockResolvedValue(null)
    confirm = vi.fn().mockResolvedValue(false)
    props = {
      data: { current_user: user, passkeys: [], ssh_accesses: [sshAccess] }, client: { request }, load: vi.fn().mockResolvedValue(undefined), notify: vi.fn(),
      useDialogs: () => ({ prompt, confirm }), passkeyAvailable: () => true, createPasskeyCredential: vi.fn().mockResolvedValue({ id: 'credential' }),
      copyText: vi.fn().mockResolvedValue(true), formatDate: d => d, localizeErrorMessage: String,
      Panel: ({ children }) => <div>{children}</div>, TOTPSetupDialog: DummyDialog, RecoveryCodesDialog: DummyDialog,
    }
  })
  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    sessionStorage.clear()
    vi.restoreAllMocks()
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
  })
  async function render(overrides: Partial<AccountPageProps> = {}) {
    await act(async () => { root.render(<AccountPage {...props} {...overrides} />) })
  }

  it('shows compact identity and read-only groups without welcome copy or inline forms', async () => {
    await render()
    expect(container.textContent).toContain('测试账户@account-user管理员')
    expect(Array.from(container.querySelectorAll('h2')).map(h => h.textContent)).toEqual(['个人资料', '登录与安全', '高级订阅设置'])
    expect(container.querySelector('form')).toBeNull()
    expect(container.textContent).not.toContain('欢迎')
    expect(container.textContent).toContain('2FA 未开启')
    await click(button('通行密钥 0 个'))
    expect(container.querySelector('#setting-row-passkeys-content')).not.toBeNull()
    expect(document.activeElement?.id).toBe('setting-row-passkeys')
    expect(container.querySelector('#setting-row-passkeys button')?.getAttribute('aria-expanded')).toBe('true')
  })

  it('edits profile in a dialog, keeps username read-only and sends the existing PATCH', async () => {
    await render()
    await click(button('编辑资料'))
    const form = document.getElementById('account-profile-form')!
    const fields = form.querySelectorAll('input')
    expect(fields[0].readOnly).toBe(true)
    expect(fields[0].disabled).toBe(false)
    expect(button('保存').disabled).toBe(true)
    await input(fields[1], '新昵称')
    await submit('account-profile-form')
    expect(request).toHaveBeenCalledWith('/me', { method: 'PATCH', body: JSON.stringify({ nickname: '新昵称' }) })
    expect(props.load).toHaveBeenCalledTimes(1)
  })

  it('retains failed profile edits and asks before discarding a draft', async () => {
    await render()
    request.mockRejectedValue(new Error('保存被拒绝'))
    await click(button('编辑资料'))
    const field = document.querySelectorAll<HTMLInputElement>('#account-profile-form input')[1]
    await input(field, '保留草稿')
    await submit('account-profile-form')
    expect(props.notify).toHaveBeenCalledWith('保存被拒绝', 'error')
    expect(field.value).toBe('保留草稿')
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('修改已保留')
    await click(button('取消'))
    expect(document.body.textContent).toContain('放弃未保存的修改？')
    await click(button('继续编辑'))
    expect(field.value).toBe('保留草稿')
  })

  it('validates password length and confirmation before submitting, then clears secrets', async () => {
    await render()
    await click(button('修改密码'))
    const fields = document.querySelectorAll<HTMLInputElement>('#account-password-form input')
    expect(fields).toHaveLength(3)
    expect(Array.from(fields).map(f => f.autocomplete)).toEqual(['current-password', 'new-password', 'new-password'])
    await input(fields[0], 'old-password')
    await input(fields[1], 'short')
    await input(fields[2], 'other')
    const save = document.querySelector<HTMLButtonElement>('button[form="account-password-form"]')!
    expect(save.disabled).toBe(true)
    expect(fields[1].getAttribute('aria-invalid')).toBe('true')
    expect(document.getElementById('account-password-mismatch')?.textContent).toContain('不一致')
    await input(fields[1], 'new-password')
    await input(fields[2], 'new-password')
    await submit('account-password-form')
    expect(request).toHaveBeenCalledWith('/auth/password', { method: 'POST', body: JSON.stringify({ current_password: 'old-password', new_password: 'new-password' }) })
    expect(document.getElementById('account-password-form')).toBeNull()
  })

  it('keeps password failure visible without clearing the draft', async () => {
    await render()
    request.mockRejectedValue(new Error('密码错误'))
    await click(button('修改密码'))
    const fields = document.querySelectorAll<HTMLInputElement>('#account-password-form input')
    await input(fields[0], 'old-password')
    await input(fields[1], 'new-password')
    await input(fields[2], 'new-password')
    await submit('account-password-form')
    expect(fields[0].value).toBe('old-password')
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('密码未修改')
    expect(props.notify).toHaveBeenCalledWith('密码错误', 'error')
  })

  it('configures Age in a dialog and preserves required-encryption policy', async () => {
    await render({ data: { ...props.data, current_user: { ...user, subscription_age_policy: 'required' } } })
    await click(button('Age 强制加密'))
    const toggle = document.querySelector<HTMLInputElement>('input[role="switch"]')!
    expect(toggle.checked).toBe(true)
    expect(toggle.disabled).toBe(true)
    expect(button('保存').disabled).toBe(true)
    const field = document.querySelector<HTMLTextAreaElement>('.age-public-key-textarea')!
    expect(field.closest('label')?.textContent).toContain('Age 公钥')
    await input(field, 'age1example-public-key')
    await submit('account-age-form')
    expect(request).toHaveBeenCalledWith('/me/subscription-age', { method: 'PATCH', body: JSON.stringify({ enabled: true, public_key: 'age1example-public-key' }) })
  })

  it('keeps optional Age controls progressive and preserves a failed save', async () => {
    await render()
    await click(button('配置'))
    expect(document.querySelector('.age-public-key-textarea')).toBeNull()
    await click(document.querySelector<HTMLInputElement>('input[role="switch"]')!)
    const field = document.querySelector<HTMLTextAreaElement>('.age-public-key-textarea')!
    await input(field, 'age1draft')
    request.mockRejectedValue(new Error('公钥无效'))
    await submit('account-age-form')
    expect(field.value).toBe('age1draft')
    expect(document.querySelector('[role="alert"]')?.textContent).toContain('修改已保留')
    expect(props.notify).toHaveBeenCalledWith('公钥无效', 'error')
  })

  it('keeps TOTP setup password verification, recovery-code handoff and csrf update', async () => {
    prompt.mockResolvedValueOnce('current-password')
    request.mockImplementation(async (path: string) => path === '/me/totp/setup/begin' ? { secret: 'test-secret', qr_data_url: 'test-qr' } : authentication)
    await render({
      TOTPSetupDialog: ({ setup, onComplete }) => <button onClick={() => void onComplete({ recovery_codes: ['recovery-one'], csrf_token: 'new-csrf' })}>确认绑定 {setup.secret}</button>,
      RecoveryCodesDialog: ({ codes, onClose }) => <div data-testid="recovery-codes">{codes.join(',')}<button onClick={onClose}>完成</button></div>,
    })
    await click(button('开启'))
    expect(prompt).toHaveBeenCalledWith(expect.objectContaining({ inputType: 'password' }))
    expect(request).toHaveBeenCalledWith('/me/totp/setup/begin', { method: 'POST', body: JSON.stringify({ current_password: 'current-password' }) })
    await click(button('确认绑定 test-secret'))
    expect(sessionStorage.getItem('oboard.csrf')).toBe('new-csrf')
    expect(container.querySelector('[data-testid="recovery-codes"]')?.textContent).toContain('recovery-one')
    await click(button('完成'))
    expect(container.textContent).not.toContain('recovery-one')
  })

  it('does not disable TOTP on cancelled confirmation and verifies both factors on approval', async () => {
    request.mockResolvedValue({ ...authentication, totp_enabled: true, recovery_codes_remaining: 4, csrf_token: 'rotated' })
    await render()
    await click(button('停用'))
    expect(prompt).not.toHaveBeenCalled()
    expect(request).not.toHaveBeenCalledWith('/me/totp/disable', expect.anything())
    confirm.mockResolvedValue(true)
    prompt.mockResolvedValueOnce('password').mockResolvedValueOnce('123456')
    await click(button('停用'))
    expect(request).toHaveBeenCalledWith('/me/totp/disable', { method: 'POST', body: JSON.stringify({ current_password: 'password', code: '123456' }) })
    expect(sessionStorage.getItem('oboard.csrf')).toBe('rotated')
  })

  it('regenerates recovery codes only after password and authenticator prompts', async () => {
    request.mockImplementation(async (path: string) => path === '/me/totp/recovery-codes' ? { recovery_codes: ['replacement-code'] } : { ...authentication, totp_enabled: true, recovery_codes_remaining: 2 })
    prompt.mockResolvedValueOnce('password').mockResolvedValueOnce('recovery-old')
    await render({ RecoveryCodesDialog: ({ codes }) => <div>{codes.join(',')}</div> })
    await click(button('生成新恢复码'))
    expect(prompt).toHaveBeenNthCalledWith(1, expect.objectContaining({ message: '生成后，之前的恢复码会立即失效。' }))
    expect(request).toHaveBeenCalledWith('/me/totp/recovery-codes', { method: 'POST', body: JSON.stringify({ current_password: 'password', code: 'recovery-old' }) })
    expect(container.textContent).toContain('replacement-code')
  })

  it('preserves Passkey challenge and native credential flow with TOTP', async () => {
    request.mockImplementation(async (path: string) => path === '/me/passkeys/register/begin' ? { options: { challenge: 'test-challenge' }, challenge_token: 'signed-challenge' } : { ...authentication, totp_enabled: true })
    prompt.mockResolvedValueOnce('笔记本').mockResolvedValueOnce('password').mockResolvedValueOnce('123456')
    await render()
    await click(button('管理', container.querySelector('#setting-row-passkeys')!))
    await click(button('添加通行密钥'))
    expect(request).toHaveBeenCalledWith('/me/passkeys/register/begin', { method: 'POST', body: JSON.stringify({ name: '笔记本', current_password: 'password', code: '123456' }) })
    expect(props.createPasskeyCredential).toHaveBeenCalledWith({ challenge: 'test-challenge' })
    expect(request).toHaveBeenCalledWith('/me/passkeys/register/finish', { method: 'POST', body: JSON.stringify({ challenge_token: 'signed-challenge', credential: { id: 'credential' } }) })
  })

  it('requires destructive confirmation before removing a named Passkey', async () => {
    request.mockResolvedValue({ ...authentication, passkeys: [{ id: 'key-1', name: '笔记本', created_at: '2026-09-01' }] })
    await render()
    await click(button('通行密钥 1 个'))
    const remove = container.querySelector<HTMLButtonElement>('[aria-label="移除通行密钥 笔记本"]')!
    await click(remove)
    expect(request).not.toHaveBeenCalledWith('/me/passkeys/key-1', expect.anything())
    confirm.mockResolvedValue(true)
    prompt.mockResolvedValueOnce('password')
    await click(remove)
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ tone: 'danger', message: expect.stringContaining('笔记本') }))
    expect(request).toHaveBeenCalledWith('/me/passkeys/key-1', { method: 'DELETE', body: JSON.stringify({ current_password: 'password', code: '' }) })
  })

  it('disables unsupported Passkey registration without hiding existing key management', async () => {
    await render({ passkeyAvailable: () => false })
    await click(button('通行密钥 0 个'))
    expect(button('添加通行密钥').disabled).toBe(true)
    expect(container.textContent).toContain('HTTPS')
  })

  it('copies only the deployed SSH link for the selected branch and device', async () => {
    const url = 'ssh://opaque-user:current-secret@1.2.3.4:2222'
    request.mockImplementation(async (path: string) => path === '/node-library/share' ? { url } : authentication)
    await render()
    await click(button('查看入口'))
    await click(button('复制'))
    expect(request).toHaveBeenCalledWith('/node-library/share', { method: 'POST', body: JSON.stringify({ node_id: 'proxy_path:7', device_id: 'device-a' }) })
    expect(props.copyText).toHaveBeenCalledExactlyOnceWith(url)
    expect(container.textContent).toContain('已复制')
    expect(container.textContent).not.toContain('current-secret')
  })
})
