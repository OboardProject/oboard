import {
  collinearOverlapLength,
  expandRect,
  normalizeOrthogonalPoints,
  orthogonalSegment,
  routeCrossingCount,
  routeIntersectsRect,
  routeOverlapLength,
  routeSegments,
  routingCoordinate,
  segmentAxis,
  segmentIntersectsRectInterior,
  segmentLength,
  segmentsCross,
  type GraphAxis,
  type GraphPoint,
  type GraphRect,
  type GraphSegment,
} from './graph-geometry'
import type { GraphBranchBand, GraphLayerChannel } from './layout'

export type GraphRoutingClass = 'primary' | 'auxiliary' | 'belongs'
export type GraphRouteQuality = 'preferred' | 'pathfinder' | 'outer-gutter' | 'failed'

export type GraphEdgeRoute = {
  points: GraphPoint[]
  labelPoint?: GraphPoint
  quality: GraphRouteQuality
}

export type GraphRoutingEdgeData = {
  routingClass?: GraphRoutingClass
  route?: GraphEdgeRoute
  auxiliary?: boolean
}

export type RoutingNode = {
  id: string
  rect: GraphRect
  handles?: Record<string, GraphPoint>
}

export type RoutingEdge = {
  id: string
  source: string
  target: string
  sourceHandle?: string
  targetHandle?: string
  routingClass: GraphRoutingClass
  pathIDs: number[]
  sourceRank?: number
  targetRank?: number
}

export type GraphRoutingInput = {
  nodes: RoutingNode[]
  edges: RoutingEdge[]
  branchBands?: Record<string, GraphBranchBand>
  layerChannels?: GraphLayerChannel[]
  avoidObstacles?: boolean
  /** Cross-checking every routed pair is O(E²) segment intersection. Callers on
   *  a render hot path opt out; tests and dev builds keep it on. */
  collectDiagnostics?: boolean
}

export type GraphRoutingDiagnostics = {
  failedEdgeIDs: string[]
  nodeIntersectionEdgeIDs: string[]
  overlappingEdgePairs: Array<[string, string]>
  crossingPrimaryPairs: Array<[string, string]>
}

export type GraphRoutingResult = {
  routes: Record<string, GraphEdgeRoute>
  diagnostics: GraphRoutingDiagnostics
}

export type GraphRoutePort = {
  edgeID: string
  nodeID: string
  side: 'top' | 'bottom' | 'left' | 'right'
  point: GraphPoint
}

export type EdgeReservation = {
  edgeID: string
  routingClass: GraphRoutingClass
  axis: GraphAxis
  fixed: number
  start: number
  end: number
}

export type RouteCandidateValidation = {
  valid: boolean
  nodeIntersections: string[]
  overlapLength: number
  crossings: number
}

export const NODE_CLEARANCE = 16
export const PORT_MARGIN = 16
export const ROUTING_TRACK_GAP = 14
export const BRANCH_STUB_LENGTH = 28
export const OUTER_GUTTER = 60
export const OUTER_TRACK_GAP = 16
export const BEND_COST = 120
export const CROSSING_COST = 5000
export const NODE_PROXIMITY_COST = 6
export const ABOVE_SOURCE_COST = 4000
/** Cards that read as vertically aligned are rarely aligned to the half-pixel.
 *  Within this tolerance the edge is drawn as one straight segment instead of
 *  degenerating into a stub, a few-pixel jog and a second stub. */
export const STRAIGHT_SNAP_TOLERANCE = 6

function sortedPair(left: string, right: string): [string, string] {
  return left < right ? [left, right] : [right, left]
}

function nodeCenter(rect: GraphRect): GraphPoint {
  return {
    x: routingCoordinate((rect.left + rect.right) / 2),
    y: routingCoordinate((rect.top + rect.bottom) / 2),
  }
}

function reservationFor(edgeID: string, routingClass: GraphRoutingClass, segment: GraphSegment): EdgeReservation | undefined {
  if (!segmentLength(segment)) return undefined
  const axis = segmentAxis(segment)
  return axis === 'horizontal'
    ? {
        edgeID,
        routingClass,
        axis,
        fixed: routingCoordinate(segment.from.y),
        start: Math.min(segment.from.x, segment.to.x),
        end: Math.max(segment.from.x, segment.to.x),
      }
    : {
        edgeID,
        routingClass,
        axis,
        fixed: routingCoordinate(segment.from.x),
        start: Math.min(segment.from.y, segment.to.y),
        end: Math.max(segment.from.y, segment.to.y),
      }
}

export class EdgeReservationIndex {
  private horizontalByY = new Map<number, EdgeReservation[]>()
  private verticalByX = new Map<number, EdgeReservation[]>()
  readonly allHorizontal: EdgeReservation[] = []
  readonly allVertical: EdgeReservation[] = []

