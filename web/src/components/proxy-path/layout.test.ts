import { describe, expect, it } from 'vitest'
import { GRAPH_ENTRY_NODE_WIDTH, defaultEntryGraphPosition, graphEntryHandleLeft, graphServerNodeWidth, layoutProxyGraphTopology, minimizeGraphLayerCrossings } from './layout'

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

  it('keeps independent entry handles apart from the batch handle', () => {
    expect(graphEntryHandleLeft(0, 2, true)).toBe('21%')
    expect(graphEntryHandleLeft(1, 2, true)).toBe('79%')
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
})
