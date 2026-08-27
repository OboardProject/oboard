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

class FakeTerminalSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  static instances: FakeTerminalSocket[] = []

  url: string
  readyState = FakeTerminalSocket.CONNECTING
  binaryType = 'blob'
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  sent: unknown[] = []

  constructor(url: string) {
    this.url = url
    FakeTerminalSocket.instances.push(this)
  }

  send(data: unknown) {
    this.sent.push(data)
  }

  close() {
    this.readyState = FakeTerminalSocket.CLOSED
    this.onclose?.()
  }

  open() {
    this.readyState = FakeTerminalSocket.OPEN
    this.onopen?.()
  }

  emitJSON(payload: Record<string, unknown>) {
    this.onmessage?.({ data: JSON.stringify(payload) })
  }
}

async function flush() {
  await act(async () => {
    await Promise.resolve()
  })
}

describe('RemoteTerminal', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    FakeTerminalSocket.instances = []
    vi.stubGlobal('WebSocket', FakeTerminalSocket)
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
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

  it('waits for agent ready before showing connected', async () => {
    const request = vi.fn().mockResolvedValue({ session_id: 'sess-1' })
    act(() => root.render(
      <RemoteTerminal
        serverId={1}
        serverName="GL-U"
        client={{ request }}
        websocketURL={sessionId => `ws://example.test/terminal/${sessionId}`}
        passwordConfirmationRequired={false}
        onClose={() => undefined}
      />,
    ))
    await flush()
    await flush()
    expect(FakeTerminalSocket.instances).toHaveLength(1)
    act(() => FakeTerminalSocket.instances[0].open())
    expect(document.querySelector('.muted')?.textContent).toBe('正在等待节点')
    act(() => FakeTerminalSocket.instances[0].emitJSON({ type: 'ready' }))
    expect(document.querySelector('.muted')?.textContent).toBe('已连接')
  })

  it('shows a readable pty start failure instead of staying connected', async () => {
    const request = vi.fn().mockResolvedValue({ session_id: 'sess-2' })
    act(() => root.render(
      <RemoteTerminal
        serverId={1}
        serverName="GL-U"
        client={{ request }}
        websocketURL={() => 'ws://example.test/terminal'}
        passwordConfirmationRequired={false}
        onClose={() => undefined}
      />,
    ))
    await flush()
    await flush()
    act(() => FakeTerminalSocket.instances[0].open())
    act(() => FakeTerminalSocket.instances[0].emitJSON({
      type: 'error',
      reason: 'pty_start_failed',
      detail: 'fork/exec /bin/bash: operation not permitted',
    }))
    expect(document.querySelector('.muted')?.textContent).toBe('节点无法启动终端：fork/exec /bin/bash: operation not permitted')
    act(() => FakeTerminalSocket.instances[0].close())
    expect(document.querySelector('.muted')?.textContent).toBe('节点无法启动终端：fork/exec /bin/bash: operation not permitted')
  })

  it('sends login mode and shows session identity from agent ready info', async () => {
    const request = vi.fn().mockResolvedValue({ session_id: 'sess-3', login_env: true, mode: 'login' })
    act(() => root.render(
      <RemoteTerminal
        serverId={1}
        serverName="GL-U"
        client={{ request }}
        websocketURL={() => 'ws://example.test/terminal'}
        passwordConfirmationRequired={false}
        onClose={() => undefined}
      />,
    ))
    await flush()
    await flush()
    expect(JSON.parse(String(request.mock.calls[0][1].body)).mode).toBe('login')
    act(() => FakeTerminalSocket.instances[0].open())
    act(() => FakeTerminalSocket.instances[0].emitJSON({
      type: 'ready',
      info: { username: 'root', uid: 0, gid: 0, home: '/root', shell: '/bin/bash', mode: 'login', cwd: '/root', term: 'xterm-256color' },
    }))
    expect(document.querySelector('.remote-terminal-identity')?.textContent).toBe('root · /bin/bash · Login')
    expect(document.querySelector('button[aria-label="最小环境打开"]')).not.toBeNull()
  })

  it('offers a minimal environment reopen after the shell exits', async () => {
    const request = vi.fn().mockResolvedValue({ session_id: 'sess-4', login_env: true, mode: 'login' })
    act(() => root.render(
      <RemoteTerminal
        serverId={1}
        serverName="GL-U"
        client={{ request }}
        websocketURL={() => 'ws://example.test/terminal'}
        passwordConfirmationRequired={false}
        onClose={() => undefined}
      />,
    ))
    await flush()
    await flush()
    act(() => FakeTerminalSocket.instances[0].open())
    act(() => FakeTerminalSocket.instances[0].emitJSON({ type: 'ready', info: { username: 'root', shell: '/bin/bash', mode: 'login' } }))
    act(() => FakeTerminalSocket.instances[0].emitJSON({ type: 'closed', reason: 'shell_exited' }))
    expect(document.querySelector('.muted')?.textContent).toBe('Shell 已退出')
    expect(document.querySelector('.remote-terminal-banner')?.textContent).toContain('以最小环境打开')
  })
})