  reserve(edgeID: string, routingClass: GraphRoutingClass, points: GraphPoint[]) {
    routeSegments(points).forEach(segment => {
      const reservation = reservationFor(edgeID, routingClass, segment)
      if (!reservation) return
      const byFixed = reservation.axis === 'horizontal' ? this.horizontalByY : this.verticalByX
      const all = reservation.axis === 'horizontal' ? this.allHorizontal : this.allVertical
      byFixed.set(reservation.fixed, [...(byFixed.get(reservation.fixed) || []), reservation])
      all.push(reservation)
    })
  }

  overlapLength(segment: GraphSegment, ignoredEdgeIDs?: string | Iterable<string>) {
    const ignored = ignoredSet(ignoredEdgeIDs)
    const normalized = orthogonalSegment(segment.from, segment.to)
    const axis = segmentAxis(normalized)
    const fixed = axis === 'horizontal' ? normalized.from.y : normalized.from.x
    const candidates = (axis === 'horizontal' ? this.horizontalByY : this.verticalByX).get(fixed) || []
    return candidates.reduce((total, reservation) => {
      if (ignored.has(reservation.edgeID)) return total
      const reservedSegment = axis === 'horizontal'
        ? orthogonalSegment({ x: reservation.start, y: fixed }, { x: reservation.end, y: fixed })
        : orthogonalSegment({ x: fixed, y: reservation.start }, { x: fixed, y: reservation.end })
      return total + collinearOverlapLength(normalized, reservedSegment)
    }, 0)
  }

  crossingCount(segment: GraphSegment, ignoredEdgeIDs?: string | Iterable<string>) {
    const ignored = ignoredSet(ignoredEdgeIDs)
    const normalized = orthogonalSegment(segment.from, segment.to)
    const opposite = segmentAxis(normalized) === 'horizontal' ? this.allVertical : this.allHorizontal
    return opposite.filter(reservation => {
      if (ignored.has(reservation.edgeID)) return false
      const reservedSegment = reservation.axis === 'horizontal'
        ? orthogonalSegment({ x: reservation.start, y: reservation.fixed }, { x: reservation.end, y: reservation.fixed })
        : orthogonalSegment({ x: reservation.fixed, y: reservation.start }, { x: reservation.fixed, y: reservation.end })
      return segmentsCross(normalized, reservedSegment)
    }).length
  }
}

function ignoredSet(ignoredEdgeIDs?: string | Iterable<string>) {
  if (!ignoredEdgeIDs) return new Set<string>()
  return typeof ignoredEdgeIDs === 'string' ? new Set([ignoredEdgeIDs]) : new Set(ignoredEdgeIDs)
}

function branchGroupKey(edge: RoutingEdge) {
  if (edge.routingClass !== 'primary') return edge.id
  return `${edge.source}\u001f${edge.sourceHandle || ''}`
}

function branchSiblingIDs(edge: RoutingEdge, edges: RoutingEdge[]) {
  const key = branchGroupKey(edge)
  return new Set(edges.filter(item => item.id === edge.id || branchGroupKey(item) === key).map(item => item.id))
}

function edgeSide(routingClass: GraphRoutingClass, endpoint: 'source' | 'target') {
  if (routingClass === 'auxiliary') return 'bottom' as const
  return endpoint === 'source' ? 'bottom' as const : 'top' as const
}

