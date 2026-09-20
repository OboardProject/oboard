// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { OAuthClientList } from './OAuthClientList'

const flush = async () => { await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) }) }

describe('OAuth client submission snapshot', () => {
  let root: Root
  let container: HTMLDivElement
  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container)
  })
  afterEach(() => { act(() => root.unmount()); container.remove() })

  it('retains newer input and changes subsequent saves to the created identity', async () => {
    let finish!: (value: unknown) => void
    const pending = new Promise(resolve => { finish = resolve })
    const requestV2 = vi.fn(async (_path: string, init?: RequestInit) => {
      if (init?.method === 'POST') return pending
      if (init?.method === 'PATCH') return { id: 'client-7' }
      return []
    })
    await act(async () => root.render(<OAuthClientList requestV2={requestV2} notify={vi.fn()} confirm={vi.fn(async () => true)} />))
    await flush()
    act(() => Array.from(container.querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent === '注册')!.click())
    await flush()
    const input = document.querySelector<HTMLInputElement>('#oauth-client-form input[required]')!
    const change = (value: string) => act(() => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    change('初稿')
    const form = document.querySelector<HTMLFormElement>('#oauth-client-form')!
    act(() => { form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })) })
    change('后续修改')
    await act(async () => { finish({ id: 'client-7' }); await pending })
    await flush()
    expect(requestV2.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1)
    expect(document.querySelector<HTMLInputElement>('#oauth-client-form input[required]')?.value).toBe('后续修改')
    act(() => form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })))
    await flush()
    expect(requestV2).toHaveBeenCalledWith('/oauth-clients/client-7', expect.objectContaining({ method: 'PATCH', body: expect.stringContaining('后续修改') }))
  })
})
