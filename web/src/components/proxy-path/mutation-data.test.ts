import { describe, expect, it } from 'vitest'
import { mergeTopologyMutation, removeTopologyRows } from './mutation-data'

describe('topology mutation data', () => {
  it('merges confirmed singular and partial collection responses without dropping existing rows', () => {
    const current = {
      proxy_paths: [{ id: 1, name: 'old' }],
      proxy_path_steps: [{ id: 10, path_id: 1 }],
    }
    const next = mergeTopologyMutation(current, {
      proxy_path: { id: 1, name: 'new' },
      proxy_path_steps: [{ id: 11, path_id: 1 }],
    })
    expect(next.proxy_paths).toEqual([{ id: 1, name: 'new' }])
    expect(next.proxy_path_steps).toEqual([{ id: 10, path_id: 1 }, { id: 11, path_id: 1 }])
  })

  it('removes only the confirmed rows', () => {
    const current = { proxy_paths: [{ id: 1 }, { id: 2 }], proxy_path_steps: [{ id: 10 }, { id: 20 }] }
    expect(removeTopologyRows(current, { proxy_paths: [1], proxy_path_steps: [10] })).toEqual({
      proxy_paths: [{ id: 2 }],
      proxy_path_steps: [{ id: 20 }],
    })
  })
})