function allocateSidePorts(
  edges: RoutingEdge[],
  nodeByID: Map<string, RoutingNode>,
  endpoint: 'source' | 'target',
): Map<string, GraphRoutePort> {
  const byNodeAndSide = new Map<string, RoutingEdge[]>()
  edges.forEach(edge => {
    const nodeID = edge[endpoint]
    const side = edgeSide(edge.routingClass, endpoint)
    const key = `${nodeID}\u001f${side}`
    byNodeAndSide.set(key, [...(byNodeAndSide.get(key) || []), edge])
  })
  const ports = new Map<string, GraphRoutePort>()
  Array.from(byNodeAndSide.entries()).sort(([left], [right]) => left.localeCompare(right)).forEach(([, groupedEdges]) => {
    const first = groupedEdges[0]
    const nodeID = first[endpoint]
    const node = nodeByID.get(nodeID)
    if (!node) return
    const side = edgeSide(first.routingClass, endpoint)
    const oppositeEndpoint = endpoint === 'source' ? 'target' : 'source'
    const handleKey = endpoint === 'source' ? 'sourceHandle' : 'targetHandle'

    const unhandledEdges: RoutingEdge[] = []
    groupedEdges.forEach(edge => {
      const specifiedHandle = edge[handleKey]
      const handlePoint = specifiedHandle ? node.handles?.[specifiedHandle] : undefined
      if (handlePoint) {
        ports.set(edge.id, { edgeID: edge.id, nodeID, side, point: { x: routingCoordinate(handlePoint.x), y: routingCoordinate(handlePoint.y) } })
      } else {
        unhandledEdges.push(edge)
      }
    })

    if (!unhandledEdges.length) return

    const shareCenter = endpoint === 'source' && (side === 'top' || side === 'bottom')
    const sharedEdges = shareCenter ? unhandledEdges.filter(edge => edge.routingClass === 'primary') : []
    const spreadEdges = shareCenter ? unhandledEdges.filter(edge => edge.routingClass !== 'primary') : unhandledEdges
    if (sharedEdges.length) {
      const point = {
        x: routingCoordinate((node.rect.left + node.rect.right) / 2),
        y: routingCoordinate(side === 'top' ? node.rect.top : node.rect.bottom),
      }
      sharedEdges.forEach(edge => ports.set(edge.id, { edgeID: edge.id, nodeID, side, point }))
    }
    if (!spreadEdges.length) return

    const ordered = spreadEdges.slice().sort((left, right) => {
      const leftNode = nodeByID.get(left[oppositeEndpoint])
      const rightNode = nodeByID.get(right[oppositeEndpoint])
      const leftCenter = leftNode ? nodeCenter(leftNode.rect) : { x: 0, y: 0 }
      const rightCenter = rightNode ? nodeCenter(rightNode.rect) : { x: 0, y: 0 }
      const primary = side === 'top' || side === 'bottom' ? leftCenter.x - rightCenter.x : leftCenter.y - rightCenter.y
      return primary || left.id.localeCompare(right.id)
    })
    ordered.forEach((edge, index) => {
      const count = ordered.length
      const horizontal = side === 'top' || side === 'bottom'
      const minimum = horizontal ? node.rect.left + PORT_MARGIN : node.rect.top + PORT_MARGIN
      const maximum = horizontal ? node.rect.right - PORT_MARGIN : node.rect.bottom - PORT_MARGIN
      const variable = count === 1 ? (minimum + maximum) / 2 : minimum + (index / (count - 1)) * (maximum - minimum)
      const point = horizontal
        ? { x: routingCoordinate(variable), y: routingCoordinate(side === 'top' ? node.rect.top : node.rect.bottom) }
        : { x: routingCoordinate(side === 'left' ? node.rect.left : node.rect.right), y: routingCoordinate(variable) }
      ports.set(edge.id, { edgeID: edge.id, nodeID, side, point })
    })
  })
  return ports
}

function graphBounds(nodes: RoutingNode[]): GraphRect {
  if (!nodes.length) return { left: 0, top: 0, right: 0, bottom: 0 }
  return {
    left: Math.min(...nodes.map(node => node.rect.left)),
    top: Math.min(...nodes.map(node => node.rect.top)),
    right: Math.max(...nodes.map(node => node.rect.right)),
    bottom: Math.max(...nodes.map(node => node.rect.bottom)),
  }
}

function routeObstacles(edge: RoutingEdge, nodes: RoutingNode[]) {
  return nodes.map(node => ({
    id: node.id,
    rect: node.id === edge.source || node.id === edge.target ? node.rect : expandRect(node.rect, NODE_CLEARANCE),
  }))
}

export function validateRouteCandidate(
  edge: RoutingEdge,
  points: GraphPoint[],
  nodes: RoutingNode[],
  reservations: EdgeReservationIndex,
  ignoredEdgeIDs?: Iterable<string>,
): RouteCandidateValidation {
  let route: GraphPoint[]
  try {
    route = normalizeOrthogonalPoints(points)
  } catch {
    return { valid: false, nodeIntersections: [], overlapLength: Number.POSITIVE_INFINITY, crossings: 0 }
  }
  const ignored = ignoredSet(ignoredEdgeIDs ?? edge.id)
  const nodeIntersections = routeObstacles(edge, nodes)
    .filter(obstacle => routeIntersectsRect(route, obstacle.rect))
    .map(obstacle => obstacle.id)
  const overlapLength = routeSegments(route).reduce((total, segment) => total + reservations.overlapLength(segment, ignored), 0)
  const crossings = routeSegments(route).reduce((total, segment) => total + reservations.crossingCount(segment, ignored), 0)
  const allowCrossings = edge.routingClass === 'belongs' || edge.routingClass === 'auxiliary'
  return {
    valid: nodeIntersections.length === 0 && overlapLength === 0 && (allowCrossings || crossings === 0),
    nodeIntersections,
    overlapLength,
    crossings,
  }
}

function primarySideApproachX(source: GraphPoint, target: GraphPoint, targetRect: GraphRect) {
  const goingRight = target.x >= source.x
  const sideX = goingRight
    ? routingCoordinate(targetRect.left - NODE_CLEARANCE)
    : routingCoordinate(targetRect.right + NODE_CLEARANCE)
  if ((goingRight && sideX >= target.x) || (!goingRight && sideX <= target.x)) return target.x
  return sideX
}

