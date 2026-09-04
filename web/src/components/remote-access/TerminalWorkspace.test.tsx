// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 120
    rows = 32
    textarea = document.createElement('textarea')
    loadAddon() {}
    open() {}
    dispose() {}
    write() {}
    clear() {}
    focus() {}
    getSelection() { return '' }
    onData() { return { dispose() {} } }
  },
}))

vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() {} } }))
vi.mock('@xterm/xterm/css/xterm.css', () => ({}))

import { TerminalWorkspace, maxTerminalSessions } from './TerminalWorkspace'

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

  send(data: unknown) { this.sent.push(data) }
  close() { this.readyState = FakeTerminalSocket.CLOSED; this.onclose?.() }
  open() { this.readyState = FakeTerminalSocket.OPEN; this.onopen?.() }
  emitJSON(payload: Record<string, unknown>) { this.onmessage?.({ data: JSON.stringify(payload) }) }

  text() {
    const decoder = new TextDecoder()
    return this.sent
      .filter((chunk): chunk is ArrayBufferView => typeof chunk !== 'string' && ArrayBuffer.isView(chunk as any))
      .map(chunk => decoder.decode(chunk))
      .join('')
  }
}

async function flush() {
  await act(async () => { await Promise.resolve() })
}

function makeServers(count: number) {
  return Array.from({ length: count }, (_, index) => ({
    id: index + 1,
    name: `node-${index + 1}`,
    status: 'online',
    agent_id: `agent-${index + 1}`,
  }))
}

function makeRequest() {
  let counter = 0
  return vi.fn(async (path: string) => {
    if (path.includes('/terminal/sessions') && !path.endsWith('/sessions/')) {
      counter += 1
      return { session_id: `sess-${counter}`, mode: 'login', login_env: true }
    }
    if (path.startsWith('/auth/step-up/begin')) return { challenge_id: 'chal-1', passkey_available: false }
    return {}
  })
}

function renderWorkspace(root: Root, props: Partial<React.ComponentProps<typeof TerminalWorkspace>> = {}) {
  const request = props.client?.request || makeRequest()
  act(() => root.render(
    <TerminalWorkspace
      servers={props.servers || makeServers(3)}
      initialServerId={props.initialServerId ?? null}
      client={{ request } as any}
      websocketURL={(serverId, sessionId) => `ws://example.test/${serverId}/${sessionId}`}
      passwordConfirmationRequired={props.passwordConfirmationRequired ?? false}
      onClose={props.onClose || (() => undefined)}
    />,
  ))
  return request as ReturnType<typeof makeRequest>
}

