export type DashboardTrafficServer = {
  traffic_upload_bytes?: unknown
  traffic_download_bytes?: unknown
}

function trafficBytes(value: unknown): number {
  const bytes = Number(value)
  return Number.isFinite(bytes) && bytes > 0 ? bytes : 0
}

export function dashboardServerTrafficBytes(servers: DashboardTrafficServer[]): number {
  return servers.reduce((total, server) => (
    total
      + trafficBytes(server.traffic_upload_bytes)
      + trafficBytes(server.traffic_download_bytes)
  ), 0)
}
