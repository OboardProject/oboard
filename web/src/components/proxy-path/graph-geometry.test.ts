import { describe, expect, it } from 'vitest'
import {
  collinearOverlapLength,
  normalizeOrthogonalPoints,
  roundedOrthogonalPath,
  segmentIntersectsRectInterior,
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
