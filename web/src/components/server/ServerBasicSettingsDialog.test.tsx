// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { ServerBasicSettingsDialog } from './ServerBasicSettingsDialog'
import type { Server } from '../proxy-path/types'

let host: HTMLDivElement
let root: Root

beforeEach(() => {
  ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
})

afterEach(() => {
  act(() => root.unmount())
  host.remove()
})

it('keeps a server save visibly pending and prevents another submission', async () => {
  let finish!: () => void
  const onSubmit = vi.fn(() => new Promise<void>(resolve => { finish = resolve }))
  const onCancel = vi.fn()
  await act(async () => root.render(<ServerBasicSettingsDialog server={{ id: 4, name: 'Tokyo' } as Server} onSubmit={onSubmit} onCancel={onCancel} />))

  const save = [...document.body.querySelectorAll('button')].find(button => button.textContent?.includes('保存修改'))!
  await act(async () => { save.click() })

  expect(onSubmit).toHaveBeenCalledTimes(1)
  expect(save.disabled).toBe(true)
  expect(save.getAttribute('aria-busy')).toBe('true')
  expect(save.textContent).toContain('保存中')
  await act(async () => { save.click() })
  expect(onSubmit).toHaveBeenCalledTimes(1)
  expect(document.body.textContent).toContain('Tokyo')

  await act(async () => { finish() })
  expect(save.disabled).toBe(false)
  expect(onCancel).not.toHaveBeenCalled()
})
