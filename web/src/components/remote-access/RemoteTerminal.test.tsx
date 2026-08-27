// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 120
    rows = 32
    loadAddon() {}
    open() {}
    dispose() {}
    write() {}
    clear() {}
    getSelection() { return '' }
    onData() { return { dispose() {} } }
  },
}))

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit() {}
  },
}))

vi.mock('@xterm/xterm/css/xterm.css', () => ({}))

import { RemoteTerminal } from './RemoteTerminal'

describe('RemoteTerminal', () => {
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

  it('uses a large terminal dialog and a compact Ctrl+C shortcut', () => {
    act(() => root.render(
      <RemoteTerminal
        serverId={1}
        serverName="GL-U"
        client={{ request: vi.fn() }}
        websocketURL={() => 'ws://example.test/terminal'}
        passwordConfirmationRequired
        onClose={() => undefined}
      />,
    ))

    expect(document.querySelector('.remote-terminal-dialog')).not.toBeNull()
    const interrupt = document.querySelector<HTMLButtonElement>('button[aria-label="发送 Ctrl+C"]')
    expect(interrupt?.textContent).toBe('Ctrl+C')
    expect(interrupt?.classList.contains('icon-button')).toBe(false)
    expect(interrupt?.classList.contains('remote-terminal-shortcut')).toBe(true)
  })
})
