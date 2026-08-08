import React, { useCallback, useEffect, useState } from 'react'
import { ShieldCheck, Trash2 } from 'lucide-react'
import { listGrants, revokeGrant, type RequestV2 } from './api'
import type { OAuthGrant, ToastTone } from './types'

interface OAuthGrantListProps {
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

function grantResourceSummary(boundary: any) {
  const servers = boundary?.resources?.server || {}
  const serverText = servers.selection === 'selected' ? (servers.ids?.length ? `服务器 ${servers.ids.join(', ')}` : '未选择服务器') : servers.selection === 'none' ? '无服务器' : '全部服务器'
  return `${serverText}${servers.allow_create ? '，允许创建' : ''}`
}

export function OAuthGrantList({ requestV2, notify, confirm }: OAuthGrantListProps) {
  const [grants, setGrants] = useState<OAuthGrant[]>([])
  const [working, setWorking] = useState('')

  const load = useCallback(async () => {
    try {
      setGrants(await listGrants(requestV2))
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
      await load()
      notify?.('OAuth Grant 已撤销', 'success')
    } catch (error: any) {
      notify?.(error?.message || '撤销失败', 'error')
    } finally {
      setWorking('')
    }
  }

  return (
    <section className="settings-card automation-wide automation-grants">
      <div className="settings-card-head automation-section-head"><div><h3>OAuth Grant</h3><p className="muted">每次用户授权都是独立 Grant；撤销后该 Grant 的访问和刷新令牌立即失效。</p></div></div>
      <div className="automation-list">{grants.length ? grants.map((grant) => {
        const revoked = Boolean(grant.revoked_at)
        const approvalRisk = Math.min(3, Math.max(0, Number(grant.approval_profile?.auto_approve_risk || 0)))
        const reconsent = grant.status === 'needs_reconsent'
        return <div className="automation-row" key={grant.id}>
          <div>
            <div className="automation-row-title"><strong>{grant.client_name || grant.client_id}</strong><span className={`automation-state ${revoked ? '' : 'is-enabled'}`}>{revoked ? '已撤销' : reconsent ? '待重新授权' : '有效'}</span><span className="automation-state">{grant.access_level === 'operate' ? '管理操作' : '只读'}</span>{grant.offline_access && <span className="automation-state">可离线刷新</span>}</div>
            <span>{grant.username || `用户 ${grant.user_id}`} · {grantResourceSummary(grant.resource_boundary)}</span>
            <small>策略 v{grant.policy_version || 1} · 角色 v{grant.role_version || 1} · 自动审批风险 ≤ {approvalRisk} · 最近使用 {grant.last_used_at ? formatTime(grant.last_used_at) : '暂无'}</small>
          </div>
          <div><button type="button" className="ghost icon-button danger-text" disabled={revoked || working === `grant-revoke-${grant.id}`} onClick={() => void revoke(grant)} title={revoked ? '授权已撤销' : '撤销授权'} aria-label={`撤销 ${grant.client_name || grant.id}`}><Trash2 size={15} /></button></div>
        </div>
      }) : <div className="automation-empty"><ShieldCheck size={20} /><span>暂无 OAuth Grant。客户端首次登录并完成同意后会显示在这里。</span></div>}</div>
    </section>
  )
}
