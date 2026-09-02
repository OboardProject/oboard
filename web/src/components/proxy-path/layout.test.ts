import { describe, expect, it } from 'vitest'
import { GRAPH_ENTRY_NODE_WIDTH, GRAPH_SERVER_SLOT_WIDTH, ROUTING_MIN_CHANNEL_HEIGHT, defaultEntryGraphPosition, graphEntryHandleLeft, graphLayoutSignature, graphServerNodeWidth, layoutProxyGraphTopology, minimizeGraphLayerCrossings, snapDraggedGraphPosition, sortServerEntriesForGraph } from './layout'

describe('proxy graph server layout', () => {
  it('gives every server source one card-wide slot', () => {
    expect(graphServerNodeWidth(0)).toBe(GRAPH_SERVER_SLOT_WIDTH)
    expect(graphServerNodeWidth(1)).toBe(GRAPH_SERVER_SLOT_WIDTH)
    expect(graphServerNodeWidth(3)).toBe(GRAPH_SERVER_SLOT_WIDTH * 3)
    // A slot has to hold the card that hangs under it plus the sibling gap,
    // otherwise children cannot line up with their own handle.
    expect(GRAPH_SERVER_SLOT_WIDTH).toBeGreaterThanOrEqual(GRAPH_ENTRY_NODE_WIDTH)
  })

  it('centres each entry card on its own slot above the server', () => {
    const server = { x: 500, y: 300 }
    const width = graphServerNodeWidth(3)
    const cards = [0, 1, 2].map(index => defaultEntryGraphPosition(server, index, 3, width))
    const centres = cards.map(card => card.x + GRAPH_ENTRY_NODE_WIDTH / 2)

    expect(centres[1]).toBe(server.x + width / 2)
    expect(centres[1] - centres[0]).toBe(GRAPH_SERVER_SLOT_WIDTH)
    expect(centres[2] - centres[1]).toBe(GRAPH_SERVER_SLOT_WIDTH)
    cards.forEach(card => expect(card.y).toBe(130))
  })

  it('puts each entry card centre exactly over its own handle', () => {
    const server = { x: 500, y: 300 }
    const count = 4
    const width = graphServerNodeWidth(count)
    for (let index = 0; index < count; index++) {
      const handleX = server.x + width * (Number.parseFloat(graphEntryHandleLeft(index, count)) / 100)
      const cardCentre = defaultEntryGraphPosition(server, index, count, width).x + GRAPH_ENTRY_NODE_WIDTH / 2
      expect(cardCentre).toBe(handleX)
    }
  })

  it('spaces independent entry handles on slot centres', () => {
    expect(graphEntryHandleLeft(0, 1)).toBe('50%')
    expect(graphEntryHandleLeft(0, 2)).toBe('25%')
    expect(graphEntryHandleLeft(1, 2)).toBe('75%')
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
    expect(result.layerChannels[0].bottom - result.layerChannels[0].top).toBe(ROUTING_MIN_CHANNEL_HEIGHT)
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

describe('proxy graph layout signature', () => {
  it('ignores node and edge ordering', () => {
    const first = graphLayoutSignature(
      ['server-1', 'entry-2', 'proxy-server-step-9'],
      [{ source: 'server-1', target: 'proxy-server-step-9', sourceHandle: 'entry-2' }],
    )
    const second = graphLayoutSignature(
      ['proxy-server-step-9', 'server-1', 'entry-2'],
      [{ source: 'server-1', target: 'proxy-server-step-9', sourceHandle: 'entry-2' }],
    )
    expect(first).toBe(second)
  })

  it('changes when a hop is added or rewired but not when a card moves', () => {
    const nodes = ['server-1', 'proxy-server-step-9']
    const base = graphLayoutSignature(nodes, [{ source: 'server-1', target: 'proxy-server-step-9' }])

    expect(graphLayoutSignature(nodes, [{ source: 'server-1', target: 'proxy-server-step-9' }])).toBe(base)
    expect(graphLayoutSignature([...nodes, 'proxy-server-step-10'], [
      { source: 'server-1', target: 'proxy-server-step-9' },
      { source: 'proxy-server-step-9', target: 'proxy-server-step-10' },
    ])).not.toBe(base)
    expect(graphLayoutSignature(nodes, [{ source: 'server-1', target: 'proxy-server-step-9', sourceHandle: 'entry-2' }])).not.toBe(base)
  })
})

describe('drag snapping', () => {
  it('quantises pointer drops so aligned cards stay aligned', () => {
    expect(snapDraggedGraphPosition({ x: 631.4, y: 299.8 })).toEqual({ x: 632, y: 300 })
    expect(snapDraggedGraphPosition({ x: 632, y: 300 })).toEqual({ x: 632, y: 300 })
  })
})
