// @vitest-environment jsdom

import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AboutSettingsPanel } from './AboutSettingsPanel'

describe('AboutSettingsPanel', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  it('shows project and runtime build information', () => {
    act(() => {
      root.render(<AboutSettingsPanel version={{
        name: 'OBoard',
        version: '1.8.0',
        build: '20260814093000',
        commit: 'abcdef123456',
        built_at: '2026-08-14T09:30:00+08:00',
        agent_expected_version: '1.8.0',
        agent_expected_build: '20260814090000',
        kernel_version: '1.12.4',
        kernel_build: '20260814084500',
        kernel: 'oboard-sb',
      }} />)
    })

    expect(container.textContent).toContain('v1.8.0')
    expect(container.textContent).toContain('20260814093000')
    expect(container.textContent).toContain('abcdef123456')
    expect(container.textContent).toContain('GPL-3.0')

    const projectLink = container.querySelector<HTMLAnchorElement>('a[href="https://github.com/OboardProject/oboard"]')
    expect(projectLink?.target).toBe('_blank')
    expect(projectLink?.rel).toContain('noreferrer')
  })

  it('renders development and missing build states without fake values', () => {
    act(() => {
      root.render(<AboutSettingsPanel version={{ version: '0.0.1', build: 'dev', dev: true }} />)
    })

    expect(container.textContent).toContain('开发构建')
    expect(container.textContent).toContain('v0.0.1 · dev')
    expect(container.textContent).toContain('未提供')
  })
})
