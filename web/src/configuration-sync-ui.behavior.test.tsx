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
    expect(button?.textContent).toContain('配置同步被阻塞 · 1 个问题')
    act(() => button?.click())
    const retryButton = Array.from(document.body.querySelectorAll('button')).find(item => item.textContent?.includes('重新尝试 1 个同步任务'))
    act(() => retryButton?.click())
    expect(retry).toHaveBeenCalledTimes(1)

    act(() => root.render(<ConfigurationSyncStatus rows={[{ server_id: 1, state: 'pending' }]} />))
    expect(container.querySelector('button')).toBeNull()
    expect(container.textContent).toContain('正在同步 1 台服务器')
  })

  it('lists currently syncing servers in a hover popover', () => {
    act(() => root.render(<ConfigurationSyncStatus
      rows={[
        { server_id: 1, state: 'running' },
        { server_id: 2, state: 'queued' },
        { server_id: 3, state: 'synced' },
        { server_id: 4, state: 'pending' },
      ]}
      servers={[{ id: 1, name: '东京入口', status: 'online', agent_id: 'agent-1' }, { id: 2, name: '香港出口', status: 'online', agent_id: 'agent-2' }, { id: 3, name: '新加坡', status: 'online', agent_id: 'agent-3' }]}
    />))
    const pill = container.querySelector('.deploy-status-pill.has-popover') as HTMLElement | null
    const popover = container.querySelector('[role="tooltip"]') as HTMLElement | null
    expect(container.textContent).toContain('正在同步 3 台服务器')
    expect(pill).not.toBeNull()
    expect(pill?.getAttribute('aria-describedby')).toBe(popover?.id)
    expect(popover?.textContent).toContain('正在同步的服务器')
    expect(popover?.textContent).toContain('东京入口')
    expect(popover?.textContent).toContain('香港出口')
    expect(popover?.textContent).toContain('服务器 #4')
    expect(popover?.textContent).toContain('下发中')
    expect(popover?.textContent).toContain('排队中')
    expect(popover?.textContent).toContain('等待中')
    expect(popover?.textContent).not.toContain('新加坡')
  })

  it('does not wait on offline or unenrolled servers', () => {
    act(() => root.render(<ConfigurationSyncStatus
      rows={[
        { server_id: 1, state: 'queued', agent_reachable: false },
        { server_id: 2, state: 'pending' },
        { server_id: 3, state: 'synced' },
      ]}
      servers={[
        { id: 1, name: 'OC DE', status: 'offline', agent_id: 'agent-de' },
        { id: 2, name: '待接入', status: 'unknown', agent_id: '' },
        { id: 3, name: '9929', status: 'online', agent_id: 'agent-9929' },
      ]}
    />))
    expect(container.textContent).toContain('配置已同步')
    expect(container.textContent).not.toContain('正在同步')
    expect(container.querySelector('[role="tooltip"]')).toBeNull()
    expect(container.textContent).not.toContain('OC DE')
    expect(container.textContent).not.toContain('排队中')
  })

  it('does not attach a syncing-server popover to idle, saving, or failed states', () => {
    act(() => root.render(<ConfigurationSyncStatus rows={[{ server_id: 1, state: 'synced' }]} servers={[{ id: 1, name: '东京入口' }]} />))
    expect(container.querySelector('[role="tooltip"]')).toBeNull()
    expect(container.textContent).not.toContain('东京入口')

    act(() => root.render(<ConfigurationSyncStatus rows={[]} saving servers={[{ id: 1, name: '东京入口' }]} />))
    expect(container.querySelector('[role="tooltip"]')).toBeNull()
    expect(container.textContent).toContain('正在保存...')

    act(() => root.render(<ConfigurationSyncStatus rows={[{ server_id: 1, state: 'failed', error: 'prepare failed' }]} servers={[{ id: 1, name: '东京入口' }]} />))
    expect(container.querySelector('[role="tooltip"]')).toBeNull()
    expect(container.querySelector('.deploy-status-pill.has-popover')).toBeNull()
  })

  it('opens deduplicated, actionable failure details without a hover-only tooltip', () => {
    const locateInbound = vi.fn()
    const repeated = '入口 15 的直接出口分支「东京直出」与「备用直出」位于同一位置；请删除或停用其中一条后再同步'
    act(() => root.render(<ConfigurationSyncStatus
      rows={[
        { server_id: 1, state: 'failed', error: repeated },
        { server_id: 2, state: 'failed', error: repeated },
      ]}
      servers={[{ id: 1, name: '东京入口' }, { id: 2, name: '香港出口' }]}
      inbounds={[{ id: 15, server_id: 1, name: '日本主入口', protocol: 'vless', listen_ip: '0.0.0.0', port: 443 }]}
      onLocateInbound={locateInbound}
      onRetry={vi.fn()}
    />))
    const trigger = container.querySelector('button') as HTMLButtonElement
    expect(trigger.title).toBe('')
    expect(trigger.getAttribute('aria-haspopup')).toBe('dialog')
    act(() => trigger.click())
    expect(document.body.textContent).toContain('入口「日本主入口」存在重复的直接出口分支')
    expect(document.body.textContent).toContain('东京入口')
    expect(document.body.textContent).toContain('VLESS · 0.0.0.0:443')
    expect(document.body.textContent).toContain('东京直出 ↔ 备用直出')
    expect(document.body.textContent).toContain('这不表示 2 台服务器各自都有问题')
    expect(document.body.textContent).toContain('东京入口')
    expect(document.body.textContent).toContain('香港出口')
    expect(document.body.textContent?.split(repeated)).toHaveLength(2)
    const target = Array.from(document.body.querySelectorAll('button')).find(item => item.textContent === '定位并选中「日本主入口」')
    act(() => target?.click())
    expect(locateInbound).toHaveBeenCalledWith(15)
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
