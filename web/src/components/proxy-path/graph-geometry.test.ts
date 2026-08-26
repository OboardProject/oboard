import { describe, expect, it } from 'vitest'
import {
  GRAPH_EDGE_ARROW_GAP,
  GRAPH_EDGE_ARROW_LENGTH,
  GRAPH_EDGE_ARROW_WIDTH,
  collinearOverlapLength,
  normalizeOrthogonalPoints,
  pointToPolylineDistance,
  roundedOrthogonalPath,
  routeEndArrowPoints,
  segmentIntersectsRectInterior,
  segmentsCross,
  trimRouteEnd,
  type GraphSegment,
} from './graph-geometry'

const rect = { left: 100, top: 100, right: 200, bottom: 200 }
const segment = (from: [number, number], to: [number, number]): GraphSegment => ({
  from: { x: from[0], y: from[1] },
  to: { x: to[0], y: to[1] },
})

describe('segmentIntersectsRectInterior', () => {
  it('detects horizontal and vertical penetration', () => {
    expect(segmentIntersectsRectInterior(segment([50, 150], [250, 150]), rect)).toBe(true)
    expect(segmentIntersectsRectInterior(segment([150, 50], [150, 250]), rect)).toBe(true)
  })

  it('allows routes on the rectangle boundary and outside it', () => {
    expect(segmentIntersectsRectInterior(segment([50, 100], [250, 100]), rect)).toBe(false)
    expect(segmentIntersectsRectInterior(segment([100, 50], [100, 250]), rect)).toBe(false)
    expect(segmentIntersectsRectInterior(segment([20, 40], [80, 40]), rect)).toBe(false)
  })
})

describe('collinearOverlapLength', () => {
  it('distinguishes separation, endpoint contact, and non-zero overlap', () => {
    expect(collinearOverlapLength(segment([0, 0], [10, 0]), segment([20, 0], [30, 0]))).toBe(0)
    expect(collinearOverlapLength(segment([0, 0], [10, 0]), segment([10, 0], [20, 0]))).toBe(0)
    expect(collinearOverlapLength(segment([0, 0], [20, 0]), segment([10, 0], [30, 0]))).toBe(10)
    expect(collinearOverlapLength(segment([0, 0], [20, 0]), segment([0, 0], [20, 0]))).toBe(20)
    expect(collinearOverlapLength(segment([0, 0], [20, 0]), segment([10, -10], [10, 10]))).toBe(0)
  })
})

describe('segmentsCross', () => {
  it('treats an interior X as a crossing and a T-junction as contact', () => {
    expect(segmentsCross(segment([0, 10], [20, 10]), segment([10, 0], [10, 20]))).toBe(true)
    expect(segmentsCross(segment([0, 10], [20, 10]), segment([10, 10], [10, 20]))).toBe(false)
    expect(segmentsCross(segment([0, 10], [10, 10]), segment([10, 10], [10, 20]))).toBe(false)
  })
})

describe('normalizeOrthogonalPoints', () => {
  it('removes duplicates and merges straight runs while retaining corners', () => {
    expect(normalizeOrthogonalPoints([
      { x: 0, y: 0 },
      { x: 0, y: 0 },
      { x: 20, y: 0 },
      { x: 40, y: 0 },
      { x: 40, y: 20 },
      { x: 40, y: 30 },
      { x: 60, y: 30 },
    ])).toEqual([
      { x: 0, y: 0 },
      { x: 40, y: 0 },
      { x: 40, y: 30 },
      { x: 60, y: 30 },
    ])
  })
})

describe('pointToPolylineDistance', () => {
  it('finds the nearest point across every route segment', () => {
    const route = [{ x: 0, y: 0 }, { x: 0, y: 100 }, { x: 100, y: 100 }]
    expect(pointToPolylineDistance({ x: 8, y: 40 }, route)).toBe(8)
    expect(pointToPolylineDistance({ x: 70, y: 112 }, route)).toBe(12)
    expect(pointToPolylineDistance({ x: -3, y: -4 }, route)).toBe(5)
  })
})

describe('trimRouteEnd', () => {
  it('shortens a vertical drop without moving earlier corners', () => {
    expect(trimRouteEnd([{ x: 10, y: 0 }, { x: 10, y: 100 }], 8)).toEqual([
      { x: 10, y: 0 },
      { x: 10, y: 92 },
    ])
  })

  it('walks back through a short last segment', () => {
    expect(trimRouteEnd([{ x: 0, y: 0 }, { x: 40, y: 0 }, { x: 40, y: 6 }], 10)).toEqual([
      { x: 0, y: 0 },
      { x: 36, y: 0 },
    ])
  })
})

describe('routeEndArrowPoints', () => {
  it('keeps a downward dart centered on the last segment', () => {
    const arrow = routeEndArrowPoints([{ x: 20, y: 0 }, { x: 20, y: 80 }])
    const tipY = 80 - GRAPH_EDGE_ARROW_GAP
    const baseY = tipY - GRAPH_EDGE_ARROW_LENGTH
    const half = GRAPH_EDGE_ARROW_WIDTH / 2
    expect(arrow).toEqual({
      tip: { x: 20, y: tipY },
      left: { x: 20 - half, y: baseY },
      right: { x: 20 + half, y: baseY },
    })
  })

  it('skips arrows when the last segment is too short to seat them', () => {
    expect(routeEndArrowPoints([{ x: 0, y: 0 }, { x: 0, y: 6 }])).toBeUndefined()
  })
})

describe('roundedOrthogonalPath', () => {
  it.each([
    [{ x: 0, y: 0 }, { x: 0, y: 100 }],
    [{ x: 0, y: 0 }, { x: 100, y: 0 }],
    [{ x: 0, y: 0 }, { x: 0, y: 100 }, { x: 100, y: 100 }],
    [{ x: 0, y: 0 }, { x: 0, y: 100 }, { x: 100, y: 100 }, { x: 100, y: 200 }],
  ])('is deterministic and finite for an orthogonal route', (...points) => {
    const left = roundedOrthogonalPath(points)
    const right = roundedOrthogonalPath(points)
    expect(left).toBe(right)
    expect(left).not.toContain('NaN')
    expect(left.startsWith('M ')).toBe(true)
  })
})