function preferredRoute(
  edge: RoutingEdge,
  sourcePort: GraphRoutePort,
  targetPort: GraphRoutePort,
  input: GraphRoutingInput,
  outerLaneIndex: number,
  belongsTracks?: Map<string, number>,
  busYByGroup?: Map<string, number>,
  sharedBusGroups?: Set<string>,
): GraphPoint[] {
  const rawSource = sourcePort.point
  const target = targetPort.point
  // Snapping the port instead of inserting a jog keeps the stroke inside the
  // card it leaves from while removing the visual kink entirely.
  const source = Math.abs(rawSource.x - target.x) <= STRAIGHT_SNAP_TOLERANCE
    ? { x: target.x, y: rawSource.y }
    : rawSource
  if (edge.routingClass === 'auxiliary') {
    const bounds = graphBounds(input.nodes)
    const y = routingCoordinate(bounds.bottom + OUTER_GUTTER + outerLaneIndex * OUTER_TRACK_GAP)
    return normalizeOrthogonalPoints([source, { x: source.x, y }, { x: target.x, y }, target])
  }
  if (source.x === target.x) return [source, target]
  if (edge.routingClass === 'belongs') {
    const trackY = belongsTracks?.get(edge.id)
      ?? routingCoordinate(source.y + (target.y - source.y) / 2)
    return normalizeOrthogonalPoints([source, { x: source.x, y: trackY }, { x: target.x, y: trackY }, target])
  }
  const stubY = routingCoordinate(source.y + BRANCH_STUB_LENGTH)
  const groupKey = branchGroupKey(edge)
  const dropY = busYByGroup?.get(groupKey) ?? stubY
  // Siblings leaving the same handle must share one bus lane so they can fan
  // out together. A lone branch has nothing to share with, so its crossover
  // sits halfway down the gap and reads as a symmetric S instead of a corner
  // pinned against the source card.
  const midY = routingCoordinate(source.y + (target.y - source.y) / 2)
  const busY = sharedBusGroups?.has(groupKey) === false && midY > stubY
    ? midY
    : Math.max(dropY, stubY)
  if (busY <= target.y) {
    return normalizeOrthogonalPoints([source, { x: source.x, y: busY }, { x: target.x, y: busY }, target])
  }
  const targetNode = input.nodes.find(node => node.id === edge.target)
  const sideX = targetNode ? primarySideApproachX(source, target, targetNode.rect) : target.x
  return normalizeOrthogonalPoints([
    source,
    { x: source.x, y: busY },
    { x: sideX, y: busY },
    { x: sideX, y: target.y },
    target,
  ])
}

type RoutingDirection = 'none' | GraphAxis
type RoutingState = { xIndex: number; yIndex: number; direction: RoutingDirection }

type HeapEntry = { key: string; state: RoutingState; cost: number; estimate: number }

class MinHeap {
  private entries: HeapEntry[] = []

  push(entry: HeapEntry) {
    this.entries.push(entry)
    let index = this.entries.length - 1
    while (index > 0) {
      const parent = Math.floor((index - 1) / 2)
      if (this.compare(this.entries[parent], this.entries[index]) <= 0) break
      ;[this.entries[parent], this.entries[index]] = [this.entries[index], this.entries[parent]]
      index = parent
    }
  }

  pop(): HeapEntry | undefined {
    const first = this.entries[0]
    const last = this.entries.pop()
    if (!first || !last || !this.entries.length) return first
    this.entries[0] = last
    let index = 0
    while (true) {
      const left = index * 2 + 1
      const right = left + 1
      let smallest = index
      if (left < this.entries.length && this.compare(this.entries[left], this.entries[smallest]) < 0) smallest = left
      if (right < this.entries.length && this.compare(this.entries[right], this.entries[smallest]) < 0) smallest = right
      if (smallest === index) break
      ;[this.entries[index], this.entries[smallest]] = [this.entries[smallest], this.entries[index]]
      index = smallest
    }
    return first
  }

  get length() { return this.entries.length }

  private compare(left: HeapEntry, right: HeapEntry) {
    return left.estimate - right.estimate || left.cost - right.cost || left.key.localeCompare(right.key)
  }
}

function coordinateList(values: number[]) {
  return Array.from(new Set(values.map(routingCoordinate))).sort((left, right) => left - right)
}

function pointKey(state: RoutingState) {
  return `${state.xIndex}:${state.yIndex}:${state.direction}`
}

function segmentProximityCost(segment: GraphSegment, obstacles: GraphRect[]) {
  return obstacles.reduce((cost, rect) => {
    const near = segmentAxis(segment) === 'horizontal'
      ? segment.from.y >= rect.top - ROUTING_TRACK_GAP && segment.from.y <= rect.bottom + ROUTING_TRACK_GAP
      : segment.from.x >= rect.left - ROUTING_TRACK_GAP && segment.from.x <= rect.right + ROUTING_TRACK_GAP
    return cost + (near ? NODE_PROXIMITY_COST : 0)
  }, 0)
}

