import React, { useEffect, useState } from 'react'
import { FormField } from '../ui/form-field'
import { Switch } from '../ui/switch'

type RequestFn = (path: string, init?: RequestInit) => Promise<any>

const reasonCopy: Record<string, string> = {
  remote_access_global_disabled: '全局远程控制已关闭',
  remote_access_server_disabled: '此服务器未启用远程控制',
  agent_offline: 'Agent 离线',
  agent_upgrade_required: 'Agent 版本不支持远程访问，请先升级',
  agent_local_gate_denied: 'Agent 本地安全锁拒绝终端',
}

export function RemoteAccessStatus({
  serverId,
  client,
  notify,
  editable = true,
}: {
  serverId: number
  client: { request: RequestFn }
  notify?: (message: string, tone?: string) => void
  editable?: boolean
}) {
  const [view, setView] = useState<any>(null)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState('')

  const load = async () => {
    try {
      const result = await client.request(`/servers/${serverId}/remote-access`)
      setView(result.remote_access)
      setError('')
    } catch (err: any) {
      setError(err?.message || '无法读取远程访问状态')
      notify?.(err?.message || '无法读取远程访问状态', 'error')
    }
  }

  useEffect(() => { void load() }, [serverId])

  const patch = async (body: Record<string, boolean>, success: string) => {
    if (saving || !editable) return
    setSaving('policy')
    try {
      const result = await client.request(`/servers/${serverId}/remote-access`, { method: 'PATCH', body: JSON.stringify(body) })
      setView(result.remote_access)
      notify?.(success, 'success')
    } catch (err: any) {
      notify?.(err?.message || '保存失败', 'error')
    } finally {
      setSaving('')
    }
  }

  if (!view) return <div className="form-extra-row"><span>{error || '正在读取远程访问状态…'}</span></div>

  const reasons = (view.unavailable_reasons || []).map((code: string) => reasonCopy[code] || code)
  const mcpEnabled = Boolean(view.server?.mcp_remote_operations_enabled)
    && Boolean(view.server?.mcp_structured_exec_enabled)
    && Boolean(view.server?.mcp_raw_shell_enabled)
  const disabled = Boolean(saving) || !editable

  return (
    <>
      <FormField label="启用远程控制" hint="允许管理员打开这台服务器的 WebSSH。此项立即生效。">
        <Switch
          checked={Boolean(view.server?.remote_terminal_enabled)}
          disabled={disabled}
          onChange={checked => void patch({ remote_terminal_enabled: checked }, checked ? '此服务器已开启远程控制' : '此服务器已关闭远程控制')}
          ariaLabel="在此服务器启用远程控制"
        />
      </FormField>
      <FormField label="启用 MCP 控制" hint="允许授权的 MCP 客户端管理这台服务器。此项立即生效。">
        <Switch
          checked={mcpEnabled}
          disabled={disabled}
          onChange={checked => void patch({ mcp_remote_operations_enabled: checked }, checked ? '此服务器已开启 MCP 控制' : '此服务器已关闭 MCP 控制')}
          ariaLabel="在此服务器启用 MCP 控制"
        />
      </FormField>
      <div className="form-extra-row"><span>当前有效：远程控制{view.effective?.remote_terminal ? '已开启' : '已关闭'} · 活动终端 {Number(view.active_terminals || 0)} / 2{reasons.length ? ` · ${reasons.join('；')}` : ''}</span></div>
    </>
  )
}
