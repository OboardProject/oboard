export type DNSBenchmarkHistoryRecord = {
  id: number
  server_id: number
  created_at: string
}

export type DNSBenchmarkHistoryServer = {
  id: number
  name: string
}

export type DNSBenchmarkServerGroup<T extends DNSBenchmarkHistoryRecord> = {
  serverID: number
  serverName: string
  records: T[]
  latest: T
}

function compareRecordsDesc(a: DNSBenchmarkHistoryRecord, b: DNSBenchmarkHistoryRecord) {
  const aTime = Date.parse(a.created_at)
  const bTime = Date.parse(b.created_at)
  if (Number.isFinite(aTime) && Number.isFinite(bTime) && aTime !== bTime) return bTime - aTime
  return b.id - a.id
}

export function groupDNSBenchmarkResults<T extends DNSBenchmarkHistoryRecord>(
  records: readonly T[],
  servers: readonly DNSBenchmarkHistoryServer[],
): DNSBenchmarkServerGroup<T>[] {
  const serverNames = new Map(servers.map(server => [Number(server.id), server.name]))
  const recordsByServer = new Map<number, T[]>()

  records.forEach(record => {
    const serverID = Number(record.server_id)
    recordsByServer.set(serverID, [...(recordsByServer.get(serverID) || []), record])
  })

  return Array.from(recordsByServer.entries())
    .map(([serverID, serverRecords]) => {
      const sortedRecords = [...serverRecords].sort(compareRecordsDesc)
      return {
        serverID,
        serverName: serverNames.get(serverID) || `#${serverID}`,
        records: sortedRecords,
        latest: sortedRecords[0],
      }
    })
    .sort((a, b) => compareRecordsDesc(a.latest, b.latest))
}

export function filterDNSBenchmarkGroups<T extends DNSBenchmarkHistoryRecord>(
  groups: readonly DNSBenchmarkServerGroup<T>[],
  query: string,
) {
  const needle = query.trim().toLocaleLowerCase()
  if (!needle) return [...groups]

  return groups.filter(group => {
    const serverID = String(group.serverID)
    return group.serverName.toLocaleLowerCase().includes(needle)
      || serverID.includes(needle.replace(/^#/, ''))
  })
}
