export type GraphPoint = {
  x: number
  y: number
}

export type GraphRect = {
  left: number
  top: number
  right: number
  bottom: number
}

export type GraphSegment = {
  from: GraphPoint
  to: GraphPoint
}

export type GraphAxis = 'horizontal' | 'vertical'

export function routingCoordinate(value: number) {
  return Math.round(value * 2) / 2
}

function routingPoint(point: GraphPoint): GraphPoint {
  return { x: routingCoordinate(point.x), y: routingCoordinate(point.y) }
}

export function rectWidth(rect: GraphRect): number {
  return rect.right - rect.left
}

export function rectHeight(rect: GraphRect): number {
  return rect.bottom - rect.top
}

export function expandRect(rect: GraphRect, padding: number): GraphRect {
  return {
    left: routingCoordinate(rect.left - padding),
    top: routingCoordinate(rect.top - padding),
    right: routingCoordinate(rect.right + padding),
    bottom: routingCoordinate(rect.bottom + padding),
  }
}

export function pointInsideRectInterior(point: GraphPoint, rect: GraphRect): boolean {
  const normalized = routingPoint(point)
  return normalized.x > routingCoordinate(rect.left)
    && normalized.x < routingCoordinate(rect.right)
    && normalized.y > routingCoordinate(rect.top)
    && normalized.y < routingCoordinate(rect.bottom)
}

export function orthogonalSegment(from: GraphPoint, to: GraphPoint): GraphSegment {
  const segment = { from: routingPoint(from), to: routingPoint(to) }
  if (segment.from.x !== segment.to.x && segment.from.y !== segment.to.y) {
    throw new Error('Graph segment must be orthogonal')
  }
  return segment
}

export function segmentAxis(segment: GraphSegment): GraphAxis {
  const normalized = orthogonalSegment(segment.from, segment.to)
  return normalized.from.y === normalized.to.y ? 'horizontal' : 'vertical'
}

export function segmentLength(segment: GraphSegment): number {
  const normalized = orthogonalSegment(segment.from, segment.to)
  return Math.abs(normalized.to.x - normalized.from.x) + Math.abs(normalized.to.y - normalized.from.y)
}

export function routeSegments(points: GraphPoint[]): GraphSegment[] {
  const normalized = normalizeOrthogonalPoints(points)
  return normalized.slice(1).map((point, index) => orthogonalSegment(normalized[index], point))
}

export function normalizeOrthogonalPoints(points: GraphPoint[]): GraphPoint[] {
  const normalized: GraphPoint[] = []
  points.forEach(rawPoint => {
    const point = routingPoint(rawPoint)
    const previous = normalized[normalized.length - 1]
    if (previous && previous.x === point.x && previous.y === point.y) return
    if (previous && previous.x !== point.x && previous.y !== point.y) {
      throw new Error('Graph route contains a diagonal segment')
    }
    normalized.push(point)
    while (normalized.length >= 3) {
      const before = normalized[normalized.length - 3]
      const middle = normalized[normalized.length - 2]
      const after = normalized[normalized.length - 1]
      if ((before.x === middle.x && middle.x === after.x) || (before.y === middle.y && middle.y === after.y)) {
        normalized.splice(normalized.length - 2, 1)
        continue
      }
      break
    }
  })
  return normalized
}

export function segmentIntersectsRectInterior(segment: GraphSegment, rect: GraphRect): boolean {
  const normalized = orthogonalSegment(segment.from, segment.to)
  if (segmentLength(normalized) === 0) return pointInsideRectInterior(normalized.from, rect)
  if (segmentAxis(normalized) === 'horizontal') {
    if (normalized.from.y <= rect.top || normalized.from.y >= rect.bottom) return false
    const start = Math.min(normalized.from.x, normalized.to.x)
    const end = Math.max(normalized.from.x, normalized.to.x)
    return end > rect.left && start < rect.right
  }
  if (normalized.from.x <= rect.left || normalized.from.x >= rect.right) return false
  const start = Math.min(normalized.from.y, normalized.to.y)
  const end = Math.max(normalized.from.y, normalized.to.y)
  return end > rect.top && start < rect.bottom
}

function betweenInclusive(value: number, start: number, end: number) {
  const minimum = Math.min(start, end)
  const maximum = Math.max(start, end)
  return value >= minimum && value <= maximum
}

