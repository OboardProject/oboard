import React, { useEffect, useState } from 'react'
import { Switch } from '../ui/switch'
import { Terminal } from 'lucide-react'

type RequestFn = (path: string, init?: RequestInit) => Promise<any>

const reasonCopy: Record<string, string> = {
  remote_access_global_disabled: '全局远程终端已关闭',
  remote_access_server_disabled: '此服务器未启用远程终端',
  agent_offline: 'Agent 离线',
  agent_upgrade_required: 'Agent 版本不支持远程访问，请先升级',
  agent_local_gate_denied: 'Agent 本地安全锁拒绝终端',
}

export function RemoteAccessStatus({
  serverId,
  client,
  notify,
  onOpenTerminal,
}: {
  serverId: number
  client: { request: RequestFn }
  notify?: (message: string, tone?: string) => void
  onOpenTerminal: () => void
}) {
  const [view, setView] = useState<any>(null)
  const [saving, setSaving] = useState('')

  const load = async () => {
    try {
      const result = await client.request(`/servers/${serverId}/remote-access`)
      setView(result.remote_access)
    } catch (error: any) {
      notify?.(error?.message || '无法读取远程访问状态', 'error')
    }
  }

  useEffect(() => { void load() }, [serverId])

  const patch = async (body: Record<string, boolean>, success: string) => {
    if (saving) return
    setSaving('policy')
    try {
      const result = await client.request(`/servers/${serverId}/remote-access`, { method: 'PATCH', body: JSON.stringify(body) })
      setView(result.remote_access)
      notify?.(success, 'success')
    } catch (error: any) {
      notify?.(error?.message || '保存失败', 'error')
    } finally {
      setSaving('')
    }
  }

  if (!view) return <p className="muted">正在读取远程访问状态…</p>

  const reasons = (view.unavailable_reasons || []).map((code: string) => reasonCopy[code] || code)
  const localMode = view.agent?.local_mode === 'hardened' ? 'Hardened' : 'Standard'
  const terminalAllowed = view.agent?.local_mode !== 'hardened' || Boolean(view.agent?.local_allow?.remote_terminal)
  const canOpen = reasons.length === 0

  return (
    <>
      <dl className="server-detail-grid">
        <div><dt>远程终端</dt><dd>{view.effective?.remote_terminal ? '已开启' : '已关闭'}</dd></div>
        <div><dt>Agent 支持</dt><dd>{(view.agent?.capabilities || []).includes('remote_terminal_v1') ? 'remote_terminal_v1' : '不支持'}</dd></div>
        <div><dt>Agent 本地安全模式</dt><dd>{localMode}</dd></div>
        <div><dt>本地允许</dt><dd>{terminalAllowed ? 'Yes' : 'No'}</dd></div>
        <div><dt>活动终端</dt><dd>{Number(view.active_terminals || 0)} / 2</dd></div>
      </dl>
      <div className="switch-form-row" style={{ marginTop: 12 }}>
        <span className="switch-form-label">在此服务器启用远程终端</span>
        <Switch
          checked={Boolean(view.server?.remote_terminal_enabled)}
          disabled={Boolean(saving)}
          onChange={checked => void patch({ remote_terminal_enabled: checked }, checked ? '此服务器已允许远程终端' : '此服务器已关闭远程终端')}
          ariaLabel="在此服务器启用远程终端"
        />
      </div>
      <div className="switch-form-row">
        <span className="switch-form-label">MCP 远程运维</span>
        <Switch
          checked={Boolean(view.server?.mcp_remote_operations_enabled)}
          disabled={Boolean(saving)}
          onChange={checked => void patch({ mcp_remote_operations_enabled: checked }, checked ? '此服务器已允许 MCP 远程运维' : '此服务器已关闭 MCP 远程运维')}
          ariaLabel="在此服务器启用 MCP 远程运维"
        />
      </div>
      <div className="switch-form-row">
        <span className="switch-form-label">Structured Exec</span>
        <Switch
          checked={Boolean(view.server?.mcp_structured_exec_enabled)}
          disabled={Boolean(saving)}
          onChange={checked => void patch({ mcp_structured_exec_enabled: checked }, checked ? '此服务器已允许 Structured Exec' : '此服务器已关闭 Structured Exec')}
          ariaLabel="在此服务器启用 Structured Exec"
        />
      </div>
      <div className="switch-form-row">
        <span className="switch-form-label">Raw Shell</span>
        <Switch
          checked={Boolean(view.server?.mcp_raw_shell_enabled)}
          disabled={Boolean(saving)}
          onChange={checked => void patch({ mcp_raw_shell_enabled: checked }, checked ? '此服务器已允许 Raw Shell' : '此服务器已关闭 Raw Shell')}
          ariaLabel="在此服务器启用 Raw Shell"
        />
      </div>
      {reasons.length ? <p className="muted" style={{ marginTop: 10 }}>{reasons.join('；')}</p> : null}
      <div className="dialog-actions" style={{ marginTop: 12, justifyContent: 'flex-start' }}>
        <button type="button" disabled={!canOpen} title={canOpen ? '打开远程终端' : reasons.join('；')} onClick={onOpenTerminal}>
          <Terminal size={15} aria-hidden="true" />打开终端
        </button>
      </div>
    </>
  )
}
