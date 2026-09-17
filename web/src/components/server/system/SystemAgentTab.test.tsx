// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { SystemAgentTab } from './SystemAgentTab'
import type { Server } from '../../proxy-path/types'

it.each(['', 'agent-1'])('copies the complete issued command for agent %s', async (agentID) => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  const command = "curl -fsSL 'https://panel.example/base/install/agent.sh' | env OBOARD_ENROLL_TOKEN='test' OBOARD_INSTALL_STEALTH='1' OBOARD_STEALTH_ADDR='transport.example:443' OBOARD_STEALTH_PIN='test-pin' sh"
  const writeText = vi.fn(async () => {})
  vi.stubGlobal('isSecureContext', true)
  vi.stubGlobal('navigator', { clipboard: { writeText } })
  try {
    await act(async () => root.render(<SystemAgentTab
      server={{ id: 1, agent_id: agentID, stealth_enabled: true } as Server}
      onEnroll={async () => command}
      onUpdateAgent={async () => {}}
    />))
    if (agentID) expect(host.textContent).toContain('待重新安装')
    const generate = [...host.querySelectorAll('button')].find(button => button.textContent?.includes(agentID ? '重新生成接入 Token' : '生成接入命令'))!
    await act(async () => generate.click())
    expect(host.querySelector('pre')?.textContent).toBe(command)
    vi.useFakeTimers()
    await act(async () => [...host.querySelectorAll('button')].find(button => button.textContent?.includes('复制接入命令'))!.click())
    expect(writeText).toHaveBeenCalledWith(command)
    await act(async () => vi.runAllTimers())
  } finally {
    act(() => root.unmount())
    host.remove()
    vi.useRealTimers()
    vi.unstubAllGlobals()
    ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
  }
})
