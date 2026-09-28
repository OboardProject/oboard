// @vitest-environment jsdom

import React, { act, useEffect, useState } from 'react'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createRoot, type Root } from 'react-dom/client'
import ts from 'typescript'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

const source = readFileSync(resolve(__dirname, 'main.tsx'), 'utf8')
const component = source.slice(source.indexOf('function Login('), source.indexOf('\nfunction renderTab('))
const compiled = ts.transpileModule(component, { compilerOptions: { jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ES2022 } }).outputText
const Icon = () => null
const request = vi.fn(async (path: string) => {
  if (path === '/auth/registration') return { registration_enabled: false }
  if (path === '/auth/passkey/login/begin') return { options: {}, challenge_token: 'challenge' }
  throw new Error('unexpected request: ' + path)
})
const getPasskeyCredential = vi.fn(async () => { throw Object.assign(new Error('cancelled'), { name: 'NotAllowedError' }) })
const dependencies = {
  React, useEffect, useState, api: () => ({ request }), passkeyAvailable: () => true,
  getPasskeyCredential, localizeErrorMessage: String, motion: { div: 'div' },
  ThemeSelector: () => null, User: Icon, Lock: Icon, Eye: Icon, EyeOff: Icon,
  Smartphone: Icon, Fingerprint: Icon,
}
const Login = new Function(...Object.keys(dependencies), compiled + '\nreturn Login')(...Object.values(dependencies))

let root: Root
let container: HTMLDivElement
beforeEach(() => {
  ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.clearAllMocks()
  ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
})

it('shows passkey cancellation as a toast without adding an inline error', async () => {
  const onToast = vi.fn()
  await act(async () => root.render(<Login theme="light" onThemeChange={vi.fn()} onToken={vi.fn()} onToast={onToast} />))
  expect(container.querySelector('.login-panel-desc')).toBeNull()

  await act(async () => {
    container.querySelector<HTMLButtonElement>('.login-passkey')?.click()
  })

  expect(onToast).toHaveBeenCalledExactlyOnceWith('未完成通行密钥验证', 'warning')
  expect(container.querySelector('.login-error')).toBeNull()
  expect(container.querySelector('.login-submit')?.textContent).toBe('登录')
})

it('keeps other passkey failures beside the login action', async () => {
  getPasskeyCredential.mockRejectedValueOnce(new Error('凭证不可用'))
  const onToast = vi.fn()
  await act(async () => root.render(<Login theme="light" onThemeChange={vi.fn()} onToken={vi.fn()} onToast={onToast} />))
  await act(async () => {
    container.querySelector<HTMLButtonElement>('.login-passkey')?.click()
  })

  expect(container.querySelector('.login-error')?.textContent).toBe('凭证不可用')
  expect(onToast).not.toHaveBeenCalled()
})
