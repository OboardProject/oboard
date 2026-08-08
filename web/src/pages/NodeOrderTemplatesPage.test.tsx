// @vitest-environment jsdom
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NodeOrderTemplatesPage } from './NodeOrderTemplatesPage'

describe('NodeOrderTemplatesPage', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
  })

  it('renders the empty template list while the editor is closed', async () => {
    const request = vi.fn().mockResolvedValue({ templates: [] })

    await act(async () => {
      root.render(<NodeOrderTemplatesPage data={{ servers: [], inbounds: [], subscription_plans: [] }} client={{ request }} />)
      await Promise.resolve()
    })

    expect(container.textContent).toContain('排序模板')
    expect(container.textContent).toContain('尚未创建排序模板')
    expect(request).toHaveBeenCalledWith('/node-order-templates?include_archived=1')
  })
})