function pathfindRoute(
  edge: RoutingEdge,
  source: GraphPoint,
  target: GraphPoint,
  input: GraphRoutingInput,
  reservations: EdgeReservationIndex,
  ignoredEdgeIDs?: Iterable<string>,
): GraphPoint[] | undefined {
  const bounds = graphBounds(input.nodes)
  const obstacles = routeObstacles(edge, input.nodes).map(item => item.rect)
  const ignored = ignoredSet(ignoredEdgeIDs ?? edge.id)
  const outerLeft = bounds.left - OUTER_GUTTER
  const outerRight = bounds.right + OUTER_GUTTER
  const outerBottom = bounds.bottom + OUTER_GUTTER
  const xValues = coordinateList([
    source.x,
    target.x,
    outerLeft,
    outerRight,
    ...obstacles.flatMap(rect => [rect.left, rect.right]),
    ...reservations.allVertical.flatMap(item => [item.fixed - ROUTING_TRACK_GAP, item.fixed + ROUTING_TRACK_GAP]),
  ])
  const yValues = coordinateList([
    source.y,
    target.y,
    source.y + BRANCH_STUB_LENGTH,
    outerBottom,
    ...obstacles.flatMap(rect => [rect.top, rect.bottom]),
    ...(input.layerChannels || []).flatMap(channel => Object.values(channel.tracks)),
    ...reservations.allHorizontal.flatMap(item => [item.fixed - ROUTING_TRACK_GAP, item.fixed + ROUTING_TRACK_GAP]),
  ])
  const start: RoutingState = { xIndex: xValues.indexOf(source.x), yIndex: yValues.indexOf(source.y), direction: 'none' }
  const targetXIndex = xValues.indexOf(target.x)
  const targetYIndex = yValues.indexOf(target.y)
  if (start.xIndex < 0 || start.yIndex < 0 || targetXIndex < 0 || targetYIndex < 0) return undefined

  const heap = new MinHeap()
  const costs = new Map<string, number>()
  const previous = new Map<string, string>()
  const states = new Map<string, RoutingState>()
  const startKey = pointKey(start)
  costs.set(startKey, 0)
  states.set(startKey, start)
  heap.push({ key: startKey, state: start, cost: 0, estimate: Math.abs(source.x - target.x) + Math.abs(source.y - target.y) })
  let finalKey: string | undefined
  while (heap.length) {
    const current = heap.pop()!
    if (current.cost !== costs.get(current.key)) continue
    if (current.state.xIndex === targetXIndex && current.state.yIndex === targetYIndex) {
      finalKey = current.key
      break
    }
    const neighbors: Array<[number, number, GraphAxis]> = [
      [current.state.xIndex - 1, current.state.yIndex, 'horizontal'],
      [current.state.xIndex + 1, current.state.yIndex, 'horizontal'],
      [current.state.xIndex, current.state.yIndex - 1, 'vertical'],
      [current.state.xIndex, current.state.yIndex + 1, 'vertical'],
    ]
    neighbors.forEach(([xIndex, yIndex, direction]) => {
      if (xIndex < 0 || yIndex < 0 || xIndex >= xValues.length || yIndex >= yValues.length) return
      const from = { x: xValues[current.state.xIndex], y: yValues[current.state.yIndex] }
      const to = { x: xValues[xIndex], y: yValues[yIndex] }
      if (obstacles.some(rect => segmentIntersectsRectInterior({ from, to }, rect))) return
      const move = orthogonalSegment(from, to)
      if (reservations.overlapLength(move, ignored) > 0) return
      const bend = current.state.direction !== 'none' && current.state.direction !== direction ? BEND_COST : 0
      const crossing = reservations.crossingCount(move, ignored) * CROSSING_COST
      const aboveSource = to.y < source.y ? ABOVE_SOURCE_COST : 0
      const nextCost = current.cost + segmentLength(move) + bend + crossing + aboveSource + segmentProximityCost(move, obstacles)
      const state: RoutingState = { xIndex, yIndex, direction }
      const key = pointKey(state)
      if (nextCost >= (costs.get(key) ?? Number.POSITIVE_INFINITY)) return
      costs.set(key, nextCost)
      previous.set(key, current.key)
      states.set(key, state)
      const heuristic = Math.abs(to.x - target.x) + Math.abs(to.y - target.y)
      heap.push({ key, state, cost: nextCost, estimate: nextCost + heuristic })
    })
  }
  if (!finalKey) return undefined
  const reversed: GraphPoint[] = []
  let cursor: string | undefined = finalKey
  while (cursor) {
    const state = states.get(cursor)!
    reversed.push({ x: xValues[state.xIndex], y: yValues[state.yIndex] })
    cursor = previous.get(cursor)
  }
  return normalizeOrthogonalPoints(reversed.reverse())
}

