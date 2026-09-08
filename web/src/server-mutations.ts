type ServerRecord = { id: number; name: string }
type ServerClient = { request: (path: string, init?: RequestInit) => Promise<any> }

export class ServerMutationUncertainError extends Error {
  constructor() {
    super('请求中断，暂时无法确认服务器状态。请刷新列表核对后再操作。')
    this.name = 'ServerMutationUncertainError'
  }
}

function ambiguousResponse(error: unknown): boolean {
  const status = Number((error as { status?: number })?.status || 0)
  return status === 0 || status >= 500 || status === 408
}

export async function createServerRecord<T extends ServerRecord>(client: ServerClient, payload: { name: string }, existing: readonly T[]): Promise<{ server: T; recovered: boolean }> {
  try {
    const result = await client.request('/servers', { method: 'POST', body: JSON.stringify(payload) })
    if (!result.server?.id) throw new Error('创建响应缺少服务器数据')
    return { ...result, server: result.server, recovered: false }
  } catch (error) {
    if (!ambiguousResponse(error)) throw error
    let servers: T[]
    try {
      const result = await client.request('/servers')
      if (!Array.isArray(result.servers)) throw new Error('服务器列表不可用')
      servers = result.servers
    } catch {
      throw new ServerMutationUncertainError()
    }
    const name = payload.name.trim().toLowerCase()
    const found = servers.filter(server => server.name.trim().toLowerCase() === name && !existing.some(item => item.id === server.id))
    if (found.length === 1) return { server: found[0], recovered: true }
    throw error
  }
}

export async function deleteServerRecord(client: ServerClient, serverID: number): Promise<void> {
  const path = `/servers/${serverID}`
  try {
    await client.request(path, { method: 'DELETE' })
  } catch (error) {
    if (!ambiguousResponse(error) && Number((error as { status?: number })?.status) !== 404) throw error
    try {
      const result = await client.request(path)
      if (result.server?.id !== serverID) throw new ServerMutationUncertainError()
    } catch (lookupError) {
      if (Number((lookupError as { status?: number })?.status) === 404) return
      throw new ServerMutationUncertainError()
    }
    throw error
  }
}
