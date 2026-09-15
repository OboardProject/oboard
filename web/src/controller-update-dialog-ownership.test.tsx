// @vitest-environment jsdom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import React, { act, useEffect, useRef, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import ts from 'typescript'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import * as update from './controller-update'
import * as diagnostics from './controller-update-diagnostics'
import { useControllerUpdatePromptAutoDismiss } from './controller-update-prompt'

const source = readFileSync(resolve(__dirname, 'main.tsx'), 'utf8')
const component = source.slice(source.indexOf('function ControllerUpdatePrompt('), source.indexOf('\nfunction ControllerUpdatePanel('))
const compiled = ts.transpileModule(component, { compilerOptions: { jsx: ts.JsxEmit.React, target: ts.ScriptTarget.ES2022 } }).outputText
const dependencies = {
  React, useEffect, useRef, useState, ...update, ...diagnostics, useControllerUpdatePromptAutoDismiss,
  usePausedInterval: () => {}, localizeErrorMessage: (message: string) => message,
  AnimatePresence: ({ children }: any) => children, m: { aside: 'aside' }, Download: () => null, X: () => null,
  ControllerUpdateInstallDialog: ({ phase, onInstall }: any) => <div role="dialog">
    <span>{phase}</span><button onClick={() => onInstall(true)}>install</button>
  </div>,
}
const Prompt = new Function(...Object.keys(dependencies), `${compiled}\nreturn ControllerUpdatePrompt`)(...Object.values(dependencies))
let root: Root
let container: HTMLDivElement
let status: any
let props: any
beforeEach(() => {
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  status = { status: 'available', current: { build: 'old' }, available: { build: 'new', version: 'new' }, update_available: true }
  props = {
    tab: 'dashboard', realtimeStatus: 'open', realtimeRevision: 0, realtimeResources: ['controller_update'],
    client: { request: vi.fn(async (path: string) => path.endsWith('/install') ? { ...status, status: 'installing' } : status) },
  }
})
afterEach(() => {
  act(() => root.unmount())
  container.remove()
  delete (globalThis as any).IS_REACT_ACT_ENVIRONMENT
})
async function render() { await act(async () => { root.render(<Prompt {...props} />) }) }
async function report(nextStatus: string) {
  status = { ...status, status: nextStatus, update_available: nextStatus !== 'installed' }
  props.realtimeRevision++
  await render()
}
async function click(label: string) {
  const button = [...container.querySelectorAll('button')].find(button => button.textContent?.includes(label))!
  await act(async () => button.click())
}
it('does not open a second result dialog for an update tracked by settings', async () => {
  props.tab = 'settings'
  await render()
  await report('installing')
  await report('installed')
  expect(container.querySelector('[role="dialog"]')).toBeNull()
  props.tab = 'dashboard'
  await render()
  expect(container.querySelector('[role="dialog"]')).toBeNull()
})
it('shows completion for an update started from the global prompt', async () => {
  await render()
  await click('确认更新')
  await click('install')
  expect(container.querySelector('[role="dialog"]')?.textContent).toContain('installing')
  await report('installed')
  expect(container.querySelectorAll('[role="dialog"]')).toHaveLength(1)
  expect(container.querySelector('[role="dialog"]')?.textContent).toContain('complete')
})
it('relinquishes its dialog when settings takes over an active update', async () => {
  await render()
  await click('确认更新')
  await click('install')
  props.tab = 'settings'
  await render()
  expect(container.querySelector('[role="dialog"]')).toBeNull()
  await report('installed')
  props.tab = 'dashboard'
  await render()
  expect(container.querySelector('[role="dialog"]')).toBeNull()
})
