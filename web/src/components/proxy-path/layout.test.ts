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

  it('stagger-compacts wide layers without changing their visual order', () => {
    const children = Array.from({ length: 5 }, (_, index) => ({
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

    const childPositions = children.map(child => positions[child.id])
    expect(new Set(childPositions.map(position => position.y))).toEqual(new Set([670, 860]))
    expect(childPositions.map(position => position.x)).toEqual(
      childPositions.map(position => position.x).slice().sort((left, right) => left - right),
    )
    expect(Math.max(...childPositions.map(position => position.x + 260)) - Math.min(...childPositions.map(position => position.x))).toBe(980)
    expect(positions['child-3'].x).toBe(positions.root.x)
  })

  it('offsets equal compact rows so incoming edges do not pass through sibling nodes', () => {
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

    const upperCenters = children.filter((_, index) => index % 2 === 0).map(child => positions[child.id].x + child.width / 2)
    const lowerCenters = children.filter((_, index) => index % 2 === 1).map(child => positions[child.id].x + child.width / 2)
    expect(lowerCenters.every(center => !upperCenters.includes(center))).toBe(true)
    expect(positions.root.x + 130).toBe(760)
  })
})