export function routeLabelPoint(points: GraphPoint[]): GraphPoint | undefined {
  const segments = routeSegments(points)
  if (!segments.length) return undefined
  const last = segments[segments.length - 1]
  const selected = segmentAxis(last) === 'vertical' && segmentLength(last) >= 20
    ? last
    : (() => {
      const horizontal = segments.filter(segment => segmentAxis(segment) === 'horizontal')
      const preferred = horizontal.filter(segment => segmentLength(segment) >= 80)
      const candidates = preferred.length ? preferred : horizontal.length ? horizontal : segments.filter(segment => segmentAxis(segment) === 'vertical')
      return candidates.slice().sort((left, right) => segmentLength(right) - segmentLength(left)
        || left.from.x - right.from.x || left.from.y - right.from.y)[0]
    })()
  if (!selected) return undefined
  return {
    x: routingCoordinate((selected.from.x + selected.to.x) / 2 + (segmentAxis(selected) === 'vertical' ? 12 : 0)),
    y: routingCoordinate((selected.from.y + selected.to.y) / 2),
  }
}

function routeOrder(nodeByID: Map<string, RoutingNode>) {
  const classOrder: Record<GraphRoutingClass, number> = { primary: 0, belongs: 1, auxiliary: 2 }
  return (left: RoutingEdge, right: RoutingEdge) => {
    const leftSource = nodeByID.get(left.source)
    const rightSource = nodeByID.get(right.source)
    const leftTarget = nodeByID.get(left.target)
    const rightTarget = nodeByID.get(right.target)
    return classOrder[left.routingClass] - classOrder[right.routingClass]
      || (left.sourceRank ?? Number.MAX_SAFE_INTEGER) - (right.sourceRank ?? Number.MAX_SAFE_INTEGER)
      || (left.targetRank ?? Number.MAX_SAFE_INTEGER) - (right.targetRank ?? Number.MAX_SAFE_INTEGER)
      || (leftSource ? nodeCenter(leftSource.rect).x : 0) - (rightSource ? nodeCenter(rightSource.rect).x : 0)
      || (leftTarget ? nodeCenter(leftTarget.rect).x : 0) - (rightTarget ? nodeCenter(rightTarget.rect).x : 0)
      || left.id.localeCompare(right.id)
  }
}

function inferPrimaryRoutingMetadata(input: GraphRoutingInput): GraphRoutingInput {
  const nodeByID = new Map(input.nodes.map(node => [node.id, node]))
  const primary = input.edges.filter(edge => edge.routingClass === 'primary' && nodeByID.has(edge.source) && nodeByID.has(edge.target))
  const incomingCount = new Map(input.nodes.map(node => [node.id, 0]))
  const outgoing = new Map<string, RoutingEdge[]>()
  primary.forEach(edge => {
    incomingCount.set(edge.target, (incomingCount.get(edge.target) || 0) + 1)
    outgoing.set(edge.source, [...(outgoing.get(edge.source) || []), edge])
  })
  outgoing.forEach(edges => edges.sort((left, right) => left.target.localeCompare(right.target) || left.id.localeCompare(right.id)))
  const ready = input.nodes.map(node => node.id).filter(nodeID => (incomingCount.get(nodeID) || 0) === 0).sort()
  const ranks = new Map(input.nodes.map(node => [node.id, 0]))
  while (ready.length) {
    const source = ready.shift()!
    for (const edge of outgoing.get(source) || []) {
      ranks.set(edge.target, Math.max(ranks.get(edge.target) || 0, (ranks.get(source) || 0) + 1))
      const next = (incomingCount.get(edge.target) || 0) - 1
      incomingCount.set(edge.target, next)
      if (next === 0) {
        ready.push(edge.target)
        ready.sort()
      }
    }
  }
  const edges = input.edges.map(edge => edge.routingClass === 'primary'
    ? { ...edge, sourceRank: edge.sourceRank ?? ranks.get(edge.source), targetRank: edge.targetRank ?? ranks.get(edge.target) }
    : edge)
  if (input.layerChannels?.length) return { ...input, edges }
  const maxRank = Math.max(0, ...edges.filter(edge => edge.routingClass === 'primary').flatMap(edge => [edge.sourceRank || 0, edge.targetRank || 0]))
  const layerChannels: GraphLayerChannel[] = []
  for (let sourceRank = 0; sourceRank < maxRank; sourceRank++) {
    const boundaryEdges = edges.filter(edge => edge.routingClass === 'primary' && edge.sourceRank === sourceRank && edge.targetRank === sourceRank + 1)
      .sort((left, right) => {
        const leftSource = nodeByID.get(left.source)
        const rightSource = nodeByID.get(right.source)
        const leftSourceX = leftSource ? nodeCenter(leftSource.rect).x : 0
        const rightSourceX = rightSource ? nodeCenter(rightSource.rect).x : 0
        return leftSourceX - rightSourceX || left.id.localeCompare(right.id)
      })
    if (!boundaryEdges.length) continue
    const sourceIndex = new Map<string, number>()
    boundaryEdges.forEach(edge => {
      const key = branchGroupKey(edge)
      if (!sourceIndex.has(key)) sourceIndex.set(key, sourceIndex.size)
    })
    const sourceNodes = boundaryEdges.map(edge => nodeByID.get(edge.source)).filter((node): node is RoutingNode => Boolean(node))
    const targetNodes = boundaryEdges.map(edge => nodeByID.get(edge.target)).filter((node): node is RoutingNode => Boolean(node))
    const top = Math.max(...sourceNodes.map(node => node.rect.bottom))
    const bottom = Math.min(...targetNodes.map(node => node.rect.top))
    const tracks = Object.fromEntries(boundaryEdges.map(edge => [
      edge.id,
      routingCoordinate(top + BRANCH_STUB_LENGTH + (sourceIndex.get(branchGroupKey(edge)) || 0) * ROUTING_TRACK_GAP),
    ]))
    layerChannels.push({ sourceRank, targetRank: sourceRank + 1, top, bottom, tracks })
  }
  return { ...input, edges, layerChannels }
}

