import { describe, expect, it, vi } from 'vitest'
import { InboundModeSaveError, saveInboundRecord } from './inbound-mutations'

const current = { id: 7, protocol: 'snell', config_json: '{"version":4,"listener_mode":"per_identity_port"}' }
const target = { ...current, port: 7177, config_json: '{"version":6,"listener_mode":"shared_port"}' }

describe('saving an inbound listener mode', () => {
  it('previews the newly saved parameters and automatically applies their mode with a fresh digest', async () => {
    let stored = { ...current, port: 6160 }
    const onSaved = vi.fn()
    const request = vi.fn(async (path: string, init?: RequestInit) => {
      const input = JSON.parse(String(init?.body))
      if (init?.method === 'PATCH') {
        expect(input.port).toBe(7177)
        expect(JSON.parse(input.config_json)).toEqual({ version: 6, listener_mode: 'per_identity_port' })
        stored = input
        return { inbound: stored }
      }
      if (path.endsWith('/preview')) {
        expect(JSON.parse(stored.config_json).version).toBe(6)
        expect(onSaved).toHaveBeenCalledWith({ inbound: stored })
        return { capability_ready: true, preview_digest: 'saved-v6-preview', target_mode: input.listener_mode }
      }
      expect(input).toEqual({ listener_mode: 'shared_port', preview_digest: 'saved-v6-preview' })
      return { inbound: { id: 7, listener_mode: input.listener_mode }, requires_deployment: true }
    })
    await saveInboundRecord({ request }, target, current, onSaved)
    expect(request.mock.calls.map(([path]) => path)).toEqual(['/inbounds/7', '/inbounds/7/listener-mode/preview', '/inbounds/7/listener-mode/apply'])
    expect(onSaved.mock.calls.at(-1)?.[0]).toMatchObject({ inbound: { port: 7177, config_json: '{"version":6,"listener_mode":"shared_port"}' }, requires_deployment: true })
  })

  it('uses the ordinary save when the mode stays unchanged, including legacy inbounds without a mode', async () => {
    const legacy = { ...current, config_json: '{"version":4}' }
    const request = vi.fn(async () => ({ inbound: legacy }))
    const onSaved = vi.fn()
    await saveInboundRecord({ request }, legacy, current, onSaved)
    expect(request).toHaveBeenCalledTimes(1)
    expect(JSON.parse(request.mock.calls[0][1].body as string)).toEqual(legacy)
  })

  it('does not preview or apply after a refused parameter save', async () => {
    const request = vi.fn(async () => { throw new Error('port conflict') })
    const onSaved = vi.fn()
    await expect(saveInboundRecord({ request }, target, current, onSaved)).rejects.toThrow('port conflict')
    expect(request).toHaveBeenCalledTimes(1)
    expect(onSaved).not.toHaveBeenCalled()
  })

  it.each([
    { preview: { capability_ready: false, preview_digest: 'unsupported' }, message: '不支持所选监听方式' },
    { preview: { capability_ready: true }, message: '校验结果无效' },
  ])('keeps saved parameters visible but refuses an invalid switch: $message', async ({ preview, message }) => {
    const saved = { inbound: { ...target, config_json: '{"version":6,"listener_mode":"per_identity_port"}' } }
    const request = vi.fn().mockResolvedValueOnce(saved).mockResolvedValueOnce(preview)
    const onSaved = vi.fn()
    await expect(saveInboundRecord({ request }, target, current, onSaved)).rejects.toThrow(message)
    expect(request).toHaveBeenCalledTimes(2)
    expect(onSaved).toHaveBeenCalledExactlyOnceWith(saved)
  })

  it('preserves an unknown apply outcome without reporting the target mode as saved', async () => {
    const saved = { inbound: current }
    const request = vi.fn().mockResolvedValueOnce(saved).mockResolvedValueOnce({ capability_ready: true, preview_digest: 'fresh' })
      .mockRejectedValueOnce(Object.assign(new Error('提交结果未知'), { unknownOutcome: true }))
    const onSaved = vi.fn()
    const result = saveInboundRecord({ request }, target, current, onSaved)
    await expect(result).rejects.toBeInstanceOf(InboundModeSaveError)
    await expect(result).rejects.toMatchObject({ parametersSaved: true, unknownOutcome: true })
    expect(onSaved).toHaveBeenCalledExactlyOnceWith(saved)
  })
})
