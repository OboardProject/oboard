import { describe, expect, it } from 'vitest'
import { GRAPH_ENTRY_NODE_WIDTH, defaultEntryGraphPosition, graphServerNodeWidth, layoutGraphLanes, minimizeGraphLayerCrossings } from './layout'

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

  it('keeps the primary continuation in its parent lane', () => {
    const positions = layoutGraphLanes(
      [
        [{ id: 'root', width: 260, terminal: false }],
        [
          { id: 'main', width: 260, terminal: false },
          { id: 'branch', width: 260, terminal: false },
        ],
        [
          { id: 'main-child', width: 260, terminal: true },
          { id: 'branch-child', width: 260, terminal: true },
        ],
      ],
      [
        { source: 'root', target: 'main' },
        { source: 'root', target: 'branch' },
        { source: 'main', target: 'main-child' },
        { source: 'branch', target: 'branch-child' },
      ],
      760,
      300,
      370,
    )

    expect(positions.root.x).toBe(positions.main.x)
    expect(positions.main.x).toBe(positions['main-child'].x)
    expect(positions.branch.x).toBe(positions['branch-child'].x)
    expect(positions.branch.x).toBeGreaterThan(positions.main.x)
  })
})