// React tracks the DOM value itself, so a plain assignment plus an input event looks like a
// no-op change. Go through the prototype setter the tracker watches.
function typeCommand(value: string) {
  const field = document.querySelector<HTMLTextAreaElement>('#terminal-batch-command')
  if (!field) throw new Error('batch command field is missing')
  const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set
  act(() => {
    setter?.call(field, value)
    field.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

function clickText(selector: string, text: string) {
  const button = Array.from(document.querySelectorAll<HTMLElement>(selector))
    .find(node => node.textContent?.includes(text))
  if (!button) throw new Error(`no ${selector} containing ${text}`)
  act(() => button.click())
}

describe('TerminalWorkspace', () => {
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

  it('opens on the server picker when no server was chosen from the card menu', () => {
    renderWorkspace(root)
    expect(document.querySelectorAll('.terminal-server-option')).toHaveLength(3)
    expect(document.querySelector('.terminal-sidebar-empty')?.textContent).toBe('还没有终端会话')
  })

  it('connects the server the card menu opened and lists it in the sidebar', async () => {
    const request = renderWorkspace(root, { initialServerId: 2 })
    await flush()
    await flush()
    expect(request.mock.calls.some(call => String(call[0]).startsWith('/servers/2/terminal/sessions'))).toBe(true)
    expect(document.querySelector('.terminal-session-name')?.textContent).toBe('node-2')

    act(() => FakeTerminalSocket.instances[0].open())
    act(() => FakeTerminalSocket.instances[0].emitJSON({ type: 'ready' }))
    expect(document.querySelector('.terminal-session-status')?.textContent).toBe('已连接')
  })

  it('offline and unenrolled servers cannot be picked', () => {
    renderWorkspace(root, {
      servers: [
        { id: 1, name: 'online', status: 'online', agent_id: 'a1' },
        { id: 2, name: 'offline', status: 'offline', agent_id: 'a2' },
        { id: 3, name: 'unenrolled', status: 'online', agent_id: '' },
      ],
    })
    const options = Array.from(document.querySelectorAll<HTMLButtonElement>('.terminal-server-option'))
    expect(options.map(option => option.disabled)).toEqual([false, true, true])
    expect(options[1].textContent).toContain('Agent 当前离线')
    expect(options[2].textContent).toContain('未接入 Agent')
  })

  it('connects every selected server and runs the batch command once each becomes ready', async () => {
    const request = renderWorkspace(root)
    clickText('.terminal-sidebar-actions .ghost', '批量执行')

    typeCommand('uptime')
    const checkboxes = Array.from(document.querySelectorAll<HTMLInputElement>('.terminal-server-check input'))
    act(() => { checkboxes[0].click(); checkboxes[2].click() })
    clickText('.terminal-batch-actions button', '连接并执行')
    await flush()
    await flush()

    const created = request.mock.calls.map(call => String(call[0])).filter(path => path.includes('/terminal/sessions'))
    expect(created).toEqual(['/servers/1/terminal/sessions', '/servers/3/terminal/sessions'])
    expect(document.querySelectorAll('.terminal-session-item')).toHaveLength(2)
    expect(FakeTerminalSocket.instances).toHaveLength(2)

    for (const socket of FakeTerminalSocket.instances) {
      act(() => socket.open())
      act(() => socket.emitJSON({ type: 'ready' }))
    }
    expect(FakeTerminalSocket.instances.map(socket => socket.text())).toEqual(['uptime\r', 'uptime\r'])
    expect(Array.from(document.querySelectorAll('.terminal-session-status')).map(node => node.textContent))
      .toEqual(['已执行', '已执行'])
  })

  it('asks for one server_set step-up covering the whole batch', async () => {
    const request = renderWorkspace(root, { passwordConfirmationRequired: true })
    clickText('.terminal-sidebar-actions .ghost', '批量执行')

    typeCommand('df -h')
    const checkboxes = Array.from(document.querySelectorAll<HTMLInputElement>('.terminal-server-check input'))
    act(() => { checkboxes[0].click(); checkboxes[1].click() })
    clickText('.terminal-batch-actions button', '连接并执行')
    await flush()
    await flush()

    const begin = request.mock.calls.find(call => String(call[0]) === '/auth/step-up/begin')
    expect(begin).toBeTruthy()
    expect(JSON.parse(String((begin![1] as RequestInit).body))).toMatchObject({
      purpose: 'remote_terminal',
      resource: { type: 'server_set', id: '1,2' },
    })
    // No terminal is opened before the operator finishes the single confirmation.
    expect(request.mock.calls.some(call => String(call[0]).includes('/terminal/sessions'))).toBe(false)
  })

  it('refuses a batch that would exceed the session ceiling instead of failing halfway', async () => {
    renderWorkspace(root, { servers: makeServers(maxTerminalSessions + 2) })
    clickText('.terminal-sidebar-actions .ghost', '批量执行')

    typeCommand('uptime')
    clickText('.terminal-batch-toolbar .ghost', '全选筛选结果')
    clickText('.terminal-batch-actions button', '连接并执行')
    await flush()

    expect(document.querySelector('.remote-terminal-banner')?.textContent)
      .toContain(`最多同时保持 ${maxTerminalSessions} 个终端会话`)
    expect(FakeTerminalSocket.instances).toHaveLength(0)
  })
})
