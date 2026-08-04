// @vitest-environment jsdom

import React, { act } from "react"
import { createRoot, type Root } from "react-dom/client"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { Toast, type ToastProps } from "./toast"

vi.mock("motion/react", () => ({
  useReducedMotion: () => false,
  m: {
    div: ({ initial: _initial, animate: _animate, exit: _exit, ...props }: React.HTMLAttributes<HTMLDivElement>) => <div {...props} />,
  },
}))

describe("Toast", () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    vi.useFakeTimers()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true
    container = document.createElement("div")
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
    vi.useRealTimers()
    vi.restoreAllMocks()
    ;(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = false
  })

  const renderToast = (props: Partial<ToastProps> = {}) => {
    const onClose = props.onClose || vi.fn()
    act(() => root.render(<Toast message="操作成功" onClose={onClose} {...props} />))
    return onClose
  }

  it.each([
    { label: "ordinary messages", props: {}, duration: 4_000 },
    { label: "errors", props: { kind: "error" as const }, duration: 6_000 },
    { label: "long messages", props: { message: "长".repeat(81) }, duration: 10_000 },
  ])("automatically closes $label after the expected duration", ({ props, duration }) => {
    const onClose = renderToast(props)

    act(() => vi.advanceTimersByTime(duration - 1))
    expect(onClose).not.toHaveBeenCalled()

    act(() => vi.advanceTimersByTime(1))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it("keeps the original deadline across parent rerenders and uses the latest callback", () => {
    const firstOnClose = vi.fn()
    const latestOnClose = vi.fn()
    renderToast({ onClose: firstOnClose })

    act(() => vi.advanceTimersByTime(2_000))
    renderToast({ onClose: latestOnClose })
    act(() => vi.advanceTimersByTime(1_999))

    expect(firstOnClose).not.toHaveBeenCalled()
    expect(latestOnClose).not.toHaveBeenCalled()

    act(() => vi.advanceTimersByTime(1))
    expect(firstOnClose).not.toHaveBeenCalled()
    expect(latestOnClose).toHaveBeenCalledTimes(1)
  })

  it("pauses while hovered or focused and resumes with the remaining time", () => {
    const onClose = renderToast()
    const toast = container.querySelector<HTMLElement>(".toast")!
    const closeButton = container.querySelector<HTMLButtonElement>(".toast-close")!

    act(() => vi.advanceTimersByTime(1_000))
    act(() => toast.dispatchEvent(new MouseEvent("mouseover", { bubbles: true })))
    act(() => closeButton.focus())
    act(() => toast.dispatchEvent(new MouseEvent("mouseout", { bubbles: true })))
    act(() => vi.advanceTimersByTime(5_000))
    expect(onClose).not.toHaveBeenCalled()

    act(() => closeButton.blur())
    act(() => vi.advanceTimersByTime(2_999))
    expect(onClose).not.toHaveBeenCalled()

    act(() => vi.advanceTimersByTime(1))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it("closes immediately from the close button", () => {
    const onClose = renderToast()
    const closeButton = container.querySelector<HTMLButtonElement>(".toast-close")!

    act(() => closeButton.click())
    expect(onClose).toHaveBeenCalledTimes(1)

    act(() => vi.advanceTimersByTime(10_000))
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
