// @vitest-environment jsdom
import React, { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { AnimatePresence } from 'motion/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { MatrixLogoLoader } from './MatrixLogoLoader'

let container: HTMLDivElement
let root: Root
beforeEach(() => {
  vi.useFakeTimers()
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})
afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  vi.useRealTimers()
  ;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = false
})
async function render(visible: boolean) {
  await act(async () => root.render(<AnimatePresence>{visible && <MatrixLogoLoader key="loader" loading />}</AnimatePresence>))
}
async function advance(ms: number) {
  await act(async () => { vi.advanceTimersByTime(ms) })
}
it('finishes the current beat, pauses, slides, then removes the overlay', async () => {
  await render(true)
  await render(false)
  expect(container.querySelector('.is-loading')).not.toBeNull()
  await act(async () => { container.querySelector('.sq-tl')!.dispatchEvent(new Event('animationiteration', { bubbles: true })) })
  expect(container.querySelector('.is-settled')).not.toBeNull()
  await advance(199)
  expect(container.querySelector('.is-settled')).not.toBeNull()
  await advance(1)
  expect(container.querySelector('.is-finished')).not.toBeNull()
  await advance(500)
  expect(container.querySelector('.is-fading')).not.toBeNull()
  await advance(300)
  expect(container.querySelector('.portal-loader')).toBeNull()
})
it('recovers without animation events and cancels completion when loading resumes', async () => {
  await render(true)
  await render(false)
  await advance(900)
  expect(container.querySelector('.is-settled')).not.toBeNull()
  await render(true)
  await advance(2000)
  expect(container.querySelector('.is-loading')).not.toBeNull()
  await render(false)
  await advance(900)
  await advance(200)
  await advance(500)
  await advance(300)
  expect(container.querySelector('.portal-loader')).toBeNull()
})
