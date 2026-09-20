import * as React from "react"
import { createPortal } from "react-dom"
import { m, usePresence, useReducedMotion } from "motion/react"
import { trackModalViewport } from "./modal-viewport"
import "./drawer.css"
import { useSurfaceResize, type SurfaceMotion } from "./surface-motion"

const APPICA_SPRING = [0.175, 0.885, 0.32, 1.5] as const
const BACKDROP_EASE = [0.16, 1, 0.3, 1] as const
const POPOVER_SELECTOR = [
  '.custom-select-menu',
  '.searchable-multi-select-menu',
  '.searchable-combobox-menu',
  '.region-picker-panel',
  '.server-region-dropdown-panel',
  '.server-actions-menu',
  '.action-menu-portal',
  '.node-scope-menu',
  '.node-scope-menu-overlay',
  '[data-popover]',
  '[role="menu"]',
  '[role="listbox"]',
].join(', ')
const FOCUSABLE_SELECTOR = [
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  'a[href]',
  '[tabindex]:not([tabindex="-1"])',
].join(', ')

function isInsidePopover(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  const scope = target.closest<HTMLElement>('[data-popover-layer]')
  return Boolean(target.closest(POPOVER_SELECTOR)) && (scope ? scope.dataset.popoverActive === 'true' : Boolean(target.closest('[data-modal-top="true"]')))
}

type LayerID = symbol
const ModalOwnerContext = React.createContext<LayerID | null>(null)
type BodyStyleSnapshot = { overflow: string; paddingRight: string }

let layers: LayerID[] = []
const layerOwners = new Map<LayerID, LayerID | null>()
let bodyStyleSnapshot: BodyStyleSnapshot | null = null
let mainStyleSnapshot: string | null = null
let releaseViewport: (() => void) | undefined
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
  releaseViewport = trackModalViewport()

  const main = document.querySelector<HTMLElement>('.main')
  if (main) {
    mainStyleSnapshot = main.style.overflowY
    main.style.overflowY = "hidden"
  }
}

function unlockBodyScroll() {
  if (typeof document === "undefined" || !bodyStyleSnapshot) return
  document.body.style.overflow = bodyStyleSnapshot.overflow
  document.body.style.paddingRight = bodyStyleSnapshot.paddingRight
  bodyStyleSnapshot = null
  releaseViewport?.()
  releaseViewport = undefined

  const main = document.querySelector<HTMLElement>('.main')
  if (main && mainStyleSnapshot !== null) {
    main.style.overflowY = mainStyleSnapshot
    mainStyleSnapshot = null
  }
  if (typeof window !== "undefined" && typeof window.scrollTo === "function" && !navigator.userAgent.includes("jsdom")) {
    try {
      window.scrollTo(window.scrollX, window.scrollY)
    } catch {}
  }
}

