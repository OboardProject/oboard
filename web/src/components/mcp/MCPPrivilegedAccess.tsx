import React, { useEffect, useMemo, useState } from 'react'
import { ShieldAlert, X } from 'lucide-react'
import { MotionDialogPanel } from '../ui/motion'
import { FormField } from '../ui/form-field'
import { Select } from '../ui/select'
import { Switch } from '../ui/switch'
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

function normalizeServers(payload: unknown): ServerItem[] {
  const items = Array.isArray(payload)
    ? payload
    : Array.isArray((payload as { servers?: unknown })?.servers)
      ? (payload as { servers: unknown[] }).servers
      : []
  return items
    .map((server: any) => ({ id: Number(server.id), name: String(server.name || `server-${server.id}`) }))
    .filter(server => Number.isFinite(server.id) && server.id > 0)
    .sort((left, right) => left.name.localeCompare(right.name, 'zh-CN'))
}

function readServerScope(item: PrivilegedAccess | null) {
  const serverSel = item?.resource_boundary?.resources?.server
  if (!serverSel?.selection) {
    return { scope: 'all' as const, includeFuture: true, selected: [] as string[] }
  }
  return {
    scope: serverSel.selection === 'all' ? 'all' as const : 'selected' as const,
    includeFuture: Boolean(serverSel.include_future),
    selected: (serverSel.ids || []).map(String),
  }
}

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
  const [scope, setScope] = useState<'selected' | 'all'>('all')
  const [includeFuture, setIncludeFuture] = useState(true)
  const [selected, setSelected] = useState<string[]>([])
  const [servers, setServers] = useState<ServerItem[]>([])
  const [ttl, setTtl] = useState<'until' | '1h' | '24h' | '7d' | '30d'>('until')
  const [stepUp, setStepUp] = useState(false)
  const [busy, setBusy] = useState('')
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      setLoading(true)
      try {
        const [access, listed] = await Promise.all([
          request(`/mcp/grants/${grant.id}/privileged-access`),
          request('/servers'),
        ])
        if (cancelled) return
        const item = access?.privileged_access as PrivilegedAccess | null
        const nextScope = readServerScope(item)
        setCurrent(item)
        setScope(nextScope.scope)
        setIncludeFuture(nextScope.includeFuture)
        setSelected(nextScope.selected)
        setServers(normalizeServers(listed))
      } catch (error: any) {
        if (!cancelled) notify(error?.message || '无法读取敏感服务器访问', 'error')
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()
    return () => { cancelled = true }
  }, [grant.id, notify, request])

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
            include_future: scope === 'all' ? includeFuture : false,
            allow_create: false,
          },
        },
      },
    }
  }

  const submit = async (stepUpToken?: string) => {
    if (scope === 'selected' && selected.length === 0) {
      notify('请至少选择一台服务器', 'error')
      return
    }
    setBusy('save')
    try {
      await request(`/mcp/grants/${grant.id}/privileged-access`, {
        method: 'PUT',
        body: JSON.stringify({ ...payload(), step_up_token: stepUpToken || '' }),
      })
      notify(current ? '敏感服务器访问已更新' : '敏感服务器访问已启用', 'success')
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

  const scopeSummary = useMemo(() => {
    if (scope === 'all') return includeFuture ? '所有现有与未来服务器' : '所有现有服务器'
    if (selected.length === 0) return '尚未选择服务器'
    return `已选 ${selected.length} 台服务器`
  }, [scope, includeFuture, selected.length])

  const controlsLocked = Boolean(busy || loading)

  return (
    <>
      <MotionDialogPanel onCancel={onClose} className="mcp-privileged-dialog" aria-labelledby="mcp-privileged-title">
        <header className="dialog-head">
          <div>
            <h2 id="mcp-privileged-title">{grant.client_name || grant.client_id} · 敏感服务器访问</h2>
            <p className="muted">为 MCP 客户端配置可执行远程运维与交互式 shell 的服务器范围。</p>
          </div>
          <button type="button" className="ghost dialog-close icon-button" onClick={onClose} disabled={controlsLocked} aria-label="关闭" title="关闭"><X size={16} /></button>
        </header>
        <div className="dialog-body mcp-privileged-body">
          <div className="mcp-privileged-warning">
            <ShieldAlert size={18} aria-hidden="true" />
            <div>
              <strong>高风险授权</strong>
              <p>{warning}</p>
              <p>{interactiveWarning}</p>
            </div>
          </div>
          {loading ? <p className="muted mcp-privileged-loading">正在读取授权设置…</p> : <>
            <FormField label="服务器范围" hint="默认允许访问全部服务器；仅在需要收窄权限时切换到指定服务器。" full>
              <Select
                variant="segmented"
                className="full-width"
                value={scope}
                onChange={event => setScope(event.target.value as 'selected' | 'all')}
                disabled={controlsLocked}
                aria-label="服务器范围"
              >
                <option value="all">所有服务器</option>
                <option value="selected">指定服务器</option>
              </Select>
            </FormField>
            <div className="mcp-privileged-scope-summary">{scopeSummary}</div>
            {scope === 'selected' ? (
              <div className="mcp-privileged-server-list">
                {servers.length === 0 ? <p className="muted">暂无服务器</p> : servers.map(server => (
                  <label key={server.id} className="mcp-privileged-server-row">
                    <input
                      type="checkbox"
                      checked={selected.includes(String(server.id))}
                      disabled={controlsLocked}
                      onChange={() => setSelected(current => current.includes(String(server.id))
                        ? current.filter(id => id !== String(server.id))
                        : [...current, String(server.id)])}
                    />
                    <span>{server.name}</span>
                  </label>
                ))}
              </div>
            ) : (
              <div className="switch-form-row mcp-privileged-future-row">
                <span className="switch-form-label">包含未来新增的服务器</span>
                <Switch
                  checked={includeFuture}
                  disabled={controlsLocked}
                  onChange={setIncludeFuture}
                  ariaLabel="包含未来新增的服务器"
                />
              </div>
            )}
            <FormField label="授权有效期" hint="到期后会自动失效；也可选择直到手动撤销。" full>
              <Select
                variant="segmented"
                className="full-width"
                value={ttl}
                onChange={event => setTtl(event.target.value as typeof ttl)}
                disabled={controlsLocked}
                aria-label="授权有效期"
              >
                <option value="until">直到撤销</option>
                <option value="1h">1 小时</option>
                <option value="24h">24 小时</option>
                <option value="7d">7 天</option>
                <option value="30d">30 天</option>
              </Select>
            </FormField>
            {current?.last_step_up_at ? <p className="muted mcp-privileged-meta">最后认证 {current.last_step_up_at}</p> : null}
          </>}
        </div>
        <footer className="dialog-actions">
          <button type="button" className="ghost" onClick={onClose} disabled={controlsLocked}>取消</button>
          {current ? <button type="button" className="ghost danger-text" disabled={controlsLocked} onClick={() => void revoke()}>立即撤销</button> : null}
          <button type="button" disabled={controlsLocked} onClick={() => void submit()}>{current ? '保存授权' : '启用授权'}</button>
        </footer>
      </MotionDialogPanel>
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
