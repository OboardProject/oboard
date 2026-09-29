// Environment values arrive already validated and typed. A server-type value
// is a stable server ID, not a permission: reading it still needs the grant.
function main(run) {
  const summary = {
    label: env.LABEL,
    retries: env.RETRIES,
    threshold: env.THRESHOLD,
    verbose: env.VERBOSE,
    mode: env.MODE,
    window: env.has('ADVANCED_WINDOW') ? env.ADVANCED_WINDOW : null,
    watched: env.CHANNELS || [],
    extra: env.EXTRA,
    token: String(env.API_TOKEN),
    custom: env.names().filter(name => !['LABEL', 'NOTES', 'RETRIES', 'THRESHOLD', 'VERBOSE', 'MODE', 'ADVANCED_WINDOW', 'CHANNELS', 'PRIMARY', 'PEERS', 'DOCS_URL', 'EXTRA', 'API_TOKEN'].includes(name))
      .map(name => ({ name, type: env.type(name), raw: env.raw(name) })),
  }
  const ids = [env.PRIMARY].concat(env.PEERS || [])
  summary.servers = ids.map(id => oboard.servers.get(id).name)
  const previous = oboard.state.get('last_run')
  oboard.state.compareAndSwap('last_run', previous.version, { at: run.scheduled_at || null, run: run.run_id })
  if (env.VERBOSE) log.debug('environment', summary)
  return summary
}