export function validateGraphRoutes(input: GraphRoutingInput, routes: Record<string, GraphEdgeRoute>): GraphRoutingDiagnostics {
  const edgeByID = new Map(input.edges.map(edge => [edge.id, edge]))
  const failedEdgeIDs = input.edges.filter(edge => !routes[edge.id] || routes[edge.id].quality === 'failed' || routes[edge.id].points.length < 2).map(edge => edge.id).sort()
  const nodeIntersectionEdgeIDs = input.edges.filter(edge => {
    const route = routes[edge.id]
    if (!route?.points.length) return false
    return input.nodes.some(node => {
      const rect = node.id === edge.source || node.id === edge.target ? node.rect : expandRect(node.rect, NODE_CLEARANCE)
      return routeIntersectsRect(route.points, rect)
    })
  }).map(edge => edge.id).sort()
  const overlappingEdgePairs: Array<[string, string]> = []
  const crossingPrimaryPairs: Array<[string, string]> = []
  const routedIDs = Object.keys(routes).filter(edgeID => routes[edgeID].points.length >= 2).sort()
  for (let leftIndex = 0; leftIndex < routedIDs.length; leftIndex++) {
    for (let rightIndex = leftIndex + 1; rightIndex < routedIDs.length; rightIndex++) {
      const leftID = routedIDs[leftIndex]
      const rightID = routedIDs[rightIndex]
      if (routeOverlapLength(routes[leftID].points, routes[rightID].points) > 0) {
        const leftEdge = edgeByID.get(leftID)
        const rightEdge = edgeByID.get(rightID)
        const siblingTrunk = leftEdge && rightEdge && branchGroupKey(leftEdge) === branchGroupKey(rightEdge)
        if (!siblingTrunk) overlappingEdgePairs.push(sortedPair(leftID, rightID))
      }
      if (edgeByID.get(leftID)?.routingClass === 'primary'
        && edgeByID.get(rightID)?.routingClass === 'primary'
        && routeCrossingCount(routes[leftID].points, routes[rightID].points) > 0) {
        crossingPrimaryPairs.push(sortedPair(leftID, rightID))
      }
    }
  }
  return { failedEdgeIDs, nodeIntersectionEdgeIDs, overlappingEdgePairs, crossingPrimaryPairs }
}

