// @vitest-environment jsdom

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ConfigurationSyncStatus } from './configuration-sync-ui'

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
    expect(button?.textContent).toContain('1 台同步失败，点击重试')
    act(() => button?.click())
    expect(retry).toHaveBeenCalledTimes(1)

    act(() => root.render(<ConfigurationSyncStatus rows={[{ server_id: 1, state: 'pending' }]} />))
    expect(container.querySelector('button')).toBeNull()
    expect(container.textContent).toContain('正在同步 1 台服务器')
  })

  it('does not render an operator action for a viewer', () => {
    act(() => root.render(<ConfigurationSyncStatus rows={[{ server_id: 1, state: 'failed' }]} canOperate={false} />))
    expect(container.textContent).toBe('')
  })
})
