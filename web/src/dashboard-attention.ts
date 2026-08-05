import { groupTasksForTimeline, serverTaskStatusSummary } from './task-groups'

export type DashboardAttention = {
  parts: string[]
  fingerprint: string
}

export function getDashboardAttention(data: any): DashboardAttention {
  const summary = data.summary || {}
  const servers = data.servers || []
  const totalServers = Number(summary.servers_total ?? summary.servers ?? summary.server_count ?? servers.length ?? 0)
  const onlineServers = Number(summary.servers_online ?? summary.online_agents ?? summary.online_servers ?? servers.filter((server: any) => server.status === 'online').length ?? 0)
  const offlineServers = Math.max(0, totalServers - onlineServers)
  const latestTaskGroup = groupTasksForTimeline(data.agent_tasks || [], type => type)[0]
  const failedTasks = latestTaskGroup ? serverTaskStatusSummary(latestTaskGroup.tasks).failed : 0
  const offlineSnapshot = servers
    .filter((server: any) => server.status !== 'online')
    .map((server: any) => `${server.id}:${server.status || 'offline'}:${server.last_seen_at || server.updated_at || ''}`)
    .sort()

  const parts: string[] = []
  if (offlineServers > 0) parts.push(`${offlineServers} 台服务器离线`)
  if (failedTasks > 0) parts.push(`${failedTasks} 个任务失败`)

  return {
    parts,
    fingerprint: parts.length > 0
      ? JSON.stringify({
          offlineServers,
          offlineSnapshot,
          failedTasks,
          failedTaskGroup: failedTasks > 0 && latestTaskGroup
            ? `${latestTaskGroup.kind}:${latestTaskGroup.id}:${latestTaskGroup.updated_at}:${failedTasks}`
            : '',
        })
      : '',
  }
}