function isSegmentEndpoint(point: GraphPoint, segment: GraphSegment) {
  return (point.x === segment.from.x && point.y === segment.from.y)
    || (point.x === segment.to.x && point.y === segment.to.y)
}

export function segmentsCross(a: GraphSegment, b: GraphSegment): boolean {
  const left = orthogonalSegment(a.from, a.to)
  const right = orthogonalSegment(b.from, b.to)
  if (segmentLength(left) === 0 || segmentLength(right) === 0) return false
  const leftAxis = segmentAxis(left)
  const rightAxis = segmentAxis(right)
  if (leftAxis === rightAxis) return false
  const horizontal = leftAxis === 'horizontal' ? left : right
  const vertical = leftAxis === 'vertical' ? left : right
  const intersection = { x: vertical.from.x, y: horizontal.from.y }
  if (!betweenInclusive(intersection.x, horizontal.from.x, horizontal.to.x)
    || !betweenInclusive(intersection.y, vertical.from.y, vertical.to.y)) return false
  // A T-junction or L-corner touches at an endpoint. Only an interior X is a crossing.
  return !isSegmentEndpoint(intersection, horizontal) && !isSegmentEndpoint(intersection, vertical)
}

export function collinearOverlapLength(a: GraphSegment, b: GraphSegment): number {
  const left = orthogonalSegment(a.from, a.to)
  const right = orthogonalSegment(b.from, b.to)
  if (segmentAxis(left) !== segmentAxis(right)) return 0
  if (segmentAxis(left) === 'horizontal') {
    if (left.from.y !== right.from.y) return 0
    return Math.max(0, Math.min(Math.max(left.from.x, left.to.x), Math.max(right.from.x, right.to.x))
      - Math.max(Math.min(left.from.x, left.to.x), Math.min(right.from.x, right.to.x)))
  }
  if (left.from.x !== right.from.x) return 0
  return Math.max(0, Math.min(Math.max(left.from.y, left.to.y), Math.max(right.from.y, right.to.y))
    - Math.max(Math.min(left.from.y, left.to.y), Math.min(right.from.y, right.to.y)))
}

export function routeIntersectsRect(points: GraphPoint[], rect: GraphRect): boolean {
  return routeSegments(points).some(segment => segmentIntersectsRectInterior(segment, rect))
}

export function routeCrossingCount(left: GraphPoint[], right: GraphPoint[]): number {
  return routeSegments(left).reduce((count, leftSegment) => (
    count + routeSegments(right).filter(rightSegment => segmentsCross(leftSegment, rightSegment)).length
  ), 0)
}

export function routeOverlapLength(left: GraphPoint[], right: GraphPoint[]): number {
  return routeSegments(left).reduce((length, leftSegment) => (
    length + routeSegments(right).reduce((sum, rightSegment) => sum + collinearOverlapLength(leftSegment, rightSegment), 0)
  ), 0)
}

export function pointToPolylineDistance(point: GraphPoint, points: readonly GraphPoint[]): number {
  if (!points.length) return Number.POSITIVE_INFINITY
  if (points.length === 1) return Math.hypot(point.x - points[0].x, point.y - points[0].y)
  let minimum = Number.POSITIVE_INFINITY
  for (let index = 1; index < points.length; index++) {
    const from = points[index - 1]
    const to = points[index]
    const dx = to.x - from.x
    const dy = to.y - from.y
    const lengthSquared = dx * dx + dy * dy
    const ratio = lengthSquared
      ? Math.max(0, Math.min(1, ((point.x - from.x) * dx + (point.y - from.y) * dy) / lengthSquared))
      : 0
    minimum = Math.min(minimum, Math.hypot(point.x - (from.x + ratio * dx), point.y - (from.y + ratio * dy)))
  }
  return minimum
}

function pointText(point: GraphPoint) {
  return `${point.x} ${point.y}`
}

