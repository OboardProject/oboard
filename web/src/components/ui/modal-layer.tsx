import * as React from "react"
import { createPortal } from "react-dom"
import { m, usePresence, useReducedMotion } from "motion/react"

const APPICA_SPRING = [0.175, 0.885, 0.32, 1.5] as const
const BACKDROP_EASE = [0.16, 1, 0.3, 1] as const
const FOCUSABLE_SELECTOR = [
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  'a[href]',
  '[tabindex]:not([tabindex="-1"])',
].join(', ')

type LayerID = symbol
type BodyStyleSnapshot = { overflow: string; paddingRight: string }

let layers: LayerID[] = []
let bodyStyleSnapshot: BodyStyleSnapshot | null = null
let stackFocusTarget: HTMLElement | null = null
const listeners = new Set<() => void>()

function emitLayerChange() {
  listeners.forEach(listener => listener())
}

function lockBodyScroll() {
  if (typeof document === "undefined" || bodyStyleSnapshot) return
  const body = document.body
  bodyStyleSnapshot = {
    overflow: body.style.overflow,
    paddingRight: body.style.paddingRight,
  }
  const viewportWidth = typeof window === "undefined" ? 0 : window.innerWidth
  const documentWidth = document.documentElement.clientWidth
  const scrollbarWidth = documentWidth > 0 ? Math.max(0, viewportWidth - documentWidth) : 0
  if (scrollbarWidth > 0) {
    const currentPadding = Number.parseFloat(window.getComputedStyle(body).paddingRight) || 0
    body.style.paddingRight = `${currentPadding + scrollbarWidth}px`
  }
  body.style.overflow = "hidden"
}

function unlockBodyScroll() {
  if (typeof document === "undefined" || !bodyStyleSnapshot) return
  document.body.style.overflow = bodyStyleSnapshot.overflow
  document.body.style.paddingRight = bodyStyleSnapshot.paddingRight
  bodyStyleSnapshot = null
}

