import { describe, expect, it, vi } from 'vitest'
import { createServerRecord, deleteServerRecord, ServerMutationUncertainError } from './server-mutations'

const server = { id: 7, name: 'Tokyo' }
const unavailable = Object.assign(new Error('internal server error'), { status: 500 })

describe('server mutation response recovery', () => {
  it('uses the creation response without another request', async () => {
    const request = vi.fn().mockResolvedValue({ server, desired_revision: 12, configuration_sync: [] })
    await expect(createServerRecord({ request }, { name: 'Tokyo' }, [])).resolves.toEqual({ server, desired_revision: 12, configuration_sync: [], recovered: false })
    expect(request).toHaveBeenCalledTimes(1)
  })

  it.each([unavailable, new TypeError('Failed to fetch')])('does not infer creation from another administrator’s matching server', async error => {
    const request = vi.fn().mockRejectedValueOnce(error).mockResolvedValueOnce({ servers: [server] })
    await expect(createServerRecord({ request }, { name: ' tokyo ' }, [])).rejects.toBeInstanceOf(ServerMutationUncertainError)
    expect(request.mock.calls.map(call => call[1]?.method || 'GET')).toEqual(['POST'])
  })

  it('keeps an incomplete success response unknown without resubmitting', async () => {
    const request = vi.fn().mockResolvedValueOnce({ server: null }).mockResolvedValueOnce({ servers: [server] })
    await expect(createServerRecord({ request }, { name: 'Tokyo' }, [])).rejects.toBeInstanceOf(ServerMutationUncertainError)
    expect(request).toHaveBeenCalledTimes(1)
  })

  it('does not mistake a known duplicate for a successful create', async () => {
    const request = vi.fn().mockRejectedValueOnce(unavailable).mockResolvedValueOnce({ servers: [server] })
    await expect(createServerRecord({ request }, { name: 'Tokyo' }, [server])).rejects.toBeInstanceOf(ServerMutationUncertainError)
  })

  it('preserves validation failures without looking up a matching server', async () => {
    const conflict = Object.assign(new Error('duplicate name'), { status: 409 })
    const request = vi.fn().mockRejectedValue(conflict)
    await expect(createServerRecord({ request }, { name: 'Tokyo' }, [])).rejects.toBe(conflict)
    expect(request).toHaveBeenCalledTimes(1)
  })

  it('confirms deletion after a lost response instead of restoring a deleted server', async () => {
    const request = vi.fn().mockRejectedValueOnce(unavailable).mockRejectedValueOnce({ status: 404 })
    await expect(deleteServerRecord({ request }, server.id)).resolves.toBeUndefined()
    expect(request.mock.calls).toEqual([['/servers/7', { method: 'DELETE' }], ['/servers/7']])
  })

  it('keeps deletion unknown while a read still sees the server, without retrying', async () => {
    const request = vi.fn().mockRejectedValueOnce(unavailable).mockResolvedValueOnce({ server })
    await expect(deleteServerRecord({ request }, server.id)).rejects.toBeInstanceOf(ServerMutationUncertainError)
  })

  it('does not claim success or failure when both mutation and verification fail', async () => {
    const request = vi.fn().mockRejectedValue(unavailable)
    await expect(createServerRecord({ request }, { name: 'Tokyo' }, [])).rejects.toBeInstanceOf(ServerMutationUncertainError)
    await expect(deleteServerRecord({ request }, server.id)).rejects.toBeInstanceOf(ServerMutationUncertainError)
  })
})
