import { describe, expect, it, vi } from 'vitest'

import { createMutationCoordinator, describeMutationOutcome, isDefiniteFailure } from './mutation-coordinator'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

function httpError(status: number) {
  const error = new Error(`status ${status}`) as Error & { status?: number }
  error.status = status
  return error
}

describe('mutation coordinator', () => {
  it('applies an operation and refreshes only what it changed', async () => {
    const refresh = vi.fn(async () => undefined)
    const coordinator = createMutationCoordinator({ refresh })
    const result = await coordinator.submit({ key: 'server:1', resources: ['servers'], run: async () => 'saved' })
    expect(result.outcome).toBe('applied')
    expect(result.value).toBe('saved')
    expect(refresh).toHaveBeenCalledWith(['servers'])
  })

  it('rolls back only the operation the server rejected', async () => {
    const state: Record<string, string> = { name: 'original', note: 'original' }
    const coordinator = createMutationCoordinator()
    const rename = coordinator.submit({
      key: 'server:1/name',
      optimistic: () => { const previous = state.name; state.name = 'renamed'; return () => { state.name = previous } },
      run: async () => { throw httpError(409) },
    })
    const annotate = coordinator.submit({
      key: 'server:1/note',
      optimistic: () => { const previous = state.note; state.note = 'annotated'; return () => { state.note = previous } },
      run: async () => 'ok',
    })
    expect((await rename).outcome).toBe('rejected')
    expect((await annotate).outcome).toBe('applied')
    expect(state).toEqual({ name: 'original', note: 'annotated' })
  })

  it('serializes explicitly queued saves so the server cannot finish A after B', async () => {
    const state = { value: 'original' }
    const first = deferred<string>()
    const second = deferred<string>()
    const coordinator = createMutationCoordinator()
    const older = coordinator.submit({
      key: 'server:1',
      concurrency: 'queue',
      optimistic: () => { const previous = state.value; state.value = 'first'; return () => { state.value = previous } },
      run: () => first.promise,
    })
    const newer = coordinator.submit({
      key: 'server:1',
      concurrency: 'queue',
      optimistic: () => { const previous = state.value; state.value = 'second'; return () => { state.value = previous } },
      run: () => second.promise,
    })
    await Promise.resolve()
    expect(state.value).toBe('first')
    second.resolve('second-applied')
    expect(state.value).toBe('first')
    first.resolve('first-applied')
    expect((await older).outcome).toBe('applied')
    expect((await newer).outcome).toBe('applied')
    expect(state.value).toBe('second')
  })

  it('keeps an operation whose answer never arrived and reports it as unknown', async () => {
    const state = { value: 'original' }
    const refresh = vi.fn(async () => undefined)
    const states: number[] = []
    const coordinator = createMutationCoordinator({ refresh, onStateChange: s => states.push(s.unknown) })
    const result = await coordinator.submit({
      key: 'server:1',
      resources: ['servers'],
      optimistic: () => { const previous = state.value; state.value = 'submitted'; return () => { state.value = previous } },
      run: async () => { throw new Error('network down') },
    })
    expect(result.outcome).toBe('unknown')
    // Nobody answered, so the panel goes back to the last state the server
    // confirmed and the resources are re-read to establish what really holds.
    expect(state.value).toBe('original')
    expect(refresh).toHaveBeenCalledWith(['servers'])
    expect(coordinator.state().unknown).toBe(1)
    expect(coordinator.unknownResources()).toEqual(['servers'])
    expect(states).toContain(1)
    expect(coordinator.confirm(result.id, { operationID: 'unrelated', outcome: 'applied' })).toBe(false)
    expect(coordinator.state().unknown).toBe(1)
    coordinator.confirm(result.id, { operationID: result.id, outcome: 'applied' })
    expect(coordinator.state().unknown).toBe(0)
  })

  it('separates a refusal from an answer that may have been lost', () => {
    expect(isDefiniteFailure(httpError(409))).toBe(true)
    expect(isDefiniteFailure(httpError(403))).toBe(true)
    // A 5xx or a timeout may still have committed, so it is not a refusal.
    expect(isDefiniteFailure(httpError(500))).toBe(false)
    expect(isDefiniteFailure(httpError(408))).toBe(false)
    expect(isDefiniteFailure(new Error('Failed to fetch'))).toBe(false)
    expect(isDefiniteFailure(undefined)).toBe(false)
  })

  it('treats a server error as an outcome to be confirmed, not a rollback', async () => {
    const state = { value: 'original' }
    const coordinator = createMutationCoordinator()
    const result = await coordinator.submit({
      key: 'server:1',
      resources: ['servers'],
      optimistic: () => { const previous = state.value; state.value = 'submitted'; return () => { state.value = previous } },
      run: async () => { throw httpError(500) },
    })
    expect(result.outcome).toBe('unknown')
    expect(state.value).toBe('original')
  })

  it('does not replay a duplicate command while it is in flight', async () => {
    const pending = deferred<string>()
    const run = vi.fn(() => pending.promise)
    const coordinator = createMutationCoordinator()
    const first = coordinator.submit({ key: 'rotate:1', run })
    const duplicate = await coordinator.submit({ key: 'rotate:1', run })
    expect(duplicate.outcome).toBe('superseded')
    expect(run).toHaveBeenCalledTimes(1)
    pending.resolve('done')
    expect((await first).outcome).toBe('applied')
  })

  it('blocks queued and later writes behind an unknown outcome without retrying', async () => {
    const pending = deferred<string>()
    const coordinator = createMutationCoordinator()
    const first = coordinator.submit({ key: 'server:1', run: () => pending.promise })
    const run = vi.fn(async () => 'B')
    const second = coordinator.submit({ key: 'server:1', concurrency: 'queue', run })
    pending.reject(new Error('lost response'))
    const unknown = await first
    expect((await second).id).toBe(unknown.id)
    expect((await coordinator.submit({ key: 'server:1', run })).outcome).toBe('unknown')
    expect(run).not.toHaveBeenCalled()
    expect(coordinator.state()).toEqual({ pending: 0, unknown: 1 })
  })

  it('does not turn refresh failure into save failure or wait for a read', async () => {
    const refresh = deferred<void>()
    const onRefreshError = vi.fn()
    const coordinator = createMutationCoordinator({ refresh: () => refresh.promise, onRefreshError })
    expect((await coordinator.submit({ key: 'a', resources: ['servers'], run: async () => 'ok' })).outcome).toBe('applied')
    refresh.reject(new Error('read failed'))
    await Promise.resolve()
    expect(onRefreshError).toHaveBeenCalledOnce()
  })

  it('ignores old completions after reset, including rollback and refresh', async () => {
    const request = deferred<void>()
    const undo = vi.fn()
    const refresh = vi.fn()
    const coordinator = createMutationCoordinator({ refresh })
    const result = coordinator.submit({ key: 'a', resources: ['servers'], optimistic: () => undo, run: () => request.promise })
    await Promise.resolve()
    coordinator.reset()
    request.reject(httpError(409))
    expect((await result).outcome).toBe('superseded')
    expect(undo).not.toHaveBeenCalled()
    expect(refresh).not.toHaveBeenCalled()
    expect(coordinator.state()).toEqual({ pending: 0, unknown: 0 })
  })

  it('does not mistake an undefined rejection for success', async () => {
    const coordinator = createMutationCoordinator()
    expect((await coordinator.submit({ key: 'a', run: () => Promise.reject(undefined) })).outcome).toBe('unknown')
  })

  it('tracks in-flight operations so a page can show saving state', async () => {
    const pending = deferred<string>()
    const seen: number[] = []
    const coordinator = createMutationCoordinator({ onStateChange: state => seen.push(state.pending) })
    const running = coordinator.submit({ key: 'server:1', run: () => pending.promise })
    expect(coordinator.state().pending).toBe(1)
    pending.resolve('done')
    await running
    expect(coordinator.state().pending).toBe(0)
    expect(seen).toEqual([1, 0])
  })
})

describe('mutation outcome wording', () => {
  it('reads as a failure only when the server actually refused', async () => {
    const coordinator = createMutationCoordinator()
    const refused = await coordinator.submit({ key: 'a', run: async () => { throw httpError(409) } })
    expect(describeMutationOutcome(refused, '删除证书')).toContain('删除证书失败')

    const lost = await coordinator.submit({ key: 'b', run: async () => { throw new Error('服务暂时不可用') } })
    const text = describeMutationOutcome(lost, '删除证书')
    expect(text).toContain('删除证书的结果未知')
    expect(text).toContain('服务暂时不可用')
    expect(text).not.toContain('失败')
  })
})