function registerLayer(id: LayerID, initialFocusTarget: HTMLElement | null, owner: LayerID | null) {
  if (layers.includes(id)) return () => undefined
  if (layers.length === 0) {
    stackFocusTarget = initialFocusTarget
    lockBodyScroll()
  }
  layerOwners.set(id, owner)
  const descendantIndex = layers.findIndex(layer => {
    let ancestor = layerOwners.get(layer)
    while (ancestor) {
      if (ancestor === id) return true
      ancestor = layerOwners.get(ancestor)
    }
    return false
  })
  const insertAt = descendantIndex < 0 ? layers.length : descendantIndex
  layers = [...layers.slice(0, insertAt), id, ...layers.slice(insertAt)]
  emitLayerChange()
  return () => {
    if (!layers.includes(id)) return
    layers = layers.filter(layer => layer !== id)
    layerOwners.delete(id)
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
  const owner = React.useContext(ModalOwnerContext)
  const idRef = React.useRef<LayerID>(Symbol("modal-layer"))
  const snapshot = React.useSyncExternalStore(subscribe, getLayers, () => emptyLayers)

  React.useLayoutEffect(() => registerLayer(idRef.current, initialFocusTarget, owner), [])

  const registeredIndex = snapshot.indexOf(idRef.current)
  const index = registeredIndex >= 0 ? registeredIndex : snapshot.length
  const isTopmost = registeredIndex >= 0
    ? index === snapshot.length - 1
    : snapshot.length === 0

  return { id: idRef.current, index, isTopmost, count: snapshot.length }
}

function PopoverLayer({ children }: { children: React.ReactNode }) {
  const owner = React.useContext(ModalOwnerContext)
  const snapshot = React.useSyncExternalStore(subscribe, getLayers, () => emptyLayers)
  const index = owner ? snapshot.indexOf(owner) : -1
  const active = owner ? index >= 0 && index === snapshot.length - 1 : snapshot.length === 0
  return <div
    data-popover-layer=""
    data-popover-active={active ? 'true' : 'false'}
    className="popover-layer"
    inert={!active}
    aria-hidden={active ? undefined : true}
    style={{ '--popover-owner-index': index } as React.CSSProperties}
  >{children}</div>
}

export function createPopoverPortal(children: React.ReactNode, container: Element | DocumentFragment) {
  return createPortal(<PopoverLayer>{children}</PopoverLayer>, container)
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

export type ModalPlacement = "center" | "right"
export type DrawerSize = "compact" | "wide"

export interface ModalSurfaceProps {
  onClose: () => void
  children: React.ReactNode
  panelClassName: string
  rootClassName?: string
  ariaLabel?: string
  ariaLabelledBy?: string
  restoreFocus?: HTMLElement | null
  portal?: boolean
  surfaceMotion?: SurfaceMotion
  placement?: ModalPlacement
  drawerSize?: DrawerSize
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
  surfaceMotion = "form",
  placement = "center",
  drawerSize = "compact",
}: ModalSurfaceProps) {
  const shouldReduceMotion = useReducedMotion()
  const [isPresent, safeToRemove] = usePresence()
  const panelRef = React.useRef<HTMLElement | null>(null)
  const focusBeforeRender = typeof document !== "undefined" && document.activeElement instanceof HTMLElement
    ? document.activeElement
    : null
  const previousFocusRef = React.useRef<HTMLElement | null>(restoreFocus || focusBeforeRender)
  const { id, index, isTopmost, count } = useModalLayer(previousFocusRef.current)
  useSurfaceResize(panelRef, placement === "center" && !shouldReduceMotion && isPresent && isTopmost, surfaceMotion)
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
    if (!isInteractive || panel.contains(document.activeElement) || isInsidePopover(document.activeElement)) return
    const frame = window.requestAnimationFrame(() => {
      const active = document.activeElement
      if (isTopmostRef.current && active && !panel.contains(active) && !isInsidePopover(active)) focusFirst(panel)
    })
    return () => window.cancelAnimationFrame(frame)
  }, [isInteractive])

  React.useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      const panel = panelRef.current
      const target = event.target as HTMLElement | null
      if (!panel || !isTopmostRef.current) return
      if (isInsidePopover(target) || isInsidePopover(document.activeElement)) return
      if (event.key === "Escape") {
        const activePopover = document.querySelector('[data-popover-active="true"]')
        if (activePopover?.querySelector(POPOVER_SELECTOR) || panel.querySelector(POPOVER_SELECTOR)) return
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
      const target = event.target as HTMLElement | null
      if (!panel || !isTopmostRef.current || panel.contains(event.target as Node) || isInsidePopover(target)) return
      focusFirst(panel)
    }
    document.addEventListener("keydown", handleKeyDown)
    document.addEventListener("focusin", handleFocusIn)
    const panel = panelRef.current
    const preventGesture = (event: Event) => {
      event.preventDefault()
    }
    const preventMultiTouch = (event: TouchEvent) => {
      if (event.touches.length > 1) {
        event.preventDefault()
      }
    }
    if (panel) {
      panel.addEventListener("gesturestart", preventGesture as EventListener, { passive: false })
      panel.addEventListener("gesturechange", preventGesture as EventListener, { passive: false })
      panel.addEventListener("touchstart", preventMultiTouch as EventListener, { passive: false })
    }
    return () => {
      document.removeEventListener("keydown", handleKeyDown)
      document.removeEventListener("focusin", handleFocusIn)
      if (panel) {
        panel.removeEventListener("gesturestart", preventGesture as EventListener)
        panel.removeEventListener("gesturechange", preventGesture as EventListener)
        panel.removeEventListener("touchstart", preventMultiTouch as EventListener)
      }
      const previous = previousFocusRef.current
      if (!restoreOnUnmountRef.current || !previous?.isConnected) return
      window.requestAnimationFrame(() => {
        const containingPanel = previous.closest<HTMLElement>('.dialog-panel')
        if (previous.isConnected && !containingPanel?.inert) previous.focus()
      })
    }
  }, [])

  const reducedPanelState = { opacity: 1 }
  const isDrawer = placement === "right"
  const panelInitial = shouldReduceMotion ? { opacity: 0 } : isDrawer ? { opacity: 0, x: 24 } : { opacity: 0, y: 8, scale: 0.985 }
  const panelAnimate = shouldReduceMotion ? reducedPanelState : { opacity: 1, x: 0, y: 0, scale: 1 }
  const panelExit = shouldReduceMotion ? { opacity: 0 } : isDrawer ? { opacity: 0, x: 24 } : { opacity: 0, y: 5, scale: 0.99 }
  const panelTarget = isPresent ? panelAnimate : panelExit
  const layerStyle = { "--dialog-layer-index": index } as React.CSSProperties

  const content = (
    <m.div
      className={["dialog-layer", rootClassName].filter(Boolean).join(" ")}
      data-modal-placement={placement}
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
        animate={{ opacity: isPresent || count > 1 ? 1 : 0 }}
        exit={{ opacity: 0 }}
        transition={{ duration: shouldReduceMotion ? 0.01 : 0.22, ease: BACKDROP_EASE as any }}
        aria-hidden="true"
      />
      <m.section
        ref={panelRef}
        className={["dialog-panel", panelClassName].filter(Boolean).join(" ")}
        data-drawer-size={isDrawer ? drawerSize : undefined}
        role="dialog"
        aria-modal={isInteractive ? "true" : undefined}
        aria-hidden={isInteractive ? undefined : "true"}
        aria-label={ariaLabelledBy ? undefined : ariaLabel}
        aria-labelledby={ariaLabelledBy}
        tabIndex={-1}
        initial={panelInitial}
        animate={panelTarget}
        exit={panelExit}
        transition={{ duration: shouldReduceMotion ? 0.01 : isDrawer ? 0.22 : 0.28, ease: APPICA_SPRING as any }}
      >
        <ModalOwnerContext.Provider value={id}>{children}</ModalOwnerContext.Provider>
      </m.section>
    </m.div>
  )

  return portal && typeof document !== "undefined" ? createPortal(content, document.body) : content
}
