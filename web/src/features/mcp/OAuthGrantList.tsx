import React, { useCallback, useEffect, useState } from 'react'
import { ShieldCheck, Trash2, KeyRound } from 'lucide-react'
import { MCPPrivilegedAccess } from '../../components/mcp/MCPPrivilegedAccess'
import { listGrants, revokeGrant, type RequestV2 } from './api'
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
  if (grant.effective_role === 'admin') return '继承管理员'
  if (grant.effective_role === 'operator') return '继承操作员'
  return '继承只读权限'
}

export function OAuthGrantList({ request, requestV2, notify, confirm }: OAuthGrantListProps) {
  const [grants, setGrants] = useState<OAuthGrant[]>([])
  const [working, setWorking] = useState('')
  const [privilegedGrant, setPrivilegedGrant] = useState<OAuthGrant | null>(null)

  const load = useCallback(async () => {
    try {
      const items = await listGrants(requestV2)
      setGrants(items.filter((grant) => !grant.revoked_at && grant.status !== 'revoked'))
    } catch (error: any) {
      notify?.(error?.message || 'OAuth Grant 列表加载失败', 'error')
    }
  }, [requestV2, notify])

  useEffect(() => { void load() }, [load])

  const revoke = async (grant: OAuthGrant) => {
    const confirmed = await confirm({
      title: '撤销这个 OAuth Grant？',
      message: '该授权的 Access Token 和 Refresh Token 会立即失效，且不会影响同一客户端的其他授权。',
      confirmText: '撤销授权',
      tone: 'danger',
    })
    if (!confirmed) return
    setWorking(`grant-revoke-${grant.id}`)
    try {
      await revokeGrant(requestV2, grant.id)
      setGrants((current) => current.filter((item) => item.id !== grant.id))
      notify?.('OAuth Grant 已撤销并移除', 'success')
    } catch (error: any) {
      notify?.(error?.message || '撤销失败', 'error')
    } finally {
      setWorking('')
    }
  }

  return (
    <section className="settings-card automation-wide automation-grants">
      <div className="settings-card-head automation-section-head"><div><h3>OAuth Grant</h3><p className="muted">MCP 实时继承授权用户的当前角色；撤销后该 Grant 的访问和刷新令牌立即失效。</p></div></div>
      <div className="automation-list">{grants.length ? grants.map((grant) => {
        const reconsent = grant.status === 'needs_reconsent'
        return <div className="automation-row" key={grant.id}>
          <div>
            <div className="automation-row-title"><strong>{grant.client_name || grant.client_id}</strong><span className={`automation-state ${reconsent ? '' : 'is-enabled'}`}>{reconsent ? '待重新授权' : '有效'}</span><span className="automation-state">{inheritedRoleLabel(grant)}</span>{grant.offline_access && <span className="automation-state">可离线刷新</span>}</div>
            <span>{grant.username || `用户 ${grant.user_id}`} · 权限随用户角色实时更新</span>
            <small>最近使用 {grant.last_used_at ? formatTime(grant.last_used_at) : '暂无'}</small>
          </div>
          <div>
            <button type="button" className="ghost icon-button" onClick={() => setPrivilegedGrant(grant)} title="敏感服务器访问" aria-label={`${grant.client_name || grant.id} 敏感服务器访问`}><KeyRound size={15} /></button>
            <button type="button" className="ghost icon-button danger-text" disabled={working === `grant-revoke-${grant.id}`} onClick={() => void revoke(grant)} title="撤销授权" aria-label={`撤销 ${grant.client_name || grant.id}`}><Trash2 size={15} /></button>
          </div>
        </div>
      }) : <div className="automation-empty"><ShieldCheck size={20} /><span>暂无 OAuth Grant。客户端首次登录并完成同意后会显示在这里。</span></div>}</div>
      {privilegedGrant ? <MCPPrivilegedAccess grant={privilegedGrant} request={request} notify={notify} confirm={confirm} onClose={() => setPrivilegedGrant(null)} /> : null}
    </section>
  )
}
