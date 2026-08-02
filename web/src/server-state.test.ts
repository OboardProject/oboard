import { describe, expect, it } from 'vitest'

import { removeServerSnapshot, upsertServerSnapshot } from './server-state'

type Server = { id: number; name: string }

describe('server snapshots', () => {
  it('inserts a newly created server in backend list order', () => {
    const servers: Server[] = [{ id: 2, name: 'two' }, { id: 1, name: 'one' }]

    expect(upsertServerSnapshot(servers, { id: 3, name: 'three' })).toEqual([
      { id: 3, name: 'three' },
      { id: 2, name: 'two' },
      { id: 1, name: 'one' },
    ])
  })

  it('replaces an updated server without creating a duplicate', () => {
    const servers: Server[] = [{ id: 3, name: 'three' }, { id: 2, name: 'old' }]

    expect(upsertServerSnapshot(servers, { id: 2, name: 'updated' })).toEqual([
      { id: 3, name: 'three' },
      { id: 2, name: 'updated' },
    ])
  })

  it('removes a server and allows the original snapshot to be restored', () => {
    const servers: Server[] = [{ id: 2, name: 'two' }, { id: 1, name: 'one' }]
    const removed = removeServerSnapshot(servers, 2)

    expect(removed).toEqual([{ id: 1, name: 'one' }])
    expect(upsertServerSnapshot(removed, servers[0])).toEqual(servers)
  })
})
