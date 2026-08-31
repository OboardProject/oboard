import React, { useEffect, useState } from 'react'
import { Dialog } from '../ui/dialog'
import { StepUpAuth } from '../remote-access/StepUpAuth'
import type { OAuthGrant, ToastTone } from '../../features/mcp/types'

type RequestFn = (path: string, init?: RequestInit) => Promise<any>

type ServerItem = { id: number; name: string }

type PrivilegedAccess = {
  capabilities?: string[]
  resource_boundary?: {
    resources?: {
      server?: { selection?: string; ids?: string[]; include_future?: boolean }
    }
  }
  expires_at?: string | null
  last_step_up_at?: string | null
}

const warning = '启用后，该 MCP 客户端可在授权服务器上以 OBoard Agent 的权限执行命令，执行期间不会逐次请求确认。'
const interactiveWarning = '最高风险：允许 AI Agent 以 OBoard Agent 身份获得服务器交互式 shell。'

export function MCPPrivilegedAccess({
  grant,
  request,
  notify,
  confirm,
  onClose,
}: {
  grant: OAuthGrant
  request: RequestFn
  notify: (message: string, tone?: ToastTone) => void
  confirm: (options: { title: string; message: string; confirmText?: string; tone?: 'danger' }) => Promise<boolean>
  onClose: () => void
}) {
  const [current, setCurrent] = useState<PrivilegedAccess | null>(null)
  const [scope, setScope] = useState<'selected' | 'all'>('selected')
  const [includeFuture, setIncludeFuture] = useState(false)
  const [selected, setSelected] = useState<string[]>([])
  const [servers, setServers] = useState<ServerItem[]>([])
  const [ttl, setTtl] = useState<'until' | '1h' | '24h' | '7d' | '30d'>('until')
  const [stepUp, setStepUp] = useState(false)
  const [busy, setBusy] = useState('')

  const load = async () => {
    const [access, listed] = await Promise.all([
      request(`/mcp/grants/${grant.id}/privileged-access`),
      request('/servers'),
    ])
    const item = access?.privileged_access as PrivilegedAccess | null
    setCurrent(item)
    const serverSel = item?.resource_boundary?.resources?.server
    setScope(serverSel?.selection === 'all' ? 'all' : 'selected')
    setIncludeFuture(Boolean(serverSel?.include_future))
    setSelected(serverSel?.ids || [])
    const items = Array.isArray(listed?.servers) ? listed.servers : (Array.isArray(listed) ? listed : [])
    setServers(items.map((server: any) => ({ id: Number(server.id), name: server.name || `server-${server.id}` })))
  }

  useEffect(() => { void load().catch((error: any) => notify(error?.message || '无法读取敏感服务器访问', 'error')) }, [grant.id])

  const payload = () => {
    const capabilities = ['remote_operations', 'remote_exec', 'remote_shell', 'remote_interactive']
    const expiresAt = ttl === 'until' ? undefined : new Date(Date.now() + ({ '1h': 3600, '24h': 86400, '7d': 7 * 86400, '30d': 30 * 86400 }[ttl] * 1000)).toISOString()
    return {
      capabilities,
      until_revoked: ttl === 'until',
      expires_at: expiresAt,
      resource_boundary: {
        version: 1,
        resources: {
          server: {
            selection: scope,
            ids: scope === 'selected' ? selected : undefined,
            include_future: includeFuture,
            allow_create: false,
          },
        },
      },
    }
  }

  const submit = async (stepUpToken?: string) => {
    setBusy('save')
    try {
      await request(`/mcp/grants/${grant.id}/privileged-access`, {
        method: 'PUT',
        body: JSON.stringify({ ...payload(), step_up_token: stepUpToken || '' }),
      })
      notify('敏感服务器访问已更新', 'success')
      onClose()
    } catch (error: any) {
      if (String(error?.message || '').includes('step-up') || error?.code === 'terminal_auth_expired') {
        setStepUp(true)
        return
      }
      notify(error?.message || '保存失败', 'error')
    } finally {
      setBusy('')
    }
  }

  const revoke = async () => {
    const confirmed = await confirm({ title: '撤销敏感服务器访问？', message: '该 MCP 客户端将立即失去远程运维和执行权限。', confirmText: '立即撤销', tone: 'danger' })
    if (!confirmed) return
    setBusy('revoke')
    try {
      await request(`/mcp/grants/${grant.id}/privileged-access`, { method: 'DELETE' })
      notify('敏感服务器访问已撤销', 'success')
      onClose()
    } catch (error: any) {
      notify(error?.message || '撤销失败', 'error')
    } finally {
      setBusy('')
    }
  }

  return (
    <>
      <Dialog isOpen onClose={onClose} title={`${grant.client_name || grant.client_id} · 敏感服务器访问`} size="lg">
        <p className="text-pretty text-sm text-muted-foreground">{warning}</p>
        <p className="text-xs text-muted-foreground" style={{ marginTop: 8 }}>{interactiveWarning}</p>
        <fieldset className="mt-4">
          <legend>服务器范围</legend>
          <label className="switch-form-row"><input type="radio" checked={scope === 'selected'} onChange={() => setScope('selected')} />指定服务器</label>
          <label className="switch-form-row"><input type="radio" checked={scope === 'all'} onChange={() => setScope('all')} />所有服务器</label>
          {scope === 'selected' ? (
            <div className="mt-2 flex flex-col gap-1">
              {servers.map(server => (
                <label key={server.id} className="switch-form-row">
                  <input type="checkbox" checked={selected.includes(String(server.id))} onChange={() => setSelected(current => current.includes(String(server.id)) ? current.filter(id => id !== String(server.id)) : [...current, String(server.id)])} />
                  {server.name}
                </label>
              ))}
            </div>
          ) : null}
          <label className="switch-form-row" style={{ marginTop: 8 }}>
            <input type="checkbox" checked={includeFuture} onChange={event => setIncludeFuture(event.target.checked)} />
            包含未来服务器
          </label>
        </fieldset>
        <fieldset className="mt-4">
          <legend>授权有效期</legend>
          {([['until', '直到手动撤销'], ['1h', '1 小时'], ['24h', '24 小时'], ['7d', '7 天'], ['30d', '30 天']] as const).map(([value, label]) => (
            <label key={value} className="switch-form-row"><input type="radio" checked={ttl === value} onChange={() => setTtl(value)} />{label}</label>
          ))}
        </fieldset>
        {current?.last_step_up_at ? <p className="muted" style={{ marginTop: 12 }}>最后认证 {current.last_step_up_at}</p> : null}
        <div className="dialog-actions">
          <button type="button" className="ghost" onClick={onClose}>取消</button>
          {current ? <button type="button" className="ghost danger-text" disabled={Boolean(busy)} onClick={() => void revoke()}>立即撤销</button> : null}
          <button type="button" disabled={Boolean(busy)} onClick={() => void submit()}>修改授权</button>
        </div>
      </Dialog>
      {stepUp ? (
        <StepUpAuth
          request={request}
          purpose="privileged_grant"
          resourceType="oauth_grant"
          resourceId={grant.id}
          title="确认敏感服务器访问"
          warning={warning}
          onComplete={token => { setStepUp(false); void submit(token) }}
          onCancel={() => setStepUp(false)}
        />
      ) : null}
    </>
  )
}
