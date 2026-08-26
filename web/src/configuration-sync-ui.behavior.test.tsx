// @vitest-environment jsdom

import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ConfigurationSyncStatus } from './configuration-sync-ui'
import { mergeConfigurationMutationResponse } from './configuration-sync'

describe('ConfigurationSyncStatus', () => {
  let root: Root
  let container: HTMLDivElement

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('renders immediate local saving feedback and updates to synced state', () => {
    act(() => root.render(<ConfigurationSyncStatus rows={[]} saving />))
    expect(container.textContent).toContain('正在保存...')
    expect(container.querySelector('[aria-live="polite"]')).not.toBeNull()

    act(() => root.render(<ConfigurationSyncStatus rows={[{ server_id: 1, state: 'synced' }]} />))
    expect(container.textContent).toContain('配置已同步')
    expect(container.querySelector('[aria-live="polite"]')).not.toBeNull()
  })

  it('renders only failed retry action and clears it after the failed state is reconciled', () => {
    const retry = vi.fn()
    act(() => root.render(<ConfigurationSyncStatus rows={[{ server_id: 1, state: 'failed', error: 'prepare failed' }, { server_id: 2, state: 'synced' }]} onRetry={retry} />))
    const button = container.querySelector('button') as HTMLButtonElement | null
    expect(button?.textContent).toContain('1 台同步失败 · 1 个问题')
    act(() => button?.click())
    const retryButton = Array.from(document.body.querySelectorAll('button')).find(item => item.textContent?.includes('重试 1 台服务器'))
    act(() => retryButton?.click())
    expect(retry).toHaveBeenCalledTimes(1)

    act(() => root.render(<ConfigurationSyncStatus rows={[{ server_id: 1, state: 'pending' }]} />))
    expect(container.querySelector('button')).toBeNull()
    expect(container.textContent).toContain('正在同步 1 台服务器')
  })

  it('opens deduplicated, actionable failure details without a hover-only tooltip', () => {
    const navigate = vi.fn()
    const repeated = '入口 15 已存在相同位置的直接出口分支'
    act(() => root.render(<ConfigurationSyncStatus
      rows={[
        { server_id: 1, state: 'failed', error: repeated },
        { server_id: 2, state: 'failed', error: repeated },
      ]}
      servers={[{ id: 1, name: '东京入口' }, { id: 2, name: '香港出口' }]}
      onNavigate={navigate}
      onRetry={vi.fn()}
    />))
    const trigger = container.querySelector('button') as HTMLButtonElement
    expect(trigger.title).toBe('')
    expect(trigger.getAttribute('aria-haspopup')).toBe('dialog')
    act(() => trigger.click())
    expect(document.body.textContent).toContain('入口 15 存在重复的直接出口分支')
    expect(document.body.textContent).toContain('相同错误已合并显示')
    expect(document.body.textContent).toContain('东京入口')
    expect(document.body.textContent).toContain('香港出口')
    expect(document.body.textContent?.split(repeated)).toHaveLength(2)
    const target = Array.from(document.body.querySelectorAll('button')).find(item => item.textContent === '打开代理拓扑')
    act(() => target?.click())
    expect(navigate).toHaveBeenCalledWith('proxy-paths')
  })

  it('rolls back the rendered entity and exposes an actionable error after a failed save', async () => {
    function Harness() {
      const [saving, setSaving] = useState(false)
      const [entity, setEntity] = useState({ servers: [{ id: 1, name: '旧名称' }] })
      const [error, setError] = useState('')
      const save = async () => {
        setSaving(true)
        await Promise.resolve()
        setEntity(current => mergeConfigurationMutationResponse(current, { mutation_pending: false, mutation_error: '保存失败' }, '/servers/1'))
        setSaving(false)
        setError('保存失败，请重试')
      }
      return <>
        <ConfigurationSyncStatus rows={[]} saving={saving} />
        <span data-testid="server-name">{entity.servers[0].name}</span>
        <span role="alert">{error}</span>
        <button type="button" onClick={() => void save()}>保存</button>
      </>
    }

    act(() => root.render(<Harness />))
    const saveButton = container.querySelector('button:last-of-type') as HTMLButtonElement
    await act(async () => { saveButton.click() })
    expect(container.textContent).toContain('保存失败，请重试')
    expect(container.querySelector('[data-testid="server-name"]')?.textContent).toBe('旧名称')
    expect(container.textContent).not.toContain('配置已同步')
  })

  it('does not render an operator action for a viewer', () => {
    act(() => root.render(<ConfigurationSyncStatus rows={[{ server_id: 1, state: 'failed' }]} canOperate={false} />))
    expect(container.textContent).toBe('')
  })
})
