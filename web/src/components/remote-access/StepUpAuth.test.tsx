// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { passkeyErrorMessage, StepUpAuth } from './StepUpAuth'

const challenge = {
  challenge_id: 'challenge-1',
  passkey_available: true,
  passkey: {
    publicKey: {
      challenge: 'AAAA',
      allowCredentials: [{ id: 'BBBB', type: 'public-key' }],
    },
  },
}

function buttonByLabel(label: string) {
  return Array.from(document.querySelectorAll('button')).find(button => button.textContent?.includes(label))
}

describe('passkeyErrorMessage', () => {
  it('replaces the WebAuthn privacy error with a short Chinese message', () => {
    const error = new DOMException(
      'The operation either timed out or was not allowed. See: https://www.w3.org/TR/webauthn-2/#sctn-privacy-considerations-client.',
      'NotAllowedError',
    )
    expect(passkeyErrorMessage(error)).toBe('未完成通行密钥验证')
  })
})

describe('StepUpAuth', () => {
  let container: HTMLDivElement
  let root: Root
  let getCredential: ReturnType<typeof vi.fn>

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    getCredential = vi.fn()
    Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true })
    ;(window as Window & { PublicKeyCredential?: unknown }).PublicKeyCredential = function PublicKeyCredential() {}
    Object.defineProperty(navigator, 'credentials', {
      configurable: true,
      value: { get: getCredential, create: vi.fn() },
    })
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.restoreAllMocks()
    delete (window as Window & { PublicKeyCredential?: unknown }).PublicKeyCredential
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('automatically prompts for an available passkey when requested', async () => {
    getCredential.mockResolvedValue({
      id: 'credential-1',
      rawId: new Uint8Array([1]).buffer,
      type: 'public-key',
      authenticatorAttachment: 'platform',
      getClientExtensionResults: () => ({}),
      response: {
        clientDataJSON: new Uint8Array([2]).buffer,
        authenticatorData: new Uint8Array([3]).buffer,
        signature: new Uint8Array([4]).buffer,
        userHandle: null,
      },
    })
    const onComplete = vi.fn()
    const request = vi.fn(async (path: string) => {
      if (path === '/auth/step-up/begin') return challenge
      if (path === '/auth/step-up/passkey/finish') return { step_up_token: 'step-up-token' }
      throw new Error(`unexpected ${path}`)
    })

    await act(async () => {
      root.render(
        <StepUpAuth
          request={request}
          purpose="remote_terminal"
          resourceType="server"
          resourceId={1}
          autoStartPasskey
          title="打开远程终端"
          warning="确认后即可打开这台服务器的 WebSSH。"
          onComplete={onComplete}
          onCancel={() => undefined}
        />,
      )
      await Promise.resolve()
    })
    await act(async () => {
      await vi.waitFor(() => expect(onComplete).toHaveBeenCalledWith('step-up-token'))
    })

    expect(getCredential).toHaveBeenCalledTimes(1)
    expect(request).toHaveBeenCalledWith('/auth/step-up/passkey/finish', expect.objectContaining({ method: 'POST' }))
  })

  it('keeps the confirm label stable while a passkey prompt is open', async () => {
    let release: ((value: never) => void) | undefined
    getCredential.mockReturnValue(new Promise<never>((_resolve, reject) => {
      release = reject
    }))
    const request = vi.fn(async (path: string) => {
      if (path === '/auth/step-up/begin') return challenge
      throw new Error(`unexpected ${path}`)
    })

    await act(async () => {
      root.render(
        <StepUpAuth
          request={request}
          purpose="remote_terminal"
          resourceType="server"
          resourceId={1}
          title="打开远程终端"
          warning="确认后即可打开这台服务器的 WebSSH。"
          onComplete={() => undefined}
          onCancel={() => undefined}
        />,
      )
      await Promise.resolve()
    })

    await act(async () => {
      buttonByLabel('使用通行密钥')?.click()
    })

    const actions = document.querySelector('.step-up-actions')
    expect(actions).not.toBeNull()
    expect(actions?.classList.contains('dialog-actions')).toBe(false)
    expect(buttonByLabel('确认')?.textContent).toBe('确认')
    expect(buttonByLabel('使用通行密钥')?.textContent).toContain('使用通行密钥')
    expect(buttonByLabel('确认')?.textContent).not.toContain('验证中')

    await act(async () => {
      release?.(new DOMException('aborted', 'AbortError'))
      await Promise.resolve()
    })
  })

  it('shows a short Chinese message when passkey verification is cancelled or times out', async () => {
    getCredential.mockRejectedValue(new DOMException(
      'The operation either timed out or was not allowed. See: https://www.w3.org/TR/webauthn-2/#sctn-privacy-considerations-client.',
      'NotAllowedError',
    ))
    const request = vi.fn(async (path: string) => {
      if (path === '/auth/step-up/begin') return challenge
      throw new Error(`unexpected ${path}`)
    })

    await act(async () => {
      root.render(
        <StepUpAuth
          request={request}
          purpose="remote_terminal"
          resourceType="server"
          resourceId={1}
          title="打开远程终端"
          warning="确认后即可打开这台服务器的 WebSSH。"
          onComplete={() => undefined}
          onCancel={() => undefined}
        />,
      )
      await Promise.resolve()
    })

    await act(async () => {
      buttonByLabel('使用通行密钥')?.click()
      await Promise.resolve()
    })

    const alert = document.querySelector('[role="alert"]')
    expect(alert?.textContent).toBe('未完成通行密钥验证')
    expect(alert?.textContent).not.toMatch(/webauthn/i)
  })
})
