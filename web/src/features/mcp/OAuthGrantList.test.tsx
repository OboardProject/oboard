// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { OAuthGrantList } from './OAuthGrantList'
import type { OAuthGrant } from './types'

function grant(overrides: Partial<OAuthGrant> = {}): OAuthGrant {
  return {
    id: 'grant-active',
    client_id: 'client-active',
    client_name: 'OpenCode Active',
    user_id: 1,
    username: 'admin',
    access_level: 'operate',
    effective_role: 'admin',
    offline_access: true,
    policy_version: 1,
    role_version: 1,
    consent_version: 1,
    status: 'active',
    created_at: '2026-08-10T08:00:00Z',
    last_used_at: null,
    expires_at: null,
    revoked_at: null,
    ...overrides,
  }
}

describe('OAuthGrantList', () => {
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
    vi.restoreAllMocks()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('hides revoked grants and removes a grant immediately after revocation', async () => {
    const requestV2 = vi.fn(async (path: string, init?: RequestInit) => {
      if (path === '/oauth-grants' && !init) {
        return [
          grant(),
          grant({ id: 'grant-revoked', client_name: 'OpenCode Revoked', status: 'revoked', revoked_at: '2026-08-10T09:00:00Z' }),
        ]
      }
      if (path === '/oauth-grants/grant-active' && init?.method === 'DELETE') return { revoked: true }
      throw new Error(`unexpected request: ${path}`)
    })
    const notify = vi.fn()
    const confirm = vi.fn(async () => true)

    await act(async () => {
      root.render(<OAuthGrantList request={vi.fn()} requestV2={requestV2} notify={notify} confirm={confirm} />)
      await Promise.resolve()
    })

    expect(container.textContent).toContain('OpenCode Active')
    expect(container.textContent).not.toContain('OpenCode Revoked')

    await act(async () => {
      container.querySelector<HTMLButtonElement>('[aria-label="撤销 OpenCode Active"]')!.click()
      await Promise.resolve()
    })

    expect(requestV2).toHaveBeenCalledWith('/oauth-grants/grant-active', { method: 'DELETE' })
    expect(container.textContent).not.toContain('OpenCode Active')
    expect(container.textContent).toContain('暂无已授权访问')
    expect(notify).toHaveBeenCalledWith('授权已撤销', 'success')
    expect(requestV2.mock.calls.filter(([path]) => path === '/oauth-grants')).toHaveLength(1)
  })
})
