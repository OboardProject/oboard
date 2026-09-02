import { describe, expect, it } from 'vitest'
import { expandRect, routeCrossingCount, routeIntersectsRect, routeOverlapLength } from './graph-geometry'
import { layoutProxyGraphTopology, type ProxyLayoutEdge, type ProxyLayoutNode } from './layout'
import {
  NODE_CLEARANCE,
  routeProxyGraph,
  type GraphRoutingInput,
  type RoutingEdge,
  type RoutingNode,
} from './graph-routing'

const WIDTH = 200
const HEIGHT = 100

function autoGraph(nodeIDs: string[], edges: ProxyLayoutEdge[], rootNodeID = 'root'): GraphRoutingInput {
  const layoutNodes: ProxyLayoutNode[] = nodeIDs.map(id => ({ id, width: WIDTH, height: HEIGHT }))
  const layout = layoutProxyGraphTopology(layoutNodes, edges, rootNodeID)
  const nodes: RoutingNode[] = layoutNodes.map(node => ({
    id: node.id,
    rect: {
      left: layout.positions[node.id].x,
      top: layout.positions[node.id].y,
      right: layout.positions[node.id].x + node.width,
      bottom: layout.positions[node.id].y + node.height,
    },
  }))
  const routingEdges: RoutingEdge[] = edges.map(edge => ({
    ...edge,
    routingClass: 'primary',
    sourceRank: layout.ranks[edge.source],
    targetRank: layout.ranks[edge.target],
  }))
  return { nodes, edges: routingEdges, branchBands: layout.bands, layerChannels: layout.layerChannels }
}

function expectClean(input: GraphRoutingInput) {
  const result = routeProxyGraph(input)
  expect(result.diagnostics.failedEdgeIDs).toEqual([])
  expect(result.diagnostics.nodeIntersectionEdgeIDs).toEqual([])
  expect(result.diagnostics.overlappingEdgePairs).toEqual([])
  expect(result.diagnostics.crossingPrimaryPairs).toEqual([])
  return result
}