export function roundedOrthogonalPath(points: GraphPoint[], radius = 24): string {
  const route = normalizeOrthogonalPoints(points)
  if (!route.length) return ''
  if (route.length === 1) return `M ${pointText(route[0])}`
  let path = `M ${pointText(route[0])}`
  for (let index = 1; index < route.length - 1; index++) {
    const previous = route[index - 1]
    const corner = route[index]
    const next = route[index + 1]
    const incomingLength = Math.abs(corner.x - previous.x) + Math.abs(corner.y - previous.y)
    const outgoingLength = Math.abs(next.x - corner.x) + Math.abs(next.y - corner.y)
    const cornerRadius = Math.max(0, Math.min(radius, incomingLength * 0.48, outgoingLength * 0.48))
    if (!cornerRadius) {
      path += ` L ${pointText(corner)}`
      continue
    }
    const before = {
      x: routingCoordinate(corner.x + Math.sign(previous.x - corner.x) * cornerRadius),
      y: routingCoordinate(corner.y + Math.sign(previous.y - corner.y) * cornerRadius),
    }
    const after = {
      x: routingCoordinate(corner.x + Math.sign(next.x - corner.x) * cornerRadius),
      y: routingCoordinate(corner.y + Math.sign(next.y - corner.y) * cornerRadius),
    }
    path += ` L ${pointText(before)} Q ${pointText(corner)} ${pointText(after)}`
  }
  path += ` L ${pointText(route[route.length - 1])}`
  return path
}

export function curvedGraphPath(points: GraphPoint[], radius = 24): string {
  const route = normalizeOrthogonalPoints(points)
  if (!route.length) return ''
  if (route.length === 1) return `M ${pointText(route[0])}`
  if (route.length === 2) {
    const [p0, p1] = route
    if (p0.x === p1.x || p0.y === p1.y) {
      return `M ${pointText(p0)} L ${pointText(p1)}`
    }
    const midY = routingCoordinate((p0.y + p1.y) / 2)
    return `M ${pointText(p0)} C ${p0.x} ${midY}, ${p1.x} ${midY}, ${pointText(p1)}`
  }
  return roundedOrthogonalPath(route, radius)
}

export const GRAPH_EDGE_ARROW_GAP = 3
export const GRAPH_EDGE_ARROW_LENGTH = 8
export const GRAPH_EDGE_ARROW_WIDTH = 5

/** Shortens the last orthogonal segments so a stroke can stop at an arrow base. */
export function trimRouteEnd(points: GraphPoint[], distance: number): GraphPoint[] {
  const route = normalizeOrthogonalPoints(points).map(point => ({ ...point }))
  if (route.length < 2 || distance <= 0) return route
  let remaining = distance
  while (route.length >= 2 && remaining > 0) {
    const last = route[route.length - 1]
    const previous = route[route.length - 2]
    const length = Math.abs(last.x - previous.x) + Math.abs(last.y - previous.y)
    if (length <= remaining + 0.5) {
      remaining -= length
      route.pop()
      continue
    }
    const ratio = remaining / length
    last.x = routingCoordinate(last.x - (last.x - previous.x) * ratio)
    last.y = routingCoordinate(last.y - (last.y - previous.y) * ratio)
    remaining = 0
  }
  return route.length ? route : normalizeOrthogonalPoints(points)
}

export type GraphEdgeArrow = {
  tip: GraphPoint
  left: GraphPoint
  right: GraphPoint
}

/** Slim dart aligned to the last segment, tip held `gap` short of the node. */
export function routeEndArrowPoints(
  points: GraphPoint[],
  gap = GRAPH_EDGE_ARROW_GAP,
  length = GRAPH_EDGE_ARROW_LENGTH,
  width = GRAPH_EDGE_ARROW_WIDTH,
): GraphEdgeArrow | undefined {
  const toTip = trimRouteEnd(points, gap)
  if (toTip.length < 2) return undefined
  const tip = toTip[toTip.length - 1]
  const previous = toTip[toTip.length - 2]
  const dx = tip.x - previous.x
  const dy = tip.y - previous.y
  const segmentLength = Math.hypot(dx, dy)
  if (segmentLength < length + 0.5) return undefined
  const ux = dx / segmentLength
  const uy = dy / segmentLength
  const px = -uy
  const py = ux
  const baseX = tip.x - ux * length
  const baseY = tip.y - uy * length
  const half = width / 2
  return {
    tip: { x: routingCoordinate(tip.x), y: routingCoordinate(tip.y) },
    left: { x: routingCoordinate(baseX + px * half), y: routingCoordinate(baseY + py * half) },
    right: { x: routingCoordinate(baseX - px * half), y: routingCoordinate(baseY - py * half) },
  }
}

export function routeEndArrowPath(arrow: GraphEdgeArrow): string {
  return `M ${pointText(arrow.tip)} L ${pointText(arrow.left)} L ${pointText(arrow.right)} Z`
}
