// @vitest-environment jsdom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import React, { act, useEffect, useRef, useState } from 'react'
import { createRoot } from 'react-dom/client'
import ts from 'typescript'
import { expect, it, vi } from 'vitest'
import * as update from './controller-update'
import * as diagnostics from './controller-update-diagnostics'

const source = readFileSync(resolve(__dirname, 'main.tsx'), 'utf8')
const component = source.slice(source.indexOf('function ControllerUpdateInstallDialog('), source.indexOf('\nfunction StorageDiagnosticsCard('))
const compiled = ts.transpileModule(component, { compilerOptions: { jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ES2022 } }).outputText
const Surface = ({ children }: any) => <div>{children}</div>
const Icon = () => null
const dependencies = {
  React, useEffect, useRef, useState, ...update, ...diagnostics,
  useReducedMotion: () => true,
  AnimatePresence: Surface, m: { section: 'section', div: 'div' },
  ModalSurface: Surface, MotionDialogPanel: Surface, ControllerUpdateLightfield: Surface,
  ControllerUpdateDiagnosticsSection: () => <div>更新日志详情</div>,
  Check: Icon, X: Icon, XIcon: Icon, RefreshCw: Icon, Info: Icon, Switch: Icon,
  localizeErrorMessage: (message: string) => message,
  formatBytes: (bytes: number) => `${bytes} B`,
  monotonicPercent: (_previous: number, next: number) => next,
  rejectControllerUpdateDiagnostics: () => Promise.reject(new Error('unavailable')),
}
const InstallDialog = new Function(...Object.keys(dependencies), `${compiled}\nreturn ControllerUpdateInstallDialog`)(...Object.values(dependencies))

it('opens logs from the quiet stepper entry and keeps update operations in the logs view', async () => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  const props = {
    phase: 'downloading', targetVersion: 'dev-test', connectionInterrupted: false,
    failure: '', canCancel: true, cancelling: false,
    fetchDiagnostics: vi.fn(async () => ({ logs: [] })),
    onCancel: vi.fn(), onInstall: vi.fn(), onInterrupt: vi.fn(),
    onForceFinish: vi.fn(), onHide: vi.fn(), onReload: vi.fn(),
  }
  const button = (label: string) => [...container.querySelectorAll('button')].find(element => element.textContent === label)!
  try {
    await act(async () => root.render(<InstallDialog {...props} />))
    expect(container.querySelector('.controller-update-immersive-head button')).toBeNull()
    expect(button('出问题了？点我').closest('.controller-update-stepper-area')).not.toBeNull()
    expect(button('中断更新')).toBeUndefined()
    expect(props.fetchDiagnostics).not.toHaveBeenCalled()

    await act(async () => button('出问题了？点我').click())
    expect(props.fetchDiagnostics).toHaveBeenCalledTimes(1)
    expect(container.querySelector('[aria-label="主控更新日志"]')?.textContent).toContain('更新日志详情')
    for (const label of ['拉取日志', '中断更新', '强制结束更新', '在后台继续']) {
      expect(button(label).closest('.controller-update-logs-face')).not.toBeNull()
    }
    await act(async () => button('拉取日志').click())
    expect(props.fetchDiagnostics).toHaveBeenCalledTimes(2)
    act(() => button('中断更新').click())
    act(() => button('强制结束更新').click())
    act(() => button('在后台继续').click())
    expect(props.onInterrupt).toHaveBeenCalledTimes(1)
    expect(props.onForceFinish).toHaveBeenCalledTimes(1)
    expect(props.onHide).toHaveBeenCalledTimes(1)
    await act(async () => button('查看进度').click())
    expect(container.querySelector('[aria-label="主控更新进度"]')).not.toBeNull()
    expect(button('中断更新')).toBeUndefined()
  } finally {
    act(() => root.unmount())
    container.remove()
    delete (globalThis as any).IS_REACT_ACT_ENVIRONMENT
  }
})
