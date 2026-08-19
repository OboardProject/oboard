// @vitest-environment jsdom

import { createServer, request as httpRequest } from 'node:http'
import { useCallback, useEffect, useRef, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { describe, expect, it } from 'vitest'

import { realtimeInvalidatedPages, scheduleRealtimeRefresh } from './realtime-pages'
import { usePollingEvents } from './realtime'

function localJSON(url: string, method = 'GET'): Promise<any> {
  return new Promise((resolve, reject) => {
    const request = httpRequest(new URL(url), { method, headers: { 'content-type': 'application/json' } }, response => {
      let body = ''
      response.setEncoding('utf8')
      response.on('data', chunk => { body += chunk })
      response.on('end', () => {
        try { resolve(JSON.parse(body)) } catch (error) { reject(error) }
      })
    })
    request.on('error', reject)
    request.end()
  })
}

function CrossSessionPage({ baseURL, sample, onConsistent }: { baseURL: string; sample: number; onConsistent: (latencyMS: number, rendered: string) => void }) {
  const [page, setPage] = useState('old')
  const startedAt = useRef(performance.now())
  const writeSent = useRef(false)
  const onEvent = useCallback((event: any) => {
    const pages = realtimeInvalidatedPages(event, 'servers', ['servers'])
    const dirtyPages = new Set(['servers'])
    scheduleRealtimeRefresh({
      page: 'servers',
      activePage: 'servers',
      visible: true,
      dirtyPages,
      hasPendingRequest: false,
      schedule: (callback, delayMS) => window.setTimeout(callback, delayMS),
      refresh: refreshedPage => {
        if (!pages.has(refreshedPage)) return
        void localJSON(`${baseURL}/page-data?page=${refreshedPage}`)
          .then(data => setPage(data.servers[0].name))
      },
    })
  }, [baseURL])
  const request = useCallback(async (path: string) => {
    if (path.startsWith('/poll-events')) {
      if (!writeSent.current) {
        writeSent.current = true
        await localJSON(`${baseURL}/api/v2/ui/servers/1`, 'PATCH')
        return localJSON(`${baseURL}${path}`)
      }
      return new Promise<any>(() => undefined)
    }
    await localJSON(`${baseURL}/api/v2/ui/servers/1`, 'PATCH')
    return new Promise<any>(() => undefined)
  }, [baseURL])
  usePollingEvents(true, request, onEvent)

  useEffect(() => {
    if (page !== 'old') onConsistent(performance.now() - startedAt.current, page)
  }, [onConsistent, page])

  return <div data-page={sample}>{page}</div>
}

describe('cross-session page consistency over local HTTP and DOM', () => {
  it('measures state commit through event, page-data and rendered DOM', async () => {
    const server = createServer((request, response) => {
      response.setHeader('content-type', 'application/json')
      if (request.url?.startsWith('/poll-events')) {
        response.end(JSON.stringify({ type: 'invalidate', sequence: 1, resources: ['configuration'] }))
        return
      }
      if (request.url?.startsWith('/page-data')) {
        response.end(JSON.stringify({ servers: [{ id: 1, name: 'new-value' }] }))
        return
      }
      response.end(JSON.stringify({ desired_revision: 1, configuration_sync: [{ server_id: 1, state: 'pending' }] }))
    })
    await new Promise<void>(resolve => server.listen(0, '127.0.0.1', () => resolve()))
    const address = server.address()
    if (!address || typeof address === 'string') throw new Error('local benchmark server did not bind')
    const baseURL = `http://127.0.0.1:${address.port}`
    const roots: Root[] = []
    const containers: HTMLDivElement[] = []
    const samples = 32
    const completions: Promise<{ latencyMS: number; rendered: string }>[] = []
    const latencies: number[] = []

    try {
      for (let sample = 0; sample < samples; sample++) {
        const container = document.createElement('div')
        containers.push(container)
        document.body.appendChild(container)
        const root = createRoot(container)
        roots.push(root)
        completions.push(new Promise(resolve => {
          root.render(<CrossSessionPage baseURL={baseURL} sample={sample} onConsistent={(latencyMS, rendered) => resolve({ latencyMS, rendered })} />)
        }))
      }
      const results = await Promise.all(completions)
      results.forEach(result => {
        latencies.push(result.latencyMS)
        expect(result.rendered).toBe('new-value')
      })
      const sorted = [...latencies].sort((a, b) => a - b)
      const p95 = sorted[Math.ceil(samples * 0.95) - 1]
      console.info(`cross_session_ui samples=${samples} p95_ms=${p95.toFixed(1)} max_ms=${Math.max(...latencies).toFixed(1)}`)
      expect(p95).toBeLessThanOrEqual(2_000)
      expect(containers.every(container => container.textContent === 'new-value')).toBe(true)
    } finally {
      roots.forEach(root => root.unmount())
      containers.forEach(container => container.remove())
      await new Promise<void>(resolve => server.close(() => resolve()))
    }
  }, 10_000)
})
