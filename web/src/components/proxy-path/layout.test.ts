import { describe, expect, it } from 'vitest'
import { GRAPH_ENTRY_NODE_WIDTH, defaultEntryGraphPosition, graphEntryHandleLeft, graphServerNodeWidth, layoutGraphLanes, minimizeGraphLayerCrossings } from './layout'

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

  it('keeps chain nodes on the upper row and packs terminal exits below', () => {
    const positions = layoutGraphLanes(
      [
        [{ id: 'root', width: 260, terminal: false }],
        [
          { id: 'exit-1', width: 260, terminal: true },
          { id: 'relay-a', width: 260, terminal: false },
          { id: 'exit-2', width: 260, terminal: true },
          { id: 'relay-b', width: 260, terminal: false },
          { id: 'exit-3', width: 260, terminal: true },
        ],
        [
          { id: 'leaf-a', width: 260, terminal: true },
          { id: 'leaf-b', width: 260, terminal: true },
        ],
      ],
      [
        { source: 'root', target: 'exit-1' },
        { source: 'root', target: 'relay-a' },
        { source: 'root', target: 'exit-2' },
        { source: 'root', target: 'relay-b' },
        { source: 'root', target: 'exit-3' },
        { source: 'relay-a', target: 'leaf-a' },
        { source: 'relay-b', target: 'leaf-b' },
      ],
      760,
      300,
      370,
    )

    expect(positions['relay-a'].y).toBe(670)
    expect(positions['relay-b'].y).toBe(670)
    expect(positions['exit-1'].y).toBe(860)
    expect(positions['exit-2'].y).toBe(860)
    expect(positions['exit-3'].y).toBe(860)
    expect(positions['relay-b'].x).toBeGreaterThan(positions['relay-a'].x)
    expect(positions['exit-2'].x).toBeGreaterThan(positions['exit-1'].x)
    expect(positions['exit-3'].x).toBeGreaterThan(positions['exit-2'].x)
  })

  it('splits all-terminal wide layers contiguously with a half-column offset', () => {
    const children = Array.from({ length: 6 }, (_, index) => ({
      id: `child-${index + 1}`,
      width: 260,
      terminal: true,
    }))
    const positions = layoutGraphLanes(
      [
        [{ id: 'root', width: 260, terminal: false }],
        children,
      ],
      children.map(child => ({ source: 'root', target: child.id })),
      760,
      300,
      370,
    )

    const upper = children.slice(0, 3).map(child => positions[child.id])
    const lower = children.slice(3).map(child => positions[child.id])
    expect(upper.every(position => position.y === 670)).toBe(true)
    expect(lower.every(position => position.y === 860)).toBe(true)
    expect(upper.map(position => position.x)).toEqual(upper.map(position => position.x).slice().sort((left, right) => left - right))
    expect(lower.map(position => position.x)).toEqual(lower.map(position => position.x).slice().sort((left, right) => left - right))
    const upperCenters = upper.map(position => position.x + 130)
    const lowerCenters = lower.map(position => position.x + 130)
    expect(lowerCenters.every(center => !upperCenters.includes(center))).toBe(true)
    const singleRowWidth = 6 * 260 + 5 * 100
    const compactWidth = Math.max(...children.map(child => positions[child.id].x + 260)) - Math.min(...children.map(child => positions[child.id].x))
    expect(compactWidth).toBeLessThan(singleRowWidth)
  })
})
