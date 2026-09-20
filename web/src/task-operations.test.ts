import { expect, it } from 'vitest'
import { groupTasksForTimeline } from './task-groups'

it('groups by every persisted intent regardless of time and retains authoritative totals on a task page', () => {
  const summary = (id: string) => ({ id, total: 3, pending: 1, running: 0, succeeded: 1, failed: 1, unknown: 0 })
  const task = (id: number, at: string, ids: string[]) => ({ id, server_id: id, type: 'benchmark_dns', status: 'succeeded', created_at: at, operations: ids.map(summary) })
  const groups = groupTasksForTimeline([
    task(1, '2026-09-19T00:00:01Z', ['a']),
    task(2, '2026-09-19T00:00:02Z', ['b']),
    task(3, '2026-09-19T00:20:00Z', ['a', 'b']),
  ], type => type)
  expect(groups).toHaveLength(2)
  expect(groups.find(g => g.operation?.id === 'a')?.tasks.map(t => t.id)).toEqual([1, 3])
  expect(groups.find(g => g.operation?.id === 'b')?.tasks.map(t => t.id)).toEqual([2, 3])
  const page = groupTasksForTimeline([task(1, '2026-09-19T00:00:01Z', ['a'])], type => type)
  expect(page[0].operation).toEqual(summary('a'))
  expect(page[0].kind).toBe('batch')
})
