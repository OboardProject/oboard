export type ServerTimeSnapshot = {
  time_check_status?: string
  time_check_error?: string
  time_unsupported_paths?: string[]
}

export type ServerTimeIssue = {
  kind: 'skewed' | 'unavailable' | 'error' | 'unsupported'
  summary: string
  tone: 'warning' | 'danger'
}

export function getServerTimeIssue(server: ServerTimeSnapshot): ServerTimeIssue | null {
  const status = String(server.time_check_status || '').trim().toLowerCase()
  const error = String(server.time_check_error || '').trim()
  const unsupportedPaths = Array.isArray(server.time_unsupported_paths)
    ? server.time_unsupported_paths.filter(path => String(path).trim())
    : []

  if (status === 'unavailable') return { kind: 'unavailable', summary: '时间检测失败', tone: 'danger' }
  if (status === 'skewed') return { kind: 'skewed', summary: '时间偏差过大', tone: 'warning' }
  if (error) return { kind: 'error', summary: '时间同步异常', tone: 'danger' }
  if (unsupportedPaths.length) return { kind: 'unsupported', summary: '部分路径时间受限', tone: 'warning' }
  return null
}
