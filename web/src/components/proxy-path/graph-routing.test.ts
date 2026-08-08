import { describe, expect, it } from 'vitest'
import { expandRect, routeIntersectsRect, routeOverlapLength } from './graph-geometry'
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

  it('separates both edges of a two-way branch immediately', () => {
    const input = autoGraph(['root', 'b', 'c'], [
      { id: 'root-b', source: 'root', target: 'b', pathIDs: [1] },
      { id: 'root-c', source: 'root', target: 'c', pathIDs: [2] },
    ])
    const result = expectClean(input)
    expect(routeOverlapLength(result.routes['root-b'].points, result.routes['root-c'].points)).toBe(0)
    expect(result.routes['root-b'].points[0]).not.toEqual(result.routes['root-c'].points[0])
  })

  it.each([8, 20, 47])('routes %i sibling branches with no collision, overlap, or crossing', count => {
    const children = Array.from({ length: count }, (_, index) => `child-${String(index).padStart(2, '0')}`)
    const edges = children.map((target, index) => ({ id: `edge-${String(index).padStart(2, '0')}`, source: 'root', target, pathIDs: [index + 1] }))
    const input = autoGraph(['root', ...children], edges)
    const result = expectClean(input)
    for (let left = 0; left < edges.length; left++) {
      for (let right = left + 1; right < edges.length; right++) {
        expect(routeOverlapLength(result.routes[edges[left].id].points, result.routes[edges[right].id].points)).toBe(0)
      }
    }
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
})
