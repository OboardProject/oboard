// @vitest-environment jsdom

import { spawn, spawnSync, type ChildProcess } from 'node:child_process'
import { createServer } from 'node:net'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'

import { OBoardAppRoot } from './main'

class IdleWebSocket {
  onmessage: ((event: MessageEvent) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  onclose: ((event: CloseEvent) => void) | null = null
  close() { this.onclose?.({} as CloseEvent) }
}

async function freePort() {
  return new Promise<number>((resolvePort, reject) => {
    const server = createServer()
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      if (!address || typeof address === 'string') return reject(new Error('no test port'))
      server.close(error => error ? reject(error) : resolvePort(address.port))
    })
  })
}

async function waitFor(condition: () => boolean | Promise<boolean>, timeoutMS = 10_000) {
  const deadline = Date.now() + timeoutMS
  while (Date.now() < deadline) {
    if (await condition()) return
    await new Promise(resolve => setTimeout(resolve, 20))
  }
  throw new Error(`condition not met within ${timeoutMS}ms`)
}

describe('real Controller cross-session UI convergence SLO', () => {
  const workspace = resolve(import.meta.dirname, '../../..')
  const controllerRepo = join(workspace, 'oboard')
  const scope = mkdtempSync(join(tmpdir(), 'oboard-real-ui-'))
  const binary = join(scope, 'oboard-controller')
  const database = join(scope, 'oboard.sqlite')
  const staticDir = join(scope, 'static')
  const cache = join(scope, 'go-cache')
  const goTmp = join(scope, 'go-tmp')
  let controllerProcess: ChildProcess | null = null
  let baseURL = ''
  let token = ''
  let serverID = 0
  let root: Root | null = null
  let container: HTMLDivElement | null = null
  const nativeFetch = globalThis.fetch

  beforeAll(async () => {
    mkdirSync(staticDir, { recursive: true })
    mkdirSync(cache, { recursive: true })
    mkdirSync(goTmp, { recursive: true })
    writeFileSync(join(staticDir, 'index.html'), '<div id="root"></div>')
    const built = spawnSync('go', ['build', '-o', binary, './cmd/controller'], {
      cwd: controllerRepo,
      env: { ...processEnv(), GOCACHE: cache, GOTMPDIR: goTmp },
      encoding: 'utf8',
    })
    if (built.status !== 0) throw new Error(`controller build failed: ${built.stderr}`)
    const port = await freePort()
    baseURL = `http://127.0.0.1:${port}`
    controllerProcess = spawn(binary, ['-addr', `127.0.0.1:${port}`, '-db', database, '-static', staticDir, '-session-secret', 'real-ui-test-session-secret-at-least-32-chars', '-admin-password', 'very-secure-password'], {
      env: { ...processEnv(), OBOARD_LOG_OUTPUT: 'stdout', OBOARD_DISABLE_PUBLIC_IP_DETECT: '1' },
      stdio: 'ignore',
    })
    await waitFor(async () => {
      try { return (await nativeFetch(`${baseURL}/healthz`)).ok } catch { return false }
    }, 15_000)
    const loginResponse = await nativeFetch(`${baseURL}/api/v2/ui/auth/login`, {
      method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ username: 'admin', password: 'very-secure-password' }),
    })
    const login = await loginResponse.json() as any
    if (!loginResponse.ok || !login.token) throw new Error(`login failed: ${JSON.stringify(login)}`)
    token = login.token
    const createdResponse = await nativeFetch(`${baseURL}/api/v2/ui/servers`, {
      method: 'POST', headers: { 'content-type': 'application/json', authorization: `Bearer ${token}` },
      body: JSON.stringify({ name: 'real-ui-00', listen_ip: '0.0.0.0', port_range_start: 10000, port_range_end: 11000 }),
    })
    const created = await createdResponse.json() as any
    serverID = Number(created.server?.id || 0)
    if (!createdResponse.ok || !serverID) throw new Error(`server create failed: ${JSON.stringify(created)}`)
    sessionStorage.setItem('oboard.token', token)
    sessionStorage.setItem('oboard.user', JSON.stringify(login.user))
    sessionStorage.setItem('oboard.csrf', login.csrf_token || '')
    history.replaceState({}, '', '/servers')
    vi.stubGlobal('WebSocket', IdleWebSocket)
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url
      const url = /^https?:\/\//.test(raw) ? raw : `${baseURL}${raw.startsWith('/') ? raw : `/${raw}`}`
      return nativeFetch(url, init)
    })
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    root.render(<OBoardAppRoot />)
    await waitFor(() => container?.textContent?.includes('real-ui-00') === true, 15_000)
  }, 60_000)

  afterAll(async () => {
    root?.unmount()
    container?.remove()
    vi.unstubAllGlobals()
    sessionStorage.clear()
    localStorage.clear()
    if (controllerProcess && !controllerProcess.killed) {
      controllerProcess.kill('SIGTERM')
      await new Promise(resolveExit => controllerProcess?.once('exit', resolveExit))
    }
    rmSync(scope, { recursive: true, force: true })
  })

  it('measures committed SQLite writes through real poll-events, page-data and production DOM', async () => {
    const samples = 32
    const latencies: number[] = []
    for (let index = 1; index <= samples; index++) {
      const name = `real-ui-${String(index).padStart(2, '0')}`
      const response = await nativeFetch(`${baseURL}/api/v2/ui/servers/${serverID}`, {
        method: 'PATCH', headers: { 'content-type': 'application/json', authorization: `Bearer ${token}` }, body: JSON.stringify({ name }),
      })
      const result = await response.json() as any
      if (!response.ok || !result.state_committed_at) throw new Error(`PATCH failed: ${JSON.stringify(result)}`)
      const committedAt = Date.parse(result.state_committed_at)
      await waitFor(() => container?.textContent?.includes(name) === true, 5_000)
      latencies.push(Date.now() - committedAt)
    }
    const sorted = [...latencies].sort((a, b) => a - b)
    const p95 = sorted[Math.ceil(samples * 0.95) - 1]
    console.info(`real_controller_cross_session_ui samples=${samples} p95_ms=${p95} max_ms=${Math.max(...latencies)}`)
    expect(p95).toBeLessThanOrEqual(2_000)
    expect(container?.textContent).toContain('real-ui-32')
  }, 60_000)
})

function processEnv() {
  return Object.fromEntries(Object.entries(process.env).filter((entry): entry is [string, string] => typeof entry[1] === 'string'))
}
