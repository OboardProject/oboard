function main(run) {
  const server = oboard.servers.get(env.TARGET_SERVER)
  log.info(`${server.name} is ${server.online ? 'online' : 'offline'}`)
  return { server: server.name, online: server.online, trigger: run.trigger }
}