function registerLayer(id: LayerID, initialFocusTarget: HTMLElement | null) {
  if (layers.includes(id)) return () => undefined
  if (layers.length === 0) {
    stackFocusTarget = initialFocusTarget
    lockBodyScroll()
  }
  layers = [...layers, id]
  emitLayerChange()
  return () => {
    if (!layers.includes(id)) return
    layers = layers.filter(layer => layer !== id)
    if (layers.length === 0) {
      const target = stackFocusTarget
      stackFocusTarget = null
      unlockBodyScroll()
      if (target?.isConnected) {
        window.requestAnimationFrame(() => {
          if (layers.length === 0 && target.isConnected) target.focus()
        })
      }
    }
    emitLayerChange()
  }
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

function getLayers() {
  return layers
}

const emptyLayers: LayerID[] = []

function useModalLayer(initialFocusTarget: HTMLElement | null) {
  const idRef = React.useRef<LayerID>(Symbol("modal-layer"))
  const snapshot = React.useSyncExternalStore(subscribe, getLayers, () => emptyLayers)

  React.useLayoutEffect(() => registerLayer(idRef.current, initialFocusTarget), [])

  const registeredIndex = snapshot.indexOf(idRef.current)
  const index = registeredIndex >= 0 ? registeredIndex : snapshot.length
  const isTopmost = registeredIndex >= 0
    ? index === snapshot.length - 1
    : snapshot.length === 0

  return { index, isTopmost }
}

function focusableElements(panel: HTMLElement) {
  return Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
    .filter(element => !element.hidden && element.getAttribute("aria-hidden") !== "true")
}

function focusFirst(panel: HTMLElement) {
  const requested = panel.querySelector<HTMLElement>('[autofocus]')
  const focusable = focusableElements(panel)
  const contentControl = focusable.find(element => !element.classList.contains('dialog-close'))
  const target = requested || contentControl || focusable[0] || panel
  target.focus()
}

export interface ModalSurfaceProps {
  onClose: () => void
  children: React.ReactNode
  panelClassName: string
  rootClassName?: string
  ariaLabel?: string
  ariaLabelledBy?: string
  restoreFocus?: HTMLElement | null
  portal?: boolean
}

export function ModalSurface({
  onClose,
  children,
  panelClassName,
  rootClassName = "",
  ariaLabel,
  ariaLabelledBy,
  restoreFocus,
  portal = true,
}: ModalSurfaceProps) {
  const shouldReduceMotion = useReducedMotion()
  const [isPresent, safeToRemove] = usePresence()
  const panelRef = React.useRef<HTMLElement | null>(null)
  const focusBeforeRender = typeof document !== "undefined" && document.activeElement instanceof HTMLElement
    ? document.activeElement
    : null
  const previousFocusRef = React.useRef<HTMLElement | null>(restoreFocus || focusBeforeRender)
  const { index, isTopmost } = useModalLayer(previousFocusRef.current)
  const capturedFocusRef = React.useRef(Boolean(restoreFocus || focusBeforeRender))
  const onCloseRef = React.useRef(onClose)
  const isTopmostRef = React.useRef(isTopmost)
  const restoreOnUnmountRef = React.useRef(isTopmost)
  const isInteractive = isTopmost && isPresent

  onCloseRef.current = onClose
  isTopmostRef.current = isInteractive
  if (isPresent) restoreOnUnmountRef.current = isTopmost

  React.useEffect(() => {
    if (isPresent || !safeToRemove) return
    const timer = window.setTimeout(safeToRemove, shouldReduceMotion ? 10 : 300)
    return () => window.clearTimeout(timer)
  }, [isPresent, safeToRemove, shouldReduceMotion])

  React.useLayoutEffect(() => {
    const panel = panelRef.current
    if (!panel) return
    if (!capturedFocusRef.current) {
      previousFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
      capturedFocusRef.current = true
    }
    panel.inert = !isInteractive
    if (!isInteractive || panel.contains(document.activeElement)) return
    const frame = window.requestAnimationFrame(() => {
      if (isTopmostRef.current && !panel.contains(document.activeElement)) focusFirst(panel)
    })
    return () => window.cancelAnimationFrame(frame)
  }, [isInteractive])

  React.useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      const panel = panelRef.current
      if (!panel || !isTopmostRef.current) return
      if (event.key === "Escape") {
        event.preventDefault()
        event.stopPropagation()
        onCloseRef.current()
        return
      }
      if (event.key !== "Tab") return
      const focusable = focusableElements(panel)
      if (focusable.length === 0) {
        event.preventDefault()
        panel.focus()
        return
      }
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && (document.activeElement === first || document.activeElement === panel)) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    const handleFocusIn = (event: FocusEvent) => {
      const panel = panelRef.current
      if (!panel || !isTopmostRef.current || panel.contains(event.target as Node)) return
      focusFirst(panel)
    }
    document.addEventListener("keydown", handleKeyDown)
    document.addEventListener("focusin", handleFocusIn)
    return () => {
      document.removeEventListener("keydown", handleKeyDown)
      document.removeEventListener("focusin", handleFocusIn)
      const previous = previousFocusRef.current
      if (!restoreOnUnmountRef.current || !previous?.isConnected) return
      window.requestAnimationFrame(() => {
        const containingPanel = previous.closest<HTMLElement>('.dialog-panel')
        if (previous.isConnected && !containingPanel?.inert) previous.focus()
      })
    }
  }, [])

  const reducedPanelState = { opacity: 1 }
  const panelInitial = shouldReduceMotion ? { opacity: 0 } : { opacity: 0, y: 14, scale: 0.96 }
  const panelAnimate = shouldReduceMotion ? reducedPanelState : { opacity: 1, y: 0, scale: 1 }
  const panelExit = shouldReduceMotion ? { opacity: 0 } : { opacity: 0, y: 10, scale: 0.97 }
  const panelTarget = isPresent ? panelAnimate : panelExit
  const layerStyle = { "--dialog-layer-index": index } as React.CSSProperties

  const content = (
    <m.div
      className={["dialog-layer", rootClassName].filter(Boolean).join(" ")}
      data-modal-index={index}
      data-modal-top={isTopmost ? "true" : "false"}
      data-modal-closing={isPresent ? "false" : "true"}
      style={layerStyle}
      role="presentation"
    >
      <m.div
        className="dialog-backdrop"
        onMouseDown={event => {
          if (isTopmostRef.current && event.target === event.currentTarget) onCloseRef.current()
        }}
        initial={{ opacity: 0 }}
        animate={{ opacity: isPresent ? 1 : 0 }}
        exit={{ opacity: 0 }}
        transition={{ duration: shouldReduceMotion ? 0.01 : 0.22, ease: BACKDROP_EASE as any }}
        aria-hidden="true"
      />
      <m.section
        ref={panelRef}
        className={["dialog-panel", panelClassName].filter(Boolean).join(" ")}
        role="dialog"
        aria-modal={isInteractive ? "true" : undefined}
        aria-hidden={isInteractive ? undefined : "true"}
        aria-label={ariaLabelledBy ? undefined : ariaLabel}
        aria-labelledby={ariaLabelledBy}
        tabIndex={-1}
        initial={panelInitial}
        animate={panelTarget}
        exit={panelExit}
        transition={{ duration: shouldReduceMotion ? 0.01 : 0.28, ease: APPICA_SPRING as any }}
      >
        {children}
      </m.section>
    </m.div>
  )

  return portal && typeof document !== "undefined" ? createPortal(content, document.body) : content
}
