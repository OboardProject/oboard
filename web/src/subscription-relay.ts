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

export function subscriptionBaseURL(relayURL: string, controllerURL: string) {
  return (relayURL.trim() || controllerURL.trim()).replace(/\/+$/, '')
}

export function subscriptionRelayDomain(publicURL: string) {
  try {
    return new URL(publicURL).hostname
  } catch {
    return ''
  }
}

export function subscriptionRelayPublicURL(domain: string, basePath: string) {
  const value = domain.trim()
  if (!value || /[\s/@:?#]/.test(value)) return ''
  try {
    const target = new URL(`https://${value}`)
    const labels = target.hostname.split('.')
    if (target.hostname === '' || labels.length < 2 || labels.every(label => /^\d+$/.test(label))) return ''
    if (labels.some(label => !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/i.test(label))) return ''
    const path = basePath.trim() === '/' ? '' : basePath.trim().replace(/\/+$/, '')
    return `https://${target.hostname}${path}`
  } catch {
    return ''
  }
}

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
  const requestedVersion = options.version || 'latest'
  const releaseVersion = requestedVersion.includes('dev') ? 'dev' : requestedVersion
  const values = [`VERSION=${shellQuote(releaseVersion)}`]
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
