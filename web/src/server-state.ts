type ServerSnapshotItem = { id: number }

export function upsertServerSnapshot<T extends ServerSnapshotItem>(servers: readonly T[], server: T): T[] {
  return [server, ...servers.filter(item => item.id !== server.id)]
    .sort((left, right) => right.id - left.id)
}

export function removeServerSnapshot<T extends ServerSnapshotItem>(servers: readonly T[], serverID: number): T[] {
  return servers.filter(server => server.id !== serverID)
}
