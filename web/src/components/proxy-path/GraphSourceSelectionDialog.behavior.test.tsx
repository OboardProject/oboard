// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { GraphSourceSelectionDialog, type GraphSourceSelectionRequest } from './GraphSourceSelectionDialog'

vi.mock('../ui/motion', () => ({
  MotionDialogPanel: ({ children }: { children: React.ReactNode }) => <section>{children}</section>,
}))

describe('GraphSourceSelectionDialog', () => {
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

  it('selects an inbound by clicking the whole row without rendering switches', () => {
    const submit = vi.fn()
    const request: GraphSourceSelectionRequest = {
      title: 'WAWO',
      options: [
        { key: 'inbound:11', label: '主入口', detail: 'VLESS:443', source: { inbound_id: 11 } },
        { key: 'inbound:12', label: '备用入口', detail: 'Shadowsocks:8443', source: { inbound_id: 12 } },
      ],
      resolve: () => {},
    }

    act(() => root.render(<GraphSourceSelectionDialog request={request} onCancel={() => {}} onSubmit={submit} />))

    const choices = Array.from(container.querySelectorAll<HTMLButtonElement>('[role="radio"]'))
    const continueButton = Array.from(container.querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent === '继续')
    expect(choices).toHaveLength(2)
    expect(container.querySelector('[role="switch"]')).toBeNull()
    expect(continueButton?.disabled).toBe(true)

    act(() => choices[1].click())
    expect(choices[0].getAttribute('aria-checked')).toBe('false')
    expect(choices[1].getAttribute('aria-checked')).toBe('true')
    expect(continueButton?.disabled).toBe(false)
    expect(submit).not.toHaveBeenCalled()

    act(() => continueButton?.click())
    expect(submit).toHaveBeenCalledWith([{ inbound_id: 12 }])
  })
})
