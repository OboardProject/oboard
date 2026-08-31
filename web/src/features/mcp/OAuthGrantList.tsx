import React, { useCallback, useEffect, useState } from 'react'
import { ShieldCheck, Trash2, KeyRound, Settings2 } from 'lucide-react'
import { MCPPrivilegedAccess } from '../../components/mcp/MCPPrivilegedAccess'
import { listGrants, revokeGrant, revokeOfflineAccess, type RequestV2 } from './api'
import type { OAuthGrant, ToastTone } from './types'

interface OAuthGrantListProps {
  request: (path: string, init?: RequestInit) => Promise<any>
  requestV2: RequestV2
  notify: (message: string, tone?: ToastTone) => void
  confirm: (options: { title: string; message: string; confirmText?: string; tone?: 'danger' }) => Promise<boolean>
}

function formatTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function inheritedRoleLabel(grant: OAuthGrant) {
  if (grant.effective_role === 'admin') return '继承管理员权限'
  if (grant.effective_role === 'operator') return '继承操作员权限'
  return '继承只读权限'
}

function roleHeadline(grant: OAuthGrant) {
  if (grant.effective_role === 'admin') return '管理员'
  if (grant.effective_role === 'operator') return '操作员'
  return '只读'
}

export function OAuthGrantList({ request, requestV2, notify, confirm }: OAuthGrantListProps) {
  const [grants, setGrants] = useState<OAuthGrant[]>([])
  const [working, setWorking] = useState('')
  const [privilegedGrant, setPrivilegedGrant] = useState<OAuthGrant | null>(null)
  const [detailGrant, setDetailGrant] = useState<OAuthGrant | null>(null)

  const load = useCallback(async () => {
    try {
      const items = await listGrants(requestV2)
      setGrants(items.filter((grant) => !grant.revoked_at && grant.status !== 'revoked'))
    } catch (error: any) {
      notify?.(error?.message || '已授权访问列表加载失败', 'error')
    }
  }, [requestV2, notify])

  useEffect(() => { void load() }, [load])

  const revoke = async (grant: OAuthGrant) => {
    const confirmed = await confirm({
      title: `撤销 ${grant.client_name || grant.client_id} 的访问？`,
      message: '这会撤销该用户对此 MCP 客户端的全部授权，Access Token 与 Refresh Token 会立即失效。',
      confirmText: '撤销授权',
      tone: 'danger',
    })
    if (!confirmed) return
    setWorking(`grant-revoke-${grant.id}`)
    try {
      await revokeGrant(requestV2, grant.id)
      setGrants((current) => current.filter((item) => item.id !== grant.id))
      notify?.('授权已撤销', 'success')
    } catch (error: any) {
      notify?.(error?.message || '撤销失败', 'error')
    } finally {
      setWorking('')
    }
  }

  const revokeOffline = async (grant: OAuthGrant) => {
    const confirmed = await confirm({
      title: '撤销离线访问？',
      message: '现有 Refresh Token 会立即失效；Access Token 会在约 15 分钟内自然过期。',
      confirmText: '撤销离线访问',
      tone: 'danger',
    })
    if (!confirmed) return
    setWorking(`grant-offline-${grant.id}`)
    try {
      await revokeOfflineAccess(requestV2, grant.id)
      await load()
      notify?.('离线访问已撤销', 'success')
    } catch (error: any) {
      notify?.(error?.message || '撤销离线访问失败', 'error')
    } finally {
      setWorking('')
    }
  }

  return (
    <section className="settings-card automation-wide automation-grants">
      <div className="settings-card-head automation-section-head"><div><h3>已授权访问</h3><p className="muted">每条记录代表一个用户对一个 MCP 客户端的持续授权。重新登录和 Token 刷新不会重复创建授权。</p></div></div>
      <div className="automation-list">{grants.length ? grants.map((grant) => {
        const reconsent = grant.status === 'needs_reconsent'
        return <div className="automation-row" key={grant.id}>
          <div>
            <div className="automation-row-title"><strong>{grant.client_name || grant.client_id}</strong><span className={`automation-state ${reconsent ? '' : 'is-enabled'}`}>{reconsent ? '待重新授权' : '有效'}</span><span className="automation-state">{inheritedRoleLabel(grant)}</span>{grant.offline_access && <span className="automation-state">可离线刷新</span>}</div>
            <span>{grant.username || `用户 ${grant.user_id}`} · {roleHeadline(grant)}</span>
            <small>首次授权 {formatTime(grant.created_at)} · 最近授权 {grant.last_authorized_at ? formatTime(grant.last_authorized_at) : '暂无'} · 最近使用 {grant.last_used_at ? formatTime(grant.last_used_at) : '暂无'}{grant.offline_access ? ` · 离线会话 ${grant.active_refresh_families ?? 0}` : ''}</small>
          </div>
          <div>
            <button type="button" className="ghost icon-button" onClick={() => setDetailGrant(grant)} title="管理" aria-label={`${grant.client_name || grant.id} 管理`}><Settings2 size={15} /></button>
            <button type="button" className="ghost icon-button" onClick={() => setPrivilegedGrant(grant)} title="敏感服务器访问" aria-label={`${grant.client_name || grant.id} 敏感服务器访问`}><KeyRound size={15} /></button>
            {grant.offline_access ? <button type="button" className="ghost icon-button" disabled={working === `grant-offline-${grant.id}`} onClick={() => void revokeOffline(grant)} title="撤销离线访问" aria-label={`撤销 ${grant.client_name || grant.id} 离线访问`}><ShieldCheck size={15} /></button> : null}
            <button type="button" className="ghost icon-button danger-text" disabled={working === `grant-revoke-${grant.id}`} onClick={() => void revoke(grant)} title="撤销授权" aria-label={`撤销 ${grant.client_name || grant.id}`}><Trash2 size={15} /></button>
          </div>
        </div>
      }) : <div className="automation-empty"><ShieldCheck size={20} /><span>暂无已授权访问。客户端首次登录并完成同意后会显示在这里。</span></div>}</div>
      {detailGrant ? <div className="settings-card automation-dialog-like"><div className="settings-card-head"><div><h3>技术详情</h3><p className="muted">{detailGrant.client_name || detailGrant.client_id} · {detailGrant.username || detailGrant.user_id}</p></div><button type="button" className="ghost" onClick={() => setDetailGrant(null)}>关闭</button></div><div className="automation-list"><div className="automation-row"><div><span>Grant ID</span><small>{detailGrant.id}</small></div></div><div className="automation-row"><div><span>Principal ID</span><small>{detailGrant.principal_id || '—'}</small></div></div><div className="automation-row"><div><span>Policy Version</span><small>{detailGrant.policy_version}</small></div></div><div className="automation-row"><div><span>Consent Version</span><small>{detailGrant.consent_version}</small></div></div><div className="automation-row"><div><span>活动 Access Token</span><small>{detailGrant.active_access_tokens ?? 0}</small></div></div><div className="automation-row"><div><span>活动刷新会话</span><small>{detailGrant.active_refresh_families ?? 0}</small></div></div></div></div> : null}
      {privilegedGrant ? <MCPPrivilegedAccess grant={privilegedGrant} request={request} notify={notify} confirm={confirm} onClose={() => setPrivilegedGrant(null)} /> : null}
    </section>
  )
}
