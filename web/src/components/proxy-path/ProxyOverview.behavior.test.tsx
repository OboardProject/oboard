// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { LazyMotion, domAnimation } from 'motion/react'
import type { Connection, Edge, Node, ReactFlowProps } from 'reactflow'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ProxyOverview } from '../../main'
import { DialogContext } from '../ui/dialog-context'
import type { Inbound, Server } from './types'

const flow = vi.hoisted(() => ({ props: {} as ReactFlowProps }))
vi.mock('reactflow', async importOriginal => {
  const actual = await importOriginal<typeof import('reactflow')>()
  return {
    ...actual,
    // Only replace the browser canvas. State, layout, connections and dialogs
    // remain owned by the exported production component.
    default: (props: ReactFlowProps) => { flow.props = props; return <div data-testid="flow" /> },
    BaseEdge: ({ id, path, markerEnd, markerStart }: any) => <path data-testid={id} d={path} markerEnd={markerEnd} markerStart={markerStart} />,
    EdgeLabelRenderer: ({ children }: React.PropsWithChildren) => <>{children}</>,
  }
})

const servers = [1, 2, 3].map(id => ({
  id, name: `Server ${id}`, status: 'online', public_ipv4: `203.0.113.${id}`,
  entry_address: '', public_ipv6: '', interface_ipv6: '', region_code: 'US',
  detected_region_code: 'US', region_mode: 'manual', entry_ip_mode: 'auto',
  listen_ip: '0.0.0.0', listen_mode: 'auto', ip_stack: 'auto', udp_inbound_mode: 'allow',
} as Server))
const inbounds = [1, 2].map(id => ({
  id: id * 10, server_id: id, name: `Entry ${id}`, protocol: 'socks',
  port: 1080, listen_ip: '0.0.0.0', enabled: true, config_json: '{}',
} as Inbound))
const data = {
  session: { role: 'admin' }, servers, inbounds, proxy_paths: [], proxy_path_steps: [],
  external_outbounds: [], routing_rules: [], port_forwards: [], tunnels: [], warp_profiles: [],
}

describe('ProxyOverview canvas behavior', () => {
  let container: HTMLDivElement
  let root: Root
  const request = vi.fn(async () => ({}))
  const alert = vi.fn(async () => undefined)
  const nodes = () => flow.props.nodes as Node[]
  const edges = () => flow.props.edges as Edge[]
  const staged = () => nodes().filter(node => node.id.startsWith('canvas-server-'))
  const provisional = () => edges().filter(edge => edge.className === 'proxy-edge-provisional')
  const positions = () => Object.fromEntries(nodes().map(node => [node.id, { ...node.position }]))
  const button = (text: string) => {
    const found = Array.from(document.querySelectorAll('button')).find(item => item.textContent?.trim() === text || item.getAttribute('aria-label') === text)
    expect(found, `button ${text}`).toBeTruthy()
    return found!
  }
  const render = async (selectedServer = 1, pageData = data) => {
    await act(async () => root.render(<LazyMotion features={domAnimation}>
      <DialogContext.Provider value={{ confirm: async () => true, alert, prompt: async () => null }}>
        <ProxyOverview data={pageData} client={{ request }} selectedServer={selectedServer}
          setSelectedServer={() => undefined} load={async () => undefined} onServerSnapshot={() => undefined} />
      </DialogContext.Provider>
    </LazyMotion>))
  }
  const stage = async (id = 3) => {
    await act(async () => button('其他服务器').click())
    await act(async () => button(`将 Server ${id} 放入画布`).click())
    return staged().at(-1)!
  }
  const connect = (target: string): Connection => ({ source: 'entry-10', target, sourceHandle: null, targetHandle: null })
  const startConnection = async () => {
    const target = await stage()
    const connection = connect(target.id)
    await act(async () => flow.props.onConnect!(connection))
    return connection
  }
  const cancel = async () => { await act(async () => button('取消').click()) }

  beforeEach(() => {
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
    localStorage.clear()
    request.mockClear()
    alert.mockClear()
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })
  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    localStorage.clear()
    document.body.style.overflow = ''
  })

  it('stages a server without moving any existing canvas node or writing topology', async () => {
    await render()
    const before = positions()
    expect(Object.keys(before)).toContain('entry-10')
    const added = await stage()
    expect(added).toBeDefined()
    expect(nodes()).toHaveLength(Object.keys(before).length + 1)
    for (const [id, position] of Object.entries(before)) expect(positions()[id]).toEqual(position)
    expect(request).not.toHaveBeenCalled()
  })

  it('keeps staged servers isolated and restores each A/B canvas on switching', async () => {
    await render(1)
    const a = await stage(3)
    const aPosition = { ...a.position }
    await render(2)
    expect(staged()).toHaveLength(0)
    expect(nodes().some(node => node.id === 'entry-20')).toBe(true)
    const b = await stage(1)
    await render(1)
    expect(staged().map(node => node.id)).toEqual([a.id])
    expect(staged()[0].position).toEqual(aPosition)
    await render(2)
    expect(staged().map(node => node.id)).toEqual([b.id])
    expect(request).not.toHaveBeenCalled()
  })

  it('blocks repeated connect events while the first connection awaits confirmation', async () => {
    await render()
    const connection = await startConnection()
    expect(provisional()).toHaveLength(1)
    expect(flow.props.isValidConnection!(connection)).toBe(false)
    await act(async () => {
      flow.props.onConnect!(connection)
      flow.props.onConnect!(connection)
    })
    expect(provisional()).toHaveLength(1)
    expect(container.textContent).toContain('上一条连接仍在处理中')
    expect(request).not.toHaveBeenCalled()
    expect(alert).not.toHaveBeenCalled()
    await cancel()
  })

  it('cancels the provisional connection, clears busy nodes and allows a new attempt', async () => {
    await render()
    const connection = await startConnection()
    expect(provisional()).toHaveLength(1)
    expect(nodes().filter(node => node.className?.includes('graph-node-pending'))).toHaveLength(2)
    await cancel()
    expect(provisional()).toHaveLength(0)
    expect(nodes().some(node => node.className?.includes('graph-node-pending'))).toBe(false)
    expect(flow.props.isValidConnection!(connection)).toBe(true)
    expect(request).not.toHaveBeenCalled()
    await act(async () => flow.props.onConnect!(connection))
    expect(provisional()).toHaveLength(1)
    await cancel()
  })

  it('shows the empty canvas without a stuck loading indicator after the last server is removed', async () => {
    await render()
    await render(0, { ...data, servers: [], inbounds: [] })
    expect(container.querySelector('.graph-canvas-loading')).toBeNull()
    expect(container.textContent).toContain('还没有服务器')
  })

  it('renders the production transport edge without arrow markers even if supplied', async () => {
    await render()
    const Renderer = flow.props.edgeTypes!.proxyTransport
    expect(Renderer).toBeDefined()
    await act(async () => root.render(<svg><Renderer {...{
      id: 'edge-under-test', source: 'a', target: 'b', sourceX: 0, sourceY: 0, targetX: 100, targetY: 100,
      markerStart: 'url(#start)', markerEnd: 'url(#end)',
      data: { route: { points: [{ x: 0, y: 0 }, { x: 100, y: 0 }, { x: 100, y: 100 }] } },
    } as any} /></svg>))
    expect(container.querySelector('[data-testid="edge-under-test"]')).not.toBeNull()
    expect(container.querySelector('[marker-end], [marker-start], marker, .proxy-edge-arrow')).toBeNull()
  })
})
