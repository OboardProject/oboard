// Secrets are SecretRef objects: they can be used for auth and HMAC keys,
// but the plugin never sees their value and they are redacted from results.
async function main(run) {
  const report = env.SERVERS.map(id => {
    const server = oboard.servers.get(id)
    const health = oboard.servers.health(id)
    return { id, name: server.name, online: server.online, sync: health.config_sync, stale: health.stale }
  })
  const body = JSON.stringify({ run: run.run_id, generated_at: run.scheduled_at || null, servers: report })
  const signature = oboard.crypto.hmacSha256(env.SIGNING_KEY, body, { output: 'base64' })
  try {
    const response = oboard.http.request({
      url: env.ENDPOINT,
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Signature': signature },
      auth: { type: 'bearer', secret: env.API_TOKEN },
      body,
      timeout_ms: 10000,
    })
    if (!response.ok) throw new OBoardError('HTTP_FAILED', `status ${response.status}`)
    return { reported: report.length, status: response.status }
  } catch (error) {
    oboard.notifications.send({ title: '状态上报失败', body: `${error.code || 'ERROR'}: ${error.message}` })
    throw error
  }
}
