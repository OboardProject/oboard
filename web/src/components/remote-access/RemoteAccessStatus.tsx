import React, { useEffect, useState } from 'react'
import { FormField } from '../ui/form-field'
import { Switch } from '../ui/switch'

type RequestFn = (path: string, init?: RequestInit) => Promise<any>

const reasonCopy: Record<string, string> = {
  remote_access_global_disabled: '全局已关闭',
  remote_access_server_disabled: '服务器已关闭',
  agent_offline: 'Agent 离线',
  agent_upgrade_required: 'Agent 版本不支持，请先升级',
  agent_local_gate_denied: 'Agent 本地安全策略拒绝',
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
  const disabled = Boolean(saving) || !editable

  const caps: Array<{ key: string; label: string; global: boolean; server: boolean; effective: boolean; patchKey: string }> = [
    { key: 'remote_terminal', label: 'Web 终端', global: Boolean(view.global?.remote_terminal_enabled), server: Boolean(view.server?.remote_terminal_enabled), effective: Boolean(view.effective?.remote_terminal), patchKey: 'remote_terminal_enabled' },
    { key: 'operations', label: 'MCP 操作', global: Boolean(view.global?.mcp_remote_operations_enabled), server: Boolean(view.server?.mcp_remote_operations_enabled), effective: Boolean(view.effective?.mcp_remote_operations), patchKey: 'mcp_remote_operations_enabled' },
    { key: 'exec', label: 'MCP 执行', global: Boolean(view.global?.mcp_structured_exec_enabled), server: Boolean(view.server?.mcp_structured_exec_enabled), effective: Boolean(view.effective?.mcp_structured_exec), patchKey: 'mcp_structured_exec_enabled' },
    { key: 'shell', label: 'MCP Shell', global: Boolean(view.global?.mcp_raw_shell_enabled), server: Boolean(view.server?.mcp_raw_shell_enabled), effective: Boolean(view.effective?.mcp_raw_shell), patchKey: 'mcp_raw_shell_enabled' },
    { key: 'interactive', label: 'MCP 终端', global: Boolean(view.global?.mcp_interactive_terminal_enabled), server: Boolean(view.server?.mcp_interactive_terminal_enabled), effective: Boolean(view.effective?.mcp_interactive_terminal), patchKey: 'mcp_interactive_terminal_enabled' },
  ]

  return (
    <>
      {caps.map(cap => (
        <FormField key={cap.key} label={`启用${cap.label}`} hint={`${cap.label} 在此服务器的开关。全局与服务器均需开启才生效。`}>
          <Switch
            checked={cap.server}
            disabled={disabled}
            onChange={checked => void patch({ [cap.patchKey]: checked }, checked ? `此服务器已开启${cap.label}` : `此服务器已关闭${cap.label}`)}
            ariaLabel={`在此服务器启用${cap.label}`}
          />
        </FormField>
      ))}
      <div className="form-extra-row" style={{ overflowX: 'auto' }}>
        <table className="remote-access-effective-table" style={{ width: '100%', fontSize: 12, borderCollapse: 'collapse' }}>
          <thead>
            <tr>
              <th style={{ textAlign: 'left', padding: '4px 8px' }}>能力</th>
              <th style={{ textAlign: 'center', padding: '4px 8px' }}>全局</th>
              <th style={{ textAlign: 'center', padding: '4px 8px' }}>服务器</th>
              <th style={{ textAlign: 'center', padding: '4px 8px' }}>Agent</th>
              <th style={{ textAlign: 'center', padding: '4px 8px' }}>有效</th>
            </tr>
          </thead>
          <tbody>
            {caps.map(cap => {
              const agentCap = (() => {
                if (cap.key === 'remote_terminal') return (view.agent?.capabilities || []).includes('remote_terminal_v1')
                if (cap.key === 'interactive') return (view.agent?.capabilities || []).includes('remote_interactive_mcp_v1')
                return (view.agent?.capabilities || []).includes('remote_exec_v1')
              })()
              const agentGate = (() => {
                if (view.agent?.local_mode !== 'hardened') return true
                if (cap.key === 'remote_terminal') return Boolean(view.agent?.local_allow?.remote_terminal)
                if (cap.key === 'operations') return Boolean(view.agent?.local_allow?.mcp_remote_operations)
                if (cap.key === 'exec') return Boolean(view.agent?.local_allow?.mcp_structured_exec)
                if (cap.key === 'shell') return Boolean(view.agent?.local_allow?.mcp_raw_shell)
                if (cap.key === 'interactive') return Boolean(view.agent?.local_allow?.mcp_interactive_terminal)
                return true
              })()
              const agentEffective = agentCap && agentGate
              return (
                <tr key={cap.key} style={{ borderTop: '1px solid var(--border)' }}>
                  <td style={{ padding: '4px 8px' }}>{cap.label}</td>
                  <td style={{ textAlign: 'center', padding: '4px 8px' }}>{cap.global ? '✓' : '✗'}</td>
                  <td style={{ textAlign: 'center', padding: '4px 8px' }}>{cap.server ? '✓' : '✗'}</td>
                  <td style={{ textAlign: 'center', padding: '4px 8px' }}>{agentEffective ? '✓' : '✗'}</td>
                  <td style={{ textAlign: 'center', padding: '4px 8px', fontWeight: cap.effective ? 700 : 400 }}>{cap.effective ? '✓' : '✗'}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      <div className="form-extra-row"><span>活动终端 {Number(view.active_terminals || 0)} / 2{reasons.length ? ` · ${reasons.join('；')}` : ''}</span></div>
    </>
  )
}