describe('global proxy graph routing', () => {
  it('routes a simple chain as vertical segments', () => {
    const input = autoGraph(['root', 'b', 'c'], [
      { id: 'root-b', source: 'root', target: 'b', pathIDs: [1] },
      { id: 'b-c', source: 'b', target: 'c', pathIDs: [1] },
    ])
    const result = expectClean(input)
    Object.values(result.routes).forEach(route => {
      expect(route.points.every(point => point.x === route.points[0].x)).toBe(true)
    })
  })

  it('fans sibling branches from one shared stub then left and right', () => {
    const input = autoGraph(['root', 'b', 'c'], [
      { id: 'root-b', source: 'root', target: 'b', pathIDs: [1] },
      { id: 'root-c', source: 'root', target: 'c', pathIDs: [2] },
    ])
    const result = expectClean(input)
    const left = result.routes['root-b']
    const right = result.routes['root-c']
    expect(left.points[0]).toEqual(right.points[0])
    expect(left.points[1].x).toBe(left.points[0].x)
    expect(left.points[1].y).toBeGreaterThan(left.points[0].y)
    expect(right.points[1].x).toBe(right.points[0].x)
    expect(right.points[1].y).toBeGreaterThan(right.points[0].y)
    expect(left.quality).toBe('preferred')
    expect(right.quality).toBe('preferred')
    expect(routeOverlapLength(left.points, right.points)).toBeGreaterThan(0)
  })

  it.each([8, 20, 47])('routes %i sibling branches from one stub without collisions or crossings', count => {
    const children = Array.from({ length: count }, (_, index) => `child-${String(index).padStart(2, '0')}`)
    const edges = children.map((target, index) => ({ id: `edge-${String(index).padStart(2, '0')}`, source: 'root', target, pathIDs: [index + 1] }))
    const input = autoGraph(['root', ...children], edges)
    const result = expectClean(input)
    const origin = result.routes[edges[0].id].points[0]
    edges.forEach(edge => {
      expect(result.routes[edge.id].quality).toBe('preferred')
      expect(result.routes[edge.id].points[0]).toEqual(origin)
      expect(result.routes[edge.id].points[1].x).toBe(origin.x)
      expect(result.routes[edge.id].points[1].y).toBeGreaterThan(origin.y)
    })
  })

  it('keeps nested subtrees geometrically independent', () => {
    const edges: ProxyLayoutEdge[] = [
      ['root-a', 'root', 'a', 1], ['root-b', 'root', 'b', 2], ['root-c', 'root', 'c', 3],
      ['a-d', 'a', 'd', 1], ['a-e', 'a', 'e', 4], ['a-f', 'a', 'f', 5],
      ['b-g', 'b', 'g', 2], ['b-h', 'b', 'h', 6], ['c-i', 'c', 'i', 3],
      ['d-j', 'd', 'j', 1], ['e-k', 'e', 'k', 4], ['g-l', 'g', 'l', 2],
    ].map(([id, source, target, pathID]) => ({ id: String(id), source: String(source), target: String(target), pathIDs: [Number(pathID)] }))
    expectClean(autoGraph(['root', 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l'], edges))
  })

  it('is deterministic for identical graph input', () => {
    const input = autoGraph(['root', 'a', 'b', 'c'], [
      { id: 'root-a', source: 'root', target: 'a', pathIDs: [1] },
      { id: 'root-b', source: 'root', target: 'b', pathIDs: [2] },
      { id: 'root-c', source: 'root', target: 'c', pathIDs: [3] },
    ])
    expect(routeProxyGraph(input)).toEqual(routeProxyGraph(input))
  })

  it('uses A* after a manual move blocks the preferred channel', () => {
    const input: GraphRoutingInput = {
      nodes: [
        { id: 'source', rect: { left: 0, top: 0, right: 200, bottom: 100 } },
        { id: 'blocker', rect: { left: 230, top: 130, right: 330, bottom: 260 } },
        { id: 'target', rect: { left: 500, top: 300, right: 700, bottom: 400 } },
      ],
      edges: [{ id: 'edge', source: 'source', target: 'target', routingClass: 'primary', pathIDs: [1], sourceRank: 0, targetRank: 1 }],
      layerChannels: [{ sourceRank: 0, targetRank: 1, top: 180, bottom: 220, tracks: { edge: 190 } }],
    }
    const result = expectClean(input)
    expect(result.routes.edge.quality).toBe('pathfinder')
    expect(routeIntersectsRect(result.routes.edge.points, expandRect(input.nodes[1].rect, NODE_CLEARANCE))).toBe(false)
  })

  it('reaches a side-shifted child without routing over the graph', () => {
    const input: GraphRoutingInput = {
      nodes: [
        { id: 'source', rect: { left: 0, top: 0, right: 200, bottom: 100 } },
        { id: 'below', rect: { left: -40, top: 220, right: 160, bottom: 320 } },
        { id: 'side', rect: { left: 520, top: 40, right: 720, bottom: 160 } },
      ],
      edges: [
        { id: 'to-below', source: 'source', target: 'below', routingClass: 'primary', pathIDs: [1], sourceRank: 0, targetRank: 1 },
        { id: 'to-side', source: 'source', target: 'side', routingClass: 'primary', pathIDs: [2], sourceRank: 0, targetRank: 1 },
      ],
    }
    const result = expectClean(input)
    const side = result.routes['to-side']
    expect(side.quality).toBe('preferred')
    expect(Math.min(...side.points.map(point => point.y))).toBeGreaterThanOrEqual(40)
    expect(side.points[0]).toEqual(result.routes['to-below'].points[0])
    expect(side.points[1].x).toBe(side.points[0].x)
    expect(side.points[1].y).toBeGreaterThan(side.points[0].y)
    expect(routeIntersectsRect(side.points, input.nodes[0].rect)).toBe(false)
    expect(routeIntersectsRect(side.points, input.nodes[2].rect)).toBe(false)
  })

  it('routes auxiliary infrastructure after the primary tree through an outer gutter', () => {
    const primaryInput = autoGraph(['root', 'a', 'b'], [
      { id: 'root-a', source: 'root', target: 'a', pathIDs: [1] },
      { id: 'root-b', source: 'root', target: 'b', pathIDs: [2] },
    ])
    primaryInput.nodes.push({ id: 'infrastructure', rect: { left: 1600, top: 500, right: 1800, bottom: 600 } })
    primaryInput.edges.push({ id: 'tunnel', source: 'a', target: 'infrastructure', routingClass: 'auxiliary', pathIDs: [] })
    const result = expectClean(primaryInput)
    expect(result.routes.tunnel.quality).toBe('outer-gutter')
    expect(result.routes.tunnel.points.some(point => point.y >= 680)).toBe(true)
  })

  it('routes multiple belongs entry edges to the same server cleanly with dedicated tracks', () => {
    const input: GraphRoutingInput = {
      nodes: [
        { id: 'entry-1', rect: { left: 100, top: 50, right: 300, bottom: 180 } },
        { id: 'entry-2', rect: { left: 350, top: 50, right: 550, bottom: 180 } },
        {
          id: 'server-1',
          rect: { left: 200, top: 300, right: 460, bottom: 480 },
          handles: {
            'target-1': { x: 260, y: 300 },
            'target-2': { x: 400, y: 300 },
          },
        },
      ],
      edges: [
        { id: 'belongs-1', source: 'entry-1', target: 'server-1', targetHandle: 'target-1', routingClass: 'belongs', pathIDs: [] },
        { id: 'belongs-2', source: 'entry-2', target: 'server-1', targetHandle: 'target-2', routingClass: 'belongs', pathIDs: [] },
      ],
    }
    const result = expectClean(input)
    expect(result.routes['belongs-1'].quality).toBe('preferred')
    expect(result.routes['belongs-2'].quality).toBe('preferred')
    expect(result.routes['belongs-1'].points.every(p => p.y <= 300)).toBe(true)
    expect(result.routes['belongs-2'].points.every(p => p.y <= 300)).toBe(true)
    expect(routeCrossingCount(result.routes['belongs-1'].points, result.routes['belongs-2'].points)).toBe(0)
  })

  it('draws a straight line when a dragged card is only a few pixels off centre', () => {
    const input: GraphRoutingInput = {
      nodes: [
        { id: 'source', rect: { left: 0, top: 0, right: 200, bottom: 100 } },
        { id: 'target', rect: { left: 3, top: 300, right: 203, bottom: 400 } },
      ],
      edges: [{ id: 'edge', source: 'source', target: 'target', routingClass: 'primary', pathIDs: [1], sourceRank: 0, targetRank: 1 }],
    }
    const result = expectClean(input)
    expect(result.routes.edge.points).toHaveLength(2)
    expect(result.routes.edge.points[0].x).toBe(result.routes.edge.points[1].x)
  })

  it('crosses over halfway down for a lone branch instead of hugging the source card', () => {
    const input: GraphRoutingInput = {
      nodes: [
        { id: 'source', rect: { left: 0, top: 0, right: 200, bottom: 100 } },
        { id: 'target', rect: { left: 400, top: 400, right: 600, bottom: 500 } },
      ],
      edges: [{ id: 'edge', source: 'source', target: 'target', routingClass: 'primary', pathIDs: [1], sourceRank: 0, targetRank: 1 }],
    }
    const result = expectClean(input)
    const [start, corner] = result.routes.edge.points
    expect(result.routes.edge.quality).toBe('preferred')
    expect(corner.y - start.y).toBeGreaterThan(100)
  })

  it('skips obstacle avoidance and diagnostics in fast mode', () => {
    const input: GraphRoutingInput = {
      nodes: [
        { id: 'source', rect: { left: 0, top: 0, right: 200, bottom: 100 } },
        { id: 'blocker', rect: { left: 230, top: 130, right: 330, bottom: 260 } },
        { id: 'target', rect: { left: 500, top: 300, right: 700, bottom: 400 } },
      ],
      edges: [{ id: 'edge', source: 'source', target: 'target', routingClass: 'primary', pathIDs: [1], sourceRank: 0, targetRank: 1 }],
      layerChannels: [{ sourceRank: 0, targetRank: 1, top: 180, bottom: 220, tracks: { edge: 190 } }],
    }
    const fast = routeProxyGraph({ ...input, avoidObstacles: false, collectDiagnostics: false })
    expect(fast.routes.edge.quality).toBe('preferred')
    expect(fast.diagnostics.failedEdgeIDs).toEqual([])
    expect(fast.diagnostics.nodeIntersectionEdgeIDs).toEqual([])
    // The full pass still detours around the blocker the fast pass ignores.
    expect(routeProxyGraph(input).routes.edge.quality).toBe('pathfinder')
  })
})
