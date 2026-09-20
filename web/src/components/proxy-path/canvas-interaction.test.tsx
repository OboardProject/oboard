// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it } from 'vitest'
import { findCanvasPlacement, graphConnectionIssue, useCanvasScope } from './canvas-interaction'

describe('canvas connection checks', () => {
  const edges = [{ source: 'a', target: 'b', sourceHandle: 'out', data: { routingClass: 'primary' } }, { source: 'b', target: 'c', data: { routingClass: 'primary' } }]
  it('rejects duplicate intents, self connections and back edges', () => {
    expect(graphConnectionIssue({ source: 'a', target: 'b', sourceHandle: 'out' }, edges)).toContain('已经相连')
    expect(graphConnectionIssue({ source: 'a', target: 'a' }, edges)).toContain('自身')
    expect(graphConnectionIssue({ source: 'c', target: 'a' }, edges)).toContain('循环')
    expect(graphConnectionIssue({ source: 'a', target: 'c' }, edges, true)).toContain('处理中')
  })
  it('allows branches and ignores ownership edges for cycle detection', () => {
    expect(graphConnectionIssue({ source: 'a', target: 'b', sourceHandle: 'other' }, edges)).toBe('')
    expect(graphConnectionIssue({ source: 'c', target: 'a' }, edges.map(edge => ({ ...edge, data: { routingClass: 'ownership' } })))).toBe('')
  })
})

describe('canvas placement', () => {
  it('chooses visible free space before expanding beyond the viewport', () => {
    const occupied = [{ x: 0, y: 0, width: 260, height: 140 }]
    const position = findCanvasPlacement({ x: 0, y: 0 }, { width: 260, height: 140 }, occupied, { x: 0, y: 0, width: 600, height: 400 })
    expect(position.x >= 284 || position.y >= 164).toBe(true)
    expect(position.x + 260).toBeLessThanOrEqual(600)
    expect(position.y + 140).toBeLessThanOrEqual(400)
  })
  it('never falls back onto occupied cards when the viewport is full', () => {
    const position = findCanvasPlacement({ x: 0, y: 0 }, { width: 260, height: 140 }, [{ x: -500, y: -500, width: 1000, height: 1000 }], { x: 0, y: 0, width: 100, height: 100 })
    expect(position.y).toBeGreaterThan(500)
  })
})

it('preserves each root draft and routes late completions to their original root', () => {
  const container = document.createElement('div')
  const root = createRoot(container)
  let update!: ReturnType<typeof useCanvasScope<number[]>>[1]
  function Harness({ id }: { id: number }) {
    const [items, setItems] = useCanvasScope<number[]>(id, [])
    update = setItems
    return <span>{items.join(',')}</span>
  }
  act(() => root.render(<Harness id={1} />))
  const updateFirst = update
  act(() => { update(items => [...items, 1]); update(items => [...items, 2]) })
  expect(container.textContent).toBe('1,2')
  act(() => root.render(<Harness id={2} />))
  expect(container.textContent).toBe('')
  act(() => { update(items => [...items, 9]); updateFirst(items => [...items, 3]) })
  expect(container.textContent).toBe('9')
  act(() => root.render(<Harness id={1} />))
  expect(container.textContent).toBe('1,2,3')
  act(() => root.unmount())
})
