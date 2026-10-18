// @vitest-environment jsdom
import React, { act, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { LazyMotion, domAnimation } from 'motion/react'
import { expect, it, vi } from 'vitest'
import { DialogContext } from '../../components/ui/dialog-context'
import { InstanceDialog } from './InstanceDialog'
import { RunDialog } from './RunDialog'
import type { InstallationDetail, RequestFn } from './types'

it('opens execution details over the instance and returns to its configuration', async () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const request = vi.fn(async (path: string) => {
    if (path.includes('/logs?')) return { logs: [] }
    if (path === '/plugin-instances/1') return {
      id: 1, revision: 1, name: '默认实例', status: 'ready', enabled: true,
      values: { REGION: 'cn-hongkong' }, custom: [], secrets: {}, schedules: [], state: {}, issues: [],
    }
    return { run: { id: 7, status: 'succeeded', trigger: 'manual' } }
  }) as unknown as RequestFn
  const installation = { permissions: [], manifest: { environment: [
    { name: 'REGION', type: 'string', label: '地域' },
  ] } } as unknown as InstallationDetail
  const notify = vi.fn()
  function Workspace() {
    const [runID, setRunID] = useState<number | null>(null)
    return <LazyMotion features={domAnimation}><DialogContext.Provider value={{ confirm: vi.fn(), alert: vi.fn(), prompt: vi.fn() }}>
      <InstanceDialog instanceID={1} installation={installation} request={request}
        servers={[]} channels={[]} users={[]} plans={[]} customTypes={[]} canConfigure canAuthorize canExecute
        notify={notify} onChanged={() => {}} onOpenRun={setRunID} onClose={() => {}} />
      {runID && <RunDialog id={runID} request={request} canCancel notify={notify} onClose={() => setRunID(null)} />}
    </DialogContext.Provider></LazyMotion>
  }
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  try {
    await act(async () => root.render(<Workspace />))
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 30)) })
    const runButton = [...document.querySelectorAll('button')].find(button => button.textContent === '立即运行')!
    await act(async () => { runButton.focus(); runButton.click() })
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 30)) })
    expect(notify).toHaveBeenCalledWith('已提交运行', 'success')
    const top = document.querySelector('[data-modal-top="true"] [role="dialog"]')!
    expect(top.textContent).toContain('执行 #7')
    expect(top.contains(document.activeElement)).toBe(true)
    await act(async () => (top.querySelector('[aria-label="关闭"]') as HTMLButtonElement).click())
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 30)) })
    expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1)
    expect((document.querySelector('input[value="cn-hongkong"]') as HTMLInputElement).value).toBe('cn-hongkong')
  } finally {
    await act(async () => root.unmount())
    container.remove()
    vi.unstubAllGlobals()
  }
})
