export type SubscriptionRelay = {
  id: number
  name: string
  public_url: string
  status: 'pending' | 'online' | 'offline' | 'updating' | 'failed' | 'uninstalled' | string
  enrolled: boolean
  active: boolean
  version: string
  build: string
  commit: string
  os: string
  arch: string
  service_manager: string
  update_target_version?: string
  update_target_build?: string
  update_requested_at?: string
  last_update_error?: string
  last_seen_at?: string
  enrollment_expires_at?: string
  created_at: string
  updated_at: string
}

export type SubscriptionRelayAction = 'install' | 'update' | 'uninstall'

function shellQuote(value: string) {
  return `'${String(value).replace(/'/g, `'\\''`)}'`
}

export function subscriptionRelayCommand(options: {
  controllerURL: string
  version: string
  action: SubscriptionRelayAction
  enrollmentToken?: string
}) {
  const controllerURL = options.controllerURL.replace(/\/+$/, '')
  const download = `curl -fsSL ${shellQuote(`${controllerURL}/install/subscription-relay.sh`)}`
  const values = [`VERSION=${shellQuote(options.version || 'latest')}`]
  if (options.action === 'install') {
    values.push(`OBOARD_CONTROLLER_URL=${shellQuote(controllerURL)}`)
    values.push(`OBOARD_SUBSCRIPTION_RELAY_ENROLLMENT_TOKEN=${shellQuote(options.enrollmentToken || '')}`)
  } else {
    values.push(`OBOARD_ACTION=${shellQuote(options.action)}`)
  }
  return `${download} | env ${values.join(' ')} /bin/sh`
}

export function subscriptionRelayStatus(status: string) {
  const states: Record<string, { label: string; tone: string }> = {
    pending: { label: '待接入', tone: 'warning' },
    online: { label: '在线', tone: 'ok' },
    offline: { label: '离线', tone: 'danger' },
    updating: { label: '等待更新', tone: 'warning' },
    failed: { label: '更新失败', tone: 'danger' },
    uninstalled: { label: '已卸载', tone: 'warning' },
  }
  return states[status] || { label: status || '未知', tone: 'warning' }
}
