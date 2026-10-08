import React, { useMemo, useState } from 'react'
import { ShieldAlert, ShieldCheck } from 'lucide-react'
import { Badge } from '../../components/ui/badge'
import { Button } from '../../components/ui/button'
import { Switch } from '../../components/ui/switch'
import { SearchableMultiSelect } from '../../components/ui/SearchableMultiSelect'
import { groupLabels, riskLabels } from './domain'
import type { Grant, GrantView, InstallationDetail, NotificationChannelOption, PermissionLine, PlanOption, ServerOption, UserOption } from './types'

// initialGrant proposes the narrowest useful grant: nothing is selected for
// resource-scoped capabilities unless the current grant already holds it.
function initialGrant(permissions: PermissionLine[], current?: GrantView): Grant {
  const out: Grant = { capabilities: {} }
  for (const line of permissions) {
    const existing = current?.capabilities?.[line.capability]
    if (existing) out.capabilities[line.capability] = { ...existing }
  }
  return out
}

export function GrantEditor({ detail, grant, servers, channels, users, plans, envServerIDs, busy, onSave, onRevoke }: {
  detail: InstallationDetail
  grant?: GrantView
  servers: ServerOption[]
  channels: NotificationChannelOption[]
  users: UserOption[]
  plans: PlanOption[]
  envServerIDs: string[]
  busy?: boolean
  onSave: (grant: Grant) => void
  onRevoke?: () => void
}) {
  const [draft, setDraft] = useState<Grant>(() => initialGrant(detail.permissions, grant))
  const hosts = detail.http_hosts || []
  const serverOptions = useMemo(() => servers.map(server => ({ value: server.id, label: `${server.name}${server.online ? '' : '（离线）'}`, keywords: server.region_code })), [servers])
  const toggle = (line: PermissionLine, on: boolean) => {
    const next = { ...draft.capabilities }
    if (!on) delete next[line.capability]
    else if (line.resource === 'server') next[line.capability] = { servers: envServerIDs.map(Number).filter(Boolean) }
    else if (line.resource === 'http_host') next[line.capability] = { hosts: [...hosts] }
    else if (line.resource === 'user') next[line.capability] = { users: [] }
    else if (line.resource === 'plan') next[line.capability] = { plans: [] }
    else next[line.capability] = {}
    setDraft({ capabilities: next })
  }
  const setScope = (capability: string, patch: Partial<Grant['capabilities'][string]>) =>
    setDraft({ capabilities: { ...draft.capabilities, [capability]: { ...draft.capabilities[capability], ...patch } } })
  const missing = detail.permissions.filter(line => {
    const scope = draft.capabilities[line.capability]
    if (!scope) return false
    if (line.resource === 'server') return !scope.servers?.length
    if (line.resource === 'http_host') return !scope.hosts?.length
    if (line.resource === 'notification_channel') return !scope.channels?.length
    if (line.resource === 'user') return !scope.users?.length
    if (line.resource === 'plan') return !scope.plans?.length
    return false
  })
  const outsideGrant = envServerIDs.filter(id => !Object.values(draft.capabilities).some(scope => scope.servers?.includes(Number(id))))

  return <div className="plugin-grant-editor">
    <div className="plugin-callout">
      <ShieldCheck size={16} aria-hidden="true" />
      <p>有效权限 = 清单声明 ∩ 本授权 ∩ 资源范围 ∩ 运行策略，每次调用都会重新检查。环境变量中选择的服务器不是授权。</p>
    </div>
    {grant && !grant.current && <div className="plugin-callout warning"><ShieldAlert size={16} aria-hidden="true" /><p>当前授权属于旧版本，保存后才会用于新版本。</p></div>}
    <ul className="plugin-permission-list">
      {detail.permissions.map(line => {
        const scope = draft.capabilities[line.capability]
        return <li key={line.capability} className="plugin-permission-item">
          <div className="plugin-permission-head">
            <Switch checked={Boolean(scope)} onChange={on => toggle(line, on)} ariaLabel={`授权 ${line.label}`} disabled={busy} />
            <div className="min-w-0">
              <strong>{line.label}</strong>
              <p className="plugin-env-help">{line.description}</p>
              <code>{line.capability}</code>
            </div>
            <Badge variant={line.risk === 'high' ? 'destructive' : line.risk === 'medium' ? 'warning' : 'secondary'}>{riskLabels[line.risk]}</Badge>
          </div>
          {scope && line.resource === 'server' && <div className="plugin-scope">
            <span>允许的服务器</span>
            <SearchableMultiSelect ariaLabel={`${line.label}的服务器范围`} placeholder="选择服务器" searchPlaceholder="搜索服务器" options={serverOptions}
              value={(scope.servers || []).map(String)} onChange={next => setScope(line.capability, { servers: next.map(Number) })} />
          </div>}
          {scope && line.resource === 'http_host' && <div className="plugin-scope">
            <span>允许的主机</span>
            <div className="plugin-chip-list">
              {hosts.map(host => {
                const checked = scope.hosts?.includes(host) ?? false
                return <label key={host} className="plugin-chip">
                  <input type="checkbox" checked={checked} disabled={busy} onChange={event => setScope(line.capability, { hosts: event.target.checked ? [...(scope.hosts || []), host] : (scope.hosts || []).filter(item => item !== host) })} />
                  <code>{host}</code>
                </label>
              })}
            </div>
          </div>}
          {scope && line.resource === 'user' && <div className="plugin-scope">
            <span>允许的用户</span>
            <SearchableMultiSelect ariaLabel={`${line.label}的用户范围`} placeholder="选择用户" searchPlaceholder="搜索用户" options={users.map(user => ({ value: user.id, label: user.nickname ? `${user.username}（${user.nickname}）` : user.username }))}
              value={(scope.users || []).map(String)} onChange={next => setScope(line.capability, { users: next.map(Number) })} />
          </div>}
          {scope && line.resource === 'plan' && <div className="plugin-scope">
            <span>允许的套餐</span>
            <SearchableMultiSelect ariaLabel={`${line.label}的套餐范围`} placeholder="选择套餐" searchPlaceholder="搜索套餐" options={plans.map(plan => ({ value: plan.id, label: plan.enabled ? plan.name : `${plan.name}（已停用）` }))}
              value={(scope.plans || []).map(String)} onChange={next => setScope(line.capability, { plans: next.map(Number) })} />
          </div>}
          {scope && line.resource === 'notification_channel' && <div className="plugin-scope">
            <span>允许的通知渠道</span>
            <SearchableMultiSelect ariaLabel="通知渠道范围" placeholder="选择通知渠道" options={channels.map(channel => ({ value: String(channel.id), label: channel.name || `渠道 #${channel.id}` }))}
              value={(scope.channels || []).map(String)} onChange={next => setScope(line.capability, { channels: next.map(Number) })} />
          </div>}
        </li>
      })}
    </ul>
    {!detail.permissions.length && <p className="text-sm text-muted-foreground">此插件没有声明任何能力。</p>}
    <div className="plugin-forbidden">
      <strong>插件永远不能获得</strong>
      <p>{(detail.forbidden || []).join('、')}</p>
    </div>
    {outsideGrant.length > 0 && <p className="plugin-field-error">环境中选择的 {outsideGrant.length} 台服务器不在任何授权范围内，运行时会被拒绝。</p>}
    {missing.length > 0 && <p className="plugin-field-error">{missing.map(line => groupLabels[line.group] || line.label).join('、')} 尚未选择资源范围。</p>}
    <div className="plugin-actions">
      {onRevoke && grant && <Button type="button" variant="ghost" className="danger-text" disabled={busy} onClick={onRevoke}>撤销授权</Button>}
      <Button type="button" busy={busy} disabled={missing.length > 0} onClick={() => onSave(draft)}>保存授权</Button>
    </div>
  </div>
}
