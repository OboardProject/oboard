import { describe, expect, it } from 'vitest'
import { GRAPH_ENTRY_NODE_WIDTH, defaultEntryGraphPosition, graphEntryHandleLeft, graphServerNodeWidth, layoutProxyGraphTopology, minimizeGraphLayerCrossings, sortServerEntriesForGraph } from './layout'

describe('proxy graph server layout', () => {
  it('keeps server cards fixed width regardless of inbound count', () => {
    expect(graphServerNodeWidth(0)).toBe(GRAPH_ENTRY_NODE_WIDTH)
    expect(graphServerNodeWidth(1)).toBe(GRAPH_ENTRY_NODE_WIDTH)
    expect(graphServerNodeWidth(8)).toBe(GRAPH_ENTRY_NODE_WIDTH)
  })

  it('fans multiple entry cards around the fixed-width server center', () => {
    const server = { x: 500, y: 300 }
    const left = defaultEntryGraphPosition(server, 0, 3)
    const middle = defaultEntryGraphPosition(server, 1, 3)
    const right = defaultEntryGraphPosition(server, 2, 3)

    expect(middle.x).toBe(server.x)
    expect(left.x).toBeLessThan(middle.x)
    expect(right.x).toBeGreaterThan(middle.x)
    expect(left.y).toBe(130)
    expect(right.y).toBe(130)
  })

  it('spaces independent entry handles evenly across the card width', () => {
    expect(graphEntryHandleLeft(0, 2)).toBe('20%')
    expect(graphEntryHandleLeft(1, 2)).toBe('80%')
  })

  it('orders server handles by entry card position instead of port', () => {
    const server = { x: 500, y: 300 }
    const vless = { id: 10, port: 10777 }
    const mieru = { id: 11, port: 4101 }
    const saved = {
      'entry-10': { x: 200, y: 130 },
      'entry-11': { x: 500, y: 130 },
    }

    expect(sortServerEntriesForGraph([vless, mieru], saved, server).map(entry => entry.id)).toEqual([10, 11])
    expect(sortServerEntriesForGraph([vless, mieru], {}, server).map(entry => entry.id)).toEqual([11, 10])
  })

  it('orders child branches to match their parent order', () => {
    const layers = minimizeGraphLayerCrossings(
      [
        ['root'],
        ['left-parent', 'right-parent'],
        ['left-child', 'right-child'],
      ],
      [
        { source: 'root', target: 'left-parent' },
        { source: 'root', target: 'right-parent' },
        { source: 'left-parent', target: 'right-child' },
        { source: 'right-parent', target: 'left-child' },
      ],
      (left, right) => left.localeCompare(right),
    )

    expect(layers[1]).toEqual(['left-parent', 'right-parent'])
    expect(layers[2]).toEqual(['right-child', 'left-child'])
  })

  it('keeps every primary sibling on one rank without wrapping', () => {
    const children = Array.from({ length: 20 }, (_, index) => ({ id: `child-${index}`, width: 260, height: 220 }))
    const result = layoutProxyGraphTopology(
      [{ id: 'root', width: 260, height: 220 }, ...children],
      children.map((child, index) => ({ id: `edge-${index}`, source: 'root', target: child.id, pathIDs: [index + 1] })),
      'root',
    )
    const childY = children.map(child => result.positions[child.id].y)
    expect(new Set(childY).size).toBe(1)
    expect(result.bands['child-19'].left).toBeGreaterThan(result.bands['child-0'].right)
    expect(result.layerChannels[0].bottom - result.layerChannels[0].top).toBeGreaterThan(120)
  })

  it('uses longest-path ranks and subtree bands deterministically', () => {
    const nodes = ['root', 'a', 'b', 'c', 'd'].map(id => ({ id, width: 260, height: 220 }))
    const edges = [
      { id: 'root-a', source: 'root', target: 'a', pathIDs: [1, 2] },
      { id: 'a-b', source: 'a', target: 'b', pathIDs: [1] },
      { id: 'a-c', source: 'a', target: 'c', pathIDs: [2] },
      { id: 'b-d', source: 'b', target: 'd', pathIDs: [1] },
    ]
    const first = layoutProxyGraphTopology(nodes, edges, 'root')
    const second = layoutProxyGraphTopology(nodes, edges, 'root')
    expect(first).toEqual(second)
    expect(first.ranks).toMatchObject({ root: 0, a: 1, b: 2, c: 2, d: 3 })
    expect(first.bands.b.right).toBeLessThan(first.bands.c.left)
    expect(first.positions.root.x + 130).toBe(first.bands.root.centerX)
  })

  it('aligns a single child branch directly under its source handle without bending', () => {
    const parentWidth = 260
    const childWidth = 240
    const handleOffset = 208 // 80% handle position
    const nodes = [
      { id: 'server', width: parentWidth, height: 180, handles: { 'entry-2': { x: handleOffset, y: 180 } } },
      { id: 'routing', width: childWidth, height: 120 },
    ]
    const edges = [
      { id: 'server-routing', source: 'server', target: 'routing', sourceHandle: 'entry-2', pathIDs: [1] },
    ]
    const result = layoutProxyGraphTopology(nodes, edges, 'server')
    const serverLeft = result.positions.server.x
    const routingLeft = result.positions.routing.x
    // The handle on the server is at: serverLeft + handleOffset
    const sourceHandleX = serverLeft + handleOffset
    // The target handle on routing node is at: routingLeft + childWidth / 2
    const targetHandleX = routingLeft + childWidth / 2
    expect(targetHandleX).toBe(sourceHandleX)
  })
})
