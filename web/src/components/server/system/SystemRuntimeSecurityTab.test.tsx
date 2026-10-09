// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { SystemRuntimeSecurityTab } from './SystemRuntimeSecurityTab'

it('separates offline desired state from actual state', async () => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  const host = document.createElement('div'); document.body.appendChild(host)
  const root = createRoot(host)
  const requestV2 = vi.fn(async (_path: string, _init?: RequestInit) => ({ desired: { mode: 'enhanced', revision: 2 }, report: { desired_mode: 'standard', actual_mode: 'standard', state: 'standard', checked_at: '2026-10-08T00:00:00Z', local_policy: 'standard', checks: [] }, online: false, queued: false }))
  try {
    await act(async () => root.render(<SystemRuntimeSecurityTab serverID={12} client={{ requestV2 }} disabled={false} />))
    expect(host.textContent).toContain('强化模式'); expect(host.textContent).toContain('标准模式')
    expect(host.textContent).toContain('Agent 当前离线')
    expect([...host.querySelectorAll('button')].find(item => item.textContent === '重新检查')!.disabled).toBe(true)
    await act(async () => [...host.querySelectorAll('button')].find(item => item.textContent === '应用模式')!.click())
    const call = requestV2.mock.calls.find(call => call[0] === '/servers/12/runtime-security' && call[1]?.method === 'POST')!
    expect(call[0]).toBe('/servers/12/runtime-security')
    expect(JSON.parse(call[1]!.body as string).mode).toBe('enhanced')
    expect(host.textContent).toContain('期望配置已保存，等待 Agent 上线后应用')
  } finally { act(() => root.unmount()); host.remove(); (globalThis as any).IS_REACT_ACT_ENVIRONMENT = false }
})

it('blocks a Hardened downgrade and keeps failure visible', async () => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  const host = document.createElement('div'); document.body.appendChild(host)
  const root = createRoot(host)
  const client = { requestV2: vi.fn(async () => ({ desired: { mode: 'standard', revision: 2 }, report: { desired_mode: 'standard', actual_mode: 'enhanced', state: 'failed', checked_at: '2026-10-08T00:00:00Z', local_policy: 'hardened', checks: [], phase: 'rollback', error_code: 'runtime_security_rollback_unverified' }, online: true, queued: false, last_task_id: 3, last_task_status: 'failed' })) }
  try {
    await act(async () => root.render(<SystemRuntimeSecurityTab serverID={12} client={client} disabled={false} />))
    expect([...host.querySelectorAll('button')].find(item => item.textContent === '应用模式')!.disabled).toBe(true)
    expect(host.textContent).toContain('本机 Hardened 策略禁止远程降级')
    expect(host.textContent).toContain('任务 #3 执行失败'); expect(host.textContent).toContain('回滚失败')
  } finally { act(() => root.unmount()); host.remove(); (globalThis as any).IS_REACT_ACT_ENVIRONMENT = false }
})

it('repairs only through the authorized operation and disables duplicate queued actions', async () => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  const host = document.createElement('div'); document.body.appendChild(host)
  const root = createRoot(host)
  let queued = false
  const requestV2 = vi.fn(async (path: string, init?: RequestInit) => {
    if (path.includes('/runtime-security')) { expect(JSON.parse(init!.body as string)).toMatchObject({repair:true}); queued = true }
    return { desired: { mode: 'standard', revision: 4 }, report: { revision: 4, desired_mode: 'standard', actual_mode: 'standard', state: 'partial', local_policy: 'standard', checks: [{id:'agent_config',category:'filesystem',severity:'high',status:'failed',message:'权限不安全',remedy:'修复权限',auto_fix:true}] }, online:true, queued, task_id:9 }
  })
  const client = {requestV2}
  try {
    await act(async () => root.render(<SystemRuntimeSecurityTab serverID={12} client={client} disabled={false} />))
    await act(async () => [...host.querySelectorAll('button')].find(item => item.textContent === '修复文件权限')!.click())
    for (const text of ['应用模式','重新检查','修复文件权限']) expect([...host.querySelectorAll('button')].find(item => item.textContent === text)!.disabled).toBe(true)
    expect(host.textContent).toContain('任务 #9')
  } finally { act(() => root.unmount()); host.remove(); (globalThis as any).IS_REACT_ACT_ENVIRONMENT = false }
})

it('polls a same-mode reapply until the desired revision is reported', async () => {
  vi.useFakeTimers()
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  const host = document.createElement('div'); document.body.appendChild(host)
  const root = createRoot(host)
  const client = {requestV2:vi.fn(async () => ({desired:{mode:'enhanced',revision:8},report:{revision:7,desired_mode:'enhanced',actual_mode:'enhanced',state:'enhanced',checks:[]},online:true,queued:false}))}
  try {
    await act(async () => root.render(<SystemRuntimeSecurityTab serverID={12} client={client} disabled={false} />))
    await act(async () => {await vi.advanceTimersByTimeAsync(5000)})
    expect(client.requestV2).toHaveBeenCalledTimes(2)
  } finally { act(() => root.unmount()); host.remove(); vi.useRealTimers(); (globalThis as any).IS_REACT_ACT_ENVIRONMENT = false }
})