export function routeProxyGraph(input: GraphRoutingInput): GraphRoutingResult {
  const quantizedInput: GraphRoutingInput = {
    ...input,
    nodes: input.nodes.map(node => ({
      ...node,
      rect: {
        left: routingCoordinate(node.rect.left),
        top: routingCoordinate(node.rect.top),
        right: routingCoordinate(node.rect.right),
        bottom: routingCoordinate(node.rect.bottom),
      },
      handles: node.handles ? Object.fromEntries(
        Object.entries(node.handles).map(([key, point]) => [
          key,
          { x: routingCoordinate(point.x), y: routingCoordinate(point.y) },
        ])
      ) : undefined,
    })),
  }
  const routingInput = inferPrimaryRoutingMetadata(quantizedInput)
  const nodeByID = new Map(routingInput.nodes.map(node => [node.id, node]))
  const edges = routingInput.edges.filter(edge => nodeByID.has(edge.source) && nodeByID.has(edge.target)).slice().sort(routeOrder(nodeByID))
  const sourcePorts = allocateSidePorts(edges, nodeByID, 'source')
  const targetPorts = allocateSidePorts(edges, nodeByID, 'target')
  const reservations = new EdgeReservationIndex()
  const routes: Record<string, GraphEdgeRoute> = {}
  let auxiliaryIndex = 0
  const belongsEdges = edges.filter(edge => edge.routingClass === 'belongs')
  const belongsEdgesByTarget = new Map<string, RoutingEdge[]>()
  belongsEdges.forEach(edge => {
    belongsEdgesByTarget.set(edge.target, [...(belongsEdgesByTarget.get(edge.target) || []), edge])
  })
  const belongsTracks = new Map<string, number>()
  belongsEdgesByTarget.forEach((targetEdges, targetNodeID) => {
    const targetNode = nodeByID.get(targetNodeID)
    if (!targetNode) return
    const sorted = targetEdges.slice().sort((left, right) => {
      const leftSource = nodeByID.get(left.source)
      const rightSource = nodeByID.get(right.source)
      const leftX = leftSource ? leftSource.rect.left : 0
      const rightX = rightSource ? rightSource.rect.left : 0
      return leftX - rightX || left.id.localeCompare(right.id)
    })
    const count = sorted.length
    sorted.forEach((edge, index) => {
      const sourceNode = nodeByID.get(edge.source)
      const sourceY = sourceNode ? sourceNode.rect.bottom : targetNode.rect.top - 120
      const targetY = targetNode.rect.top
      const gap = Math.max(ROUTING_TRACK_GAP * 2, targetY - sourceY)
      const base = sourceY + (gap * 0.4)
      const offset = (index - (count - 1) / 2) * ROUTING_TRACK_GAP
      belongsTracks.set(edge.id, routingCoordinate(base + offset))
    })
  })
  const busYByGroup = new Map<string, number>()
  const primaryByGroup = new Map<string, RoutingEdge[]>()
  edges.forEach(edge => {
    if (edge.routingClass !== 'primary') return
    const key = branchGroupKey(edge)
    primaryByGroup.set(key, [...(primaryByGroup.get(key) || []), edge])
  })
  const sharedBusGroups = new Set(Array.from(primaryByGroup.entries())
    .filter(([, groupEdges]) => groupEdges.length > 1)
    .map(([key]) => key))
  primaryByGroup.forEach((groupEdges, key) => {
    const sourcePort = sourcePorts.get(groupEdges[0].id)
    if (!sourcePort) return
    const channel = routingInput.layerChannels?.find(item => (
      groupEdges.some(edge => item.sourceRank === edge.sourceRank && item.targetRank === edge.targetRank)
    ))
    const trackY = groupEdges
      .map(edge => channel?.tracks[edge.id])
      .find((value): value is number => value !== undefined && value > sourcePort.point.y)
    busYByGroup.set(key, routingCoordinate(Math.max(sourcePort.point.y + BRANCH_STUB_LENGTH, trackY ?? 0)))
  })
  edges.forEach(edge => {
    const sourcePort = sourcePorts.get(edge.id)
    const targetPort = targetPorts.get(edge.id)
    if (!sourcePort || !targetPort) {
      routes[edge.id] = { points: [], quality: 'failed' }
      return
    }
    const ignored = branchSiblingIDs(edge, edges)
    const preferred = preferredRoute(edge, sourcePort, targetPort, routingInput, auxiliaryIndex, belongsTracks, busYByGroup, sharedBusGroups)
    if (edge.routingClass === 'auxiliary') auxiliaryIndex++
    let points: GraphPoint[] | undefined
    let quality: GraphRouteQuality = 'preferred'
    // Obstacle avoidance is the expensive half of routing. Callers that need a
    // route on every animation frame skip it and take the direct candidate.
    if (input.avoidObstacles === false || validateRouteCandidate(edge, preferred, routingInput.nodes, reservations, ignored).valid) {
      points = preferred
      if (edge.routingClass === 'auxiliary') quality = 'outer-gutter'
    } else {
      points = pathfindRoute(edge, sourcePort.point, targetPort.point, routingInput, reservations, ignored)
      quality = points ? 'pathfinder' : 'failed'
      const bounds = graphBounds(routingInput.nodes)
      if (points?.some(point => point.x <= bounds.left - OUTER_GUTTER || point.x >= bounds.right + OUTER_GUTTER
        || point.y >= bounds.bottom + OUTER_GUTTER)) quality = 'outer-gutter'
    }
    if (!points || points.length < 2) {
      routes[edge.id] = { points: preferred, quality: 'preferred', labelPoint: routeLabelPoint(preferred) }
    } else {
      routes[edge.id] = { points, quality, labelPoint: routeLabelPoint(points) }
    }
    reservations.reserve(edge.id, edge.routingClass, routes[edge.id].points)
  })
  routingInput.edges.forEach(edge => {
    if (!routes[edge.id]) routes[edge.id] = { points: [], quality: 'failed' }
  })
  const diagnostics = input.collectDiagnostics === false
    ? { failedEdgeIDs: [], nodeIntersectionEdgeIDs: [], overlappingEdgePairs: [], crossingPrimaryPairs: [] }
    : validateGraphRoutes(routingInput, routes)
  return { routes, diagnostics }
}
