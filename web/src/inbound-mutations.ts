type InboundRecord = { id: number; protocol: string; config_json: string }
type InboundClient = { request: (path: string, init?: RequestInit) => Promise<any> }

export class InboundModeSaveError extends Error {
  readonly parametersSaved = true
  readonly unknownOutcome: boolean

  constructor(error: unknown) {
    super(String((error as Error)?.message || error))
    this.name = 'InboundModeSaveError'
    this.unknownOutcome = Boolean((error as { unknownOutcome?: boolean })?.unknownOutcome)
  }
}

export async function saveInboundRecord(client: InboundClient, body: InboundRecord, current: InboundRecord, onSaved: (result: Record<string, any>) => void): Promise<void> {
  const path = `/inbounds/${body.id}`
  const isSnell = current.protocol === 'snell' && body.protocol === 'snell'
  const currentMode = isSnell ? JSON.parse(current.config_json || '{}').listener_mode || 'per_identity_port' : ''
  const targetConfig = isSnell ? JSON.parse(body.config_json || '{}') : {}
  const targetMode = targetConfig.listener_mode || 'per_identity_port'
  const switching = isSnell && currentMode !== targetMode
  const payload = switching ? {
    ...body,
    config_json: JSON.stringify({ ...targetConfig, listener_mode: currentMode }),
  } : body
  const saved = await client.request(path, { method: 'PATCH', body: JSON.stringify(payload) })
  onSaved(saved)
  if (!switching) return

  try {
    const preview = await client.request(`${path}/listener-mode/preview`, { method: 'POST', body: JSON.stringify({ listener_mode: targetMode }) })
    if (!preview.capability_ready) throw new Error('当前 Agent / 内核不支持所选监听方式，请升级后重试。')
    if (!preview.preview_digest) throw new Error('监听方式校验结果无效，请重新保存。')
    const applied = await client.request(`${path}/listener-mode/apply`, { method: 'POST', body: JSON.stringify({ listener_mode: targetMode, preview_digest: preview.preview_digest }) })
    onSaved({ ...applied, inbound: {
      ...saved.inbound,
      ...applied.inbound,
      config_json: JSON.stringify({ ...JSON.parse(saved.inbound.config_json || '{}'), listener_mode: targetMode }),
    } })
  } catch (error) {
    throw new InboundModeSaveError(error)
  }
}
