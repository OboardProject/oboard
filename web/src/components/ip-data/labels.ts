export const ipDataProviderName: Record<string, string> = {
  ipinfo: 'IPinfo',
  ipapi_is: 'ipapi.is',
  ipwhois: 'IPWhois',
  proxycheck: 'Proxycheck',
  abuseipdb: 'AbuseIPDB',
  ip2region: '离线库',
  nexttrace: 'NextTrace',
  nexttrace_controller: 'NextTrace（主控）',
}

export const ipDataStatusText: Record<string, string> = {
  ok: '已获取',
  degraded: '已降级',
  offline: '仅离线库',
  partial: '部分可用',
  unknown: '未知',
  not_configured: '未配置',
  unavailable: '查询失败',
  quota_exhausted: '额度用完',
  invalid_key: 'Key 无效',
  skipped: '未查询',
}

export const ipDataStatusTone: Record<string, string> = { ok: 'online', degraded: 'warning', offline: 'warning', partial: 'warning', unavailable: 'offline', quota_exhausted: 'offline', invalid_key: 'offline' }
