import { describe, expect, it } from 'vitest'
import { GRAPH_ENTRY_NODE_WIDTH, defaultEntryGraphPosition, graphServerNodeWidth } from './layout'

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
})
