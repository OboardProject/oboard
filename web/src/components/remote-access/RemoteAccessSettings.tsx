import React, { useEffect, useState } from 'react'
import { SettingsGroup, SettingsSwitchRow } from '../settings/SettingsLayout'

type RequestFn = (path: string, init?: RequestInit) => Promise<any>

export function RemoteAccessSettings({ data, client, load, notify }: { data: any; client: { request: RequestFn }; load: () => Promise<void>; notify: (message: string, tone?: string) => void }) {
  const [terminal, setTerminal] = useState(Boolean(data.settings?.remote_terminal_enabled))
  const [operations, setOperations] = useState(Boolean(data.settings?.mcp_remote_operations_enabled))
  const [exec, setExec] = useState(Boolean(data.settings?.mcp_structured_exec_enabled))
  const [shell, setShell] = useState(Boolean(data.settings?.mcp_raw_shell_enabled))
  const [saving, setSaving] = useState('')

  useEffect(() => {
    setTerminal(Boolean(data.settings?.remote_terminal_enabled))
    setOperations(Boolean(data.settings?.mcp_remote_operations_enabled))
    setExec(Boolean(data.settings?.mcp_structured_exec_enabled))
    setShell(Boolean(data.settings?.mcp_raw_shell_enabled))
  }, [data.settings?.remote_terminal_enabled, data.settings?.mcp_remote_operations_enabled, data.settings?.mcp_structured_exec_enabled, data.settings?.mcp_raw_shell_enabled])

  const save = async (key: string, body: Record<string, boolean>, success: string) => {
    if (saving) return
    setSaving(key)
    try {
      await client.request('/settings', { method: 'POST', body: JSON.stringify(body) })
      await load()
      notify(success, 'success')
    } catch (error: any) {
      notify(error?.message || '保存失败', 'error')
    } finally {
      setSaving('')
    }
  }

  return (
    <section className="settings-card">
      <SettingsGroup title="Web 远程终端" description="管理员通过浏览器打开 Agent PTY，不使用服务器 SSH 或 22 端口。默认关闭。每次新建终端都需要重新认证。">
        <SettingsSwitchRow
          label="启用远程终端"
          description="需要管理员身份、每次打开终端重新认证，以及 Agent remote_terminal_v1。"
          checked={terminal}
          onChange={checked => {
            setTerminal(checked)
            void save('terminal', { remote_terminal_enabled: checked }, checked ? '远程终端已启用' : '远程终端已关闭')
          }}
          disabled={Boolean(saving)}
          ariaLabel="启用 Web 远程终端"
        />
      </SettingsGroup>
      <SettingsGroup title="MCP 主机操作" description="仅在后台单独授权后，Hermes 等 MCP 客户端才能使用。普通 OAuth 授权、全选和 operate 都不会包含这些能力。">
        <SettingsSwitchRow
          label="MCP 远程运维"
          description="结构化读取系统、网络、服务、日志和诊断。"
          checked={operations}
          onChange={checked => {
            setOperations(checked)
            void save('operations', { mcp_remote_operations_enabled: checked }, checked ? 'MCP 远程运维已启用' : 'MCP 远程运维已关闭')
          }}
          disabled={Boolean(saving)}
          ariaLabel="启用 MCP 远程运维"
        />
        <SettingsSwitchRow
          label="Structured Exec"
          description="允许 argv 执行程序，不经过 shell。仍等价于 root 执行任意程序。"
          checked={exec}
          onChange={checked => {
            setExec(checked)
            void save('exec', { mcp_structured_exec_enabled: checked }, checked ? 'Structured Exec 已启用' : 'Structured Exec 已关闭')
          }}
          disabled={Boolean(saving)}
          ariaLabel="启用 Structured Exec"
        />
        <SettingsSwitchRow
          label="Raw Shell"
          description="允许 /bin/sh -c。独立开关，不会因为 Structured Exec 已开启而自动放开。"
          checked={shell}
          onChange={checked => {
            setShell(checked)
            void save('shell', { mcp_raw_shell_enabled: checked }, checked ? 'Raw Shell 已启用' : 'Raw Shell 已关闭')
          }}
          disabled={Boolean(saving)}
          ariaLabel="启用 Raw Shell"
        />
      </SettingsGroup>
    </section>
  )
}
