// Ping and trace run natively on the chosen server's Agent. Targets must be
// public; a private or metadata address is refused before any probe starts.
function probe(serverId) {
  const server = oboard.servers.get(serverId)
  if (!server.online) return { server: server.name, skipped: 'offline' }
  const ping = oboard.network.ping({ server_id: serverId, target: env.TARGET, count: 5 })
  const traceOptions = { server_id: serverId, target: env.TARGET, mode: env.MODE, max_hops: 20 }
  if (env.MODE === 'tcp') traceOptions.port = env.PORT
  const trace = oboard.network.trace(traceOptions)
  const path = trace.hops.map(hop => hop.addresses[0] || '*')
  return { server: server.name, loss: ping.loss_percent, avg_rtt_ms: ping.avg_rtt_ms, reached: trace.reached, path }
}

function main(run) {
  const sources = run.trigger === 'event' ? [run.event.server_id].filter(id => env.SOURCES.includes(id)) : env.SOURCES
  const results = []
  const changes = []
  for (const id of sources) {
    let result
    try {
      result = probe(id)
    } catch (error) {
      if (error.code === 'SERVER_OFFLINE' || error.code === 'SERVER_BUSY') {
        results.push({ server: id, skipped: error.code })
        continue
      }
      throw error
    }
    results.push(result)
    if (result.skipped) continue
    const key = `path:${id}`
    const previous = oboard.state.get(key)
    const joined = result.path.join('>')
    if (previous.exists && previous.value !== joined) changes.push(`${result.server}: 路径变化`)
    if (result.loss >= env.LOSS_ALERT) changes.push(`${result.server}: 丢包 ${result.loss}%`)
    oboard.state.set(key, joined)
  }
  if (changes.length) oboard.notifications.send({ title: `${env.TARGET} 路由监控`, body: changes.join('\n') })
  return { target: env.TARGET, results, changes }
}
