export type ThemeName = 'light' | 'dark'

export type ThemeOrigin = { x: number; y: number }

const THEME_STORAGE_KEY = 'oboard.theme'
const THEME_DURATION_MS = 480
const THEME_NO_TRANSITIONS_CLASS = 'theme-no-transitions'
const THEME_KEYBOARD_SETTLE_MS = 900
const THEME_KEYBOARD_MIN_WAIT_MS = 160
const THEME_KEYBOARD_MAX_WAIT_MS = 520

export function normalizeTheme(value: string | null | undefined): ThemeName {
  return value === 'dark' ? 'dark' : 'light'
}

const THEME_PAGE_BG: Record<ThemeName, string> = {
  light: '#f7f8fa',
  dark: '#0b0d12',
}

export function applyThemeToDocument(theme: ThemeName) {
  const root = document.documentElement
  const pageBg = THEME_PAGE_BG[theme]
  root.dataset.theme = theme
  root.classList.toggle('dark', theme === 'dark')
  root.style.colorScheme = theme
  root.style.backgroundColor = pageBg
  if (document.body) {
    document.body.style.backgroundColor = pageBg
  }
  try {
    localStorage.setItem(THEME_STORAGE_KEY, theme)
  } catch {
    // Storage can be unavailable in hardened / private contexts.
  }
}

// Logical theme state for click coalescing (must track intended end-state).
let logicalTheme: ThemeName = normalizeTheme(
  (() => {
    try {
      return localStorage.getItem(THEME_STORAGE_KEY)
    } catch {
      return null
    }
  })(),
)

let themeTransitionRunning = false
let lastEditableBlurAt = Number.NEGATIVE_INFINITY

function isEditableElement(target: EventTarget | null): target is HTMLElement {
  if (!(target instanceof HTMLElement)) return false
  return target.matches('input:not([type="button"]):not([type="checkbox"]):not([type="radio"]):not([type="reset"]):not([type="submit"]), textarea, [contenteditable="true"]')
}

if (typeof document !== 'undefined') {
  document.addEventListener('focusout', event => {
    if (isEditableElement(event.target)) {
      lastEditableBlurAt = performance.now()
    }
  }, true)
}

function oppositeTheme(theme: ThemeName): ThemeName {
  return theme === 'dark' ? 'light' : 'dark'
}

function prefersReducedMotion(): boolean {
  try {
    return window.matchMedia('(prefers-reduced-motion: reduce)').matches
  } catch {
    return false
  }
}

function isMobileInputActiveOrSettling(): boolean {
  let coarsePointer = false
  try {
    coarsePointer = window.matchMedia('(pointer: coarse)').matches
  } catch {
    // Fall back to the responsive breakpoint below.
  }
  if (!coarsePointer && window.innerWidth > 980) return false

  if (isEditableElement(document.activeElement)) return true
  if (performance.now() - lastEditableBlurAt < THEME_KEYBOARD_SETTLE_MS) return true

  const viewport = window.visualViewport
  if (!viewport) return false
  const coveredHeight = window.innerHeight - viewport.height - viewport.offsetTop
  return coveredHeight > Math.max(100, window.innerHeight * 0.15)
}

type ViewportGeometry = {
  width: number
  height: number
  visualWidth: number
  visualHeight: number
  visualLeft: number
  visualTop: number
}

function readViewportGeometry(): ViewportGeometry {
  const viewport = window.visualViewport
  return {
    width: window.innerWidth,
    height: window.innerHeight,
    visualWidth: viewport?.width ?? window.innerWidth,
    visualHeight: viewport?.height ?? window.innerHeight,
    visualLeft: viewport?.offsetLeft ?? 0,
    visualTop: viewport?.offsetTop ?? 0,
  }
}

function viewportGeometryChanged(before: ViewportGeometry, after: ViewportGeometry): boolean {
  return Object.keys(before).some(key => Math.abs(before[key as keyof ViewportGeometry] - after[key as keyof ViewportGeometry]) > 2)
}

function waitForMobileViewportToSettle(): Promise<void> {
  return new Promise(resolve => {
    const startedAt = performance.now()
    let stableSince = startedAt
    let previous = readViewportGeometry()

    const step = (now: number) => {
      const current = readViewportGeometry()
      if (viewportGeometryChanged(previous, current)) {
        previous = current
        stableSince = now
      }

      const elapsed = now - startedAt
      const stableFor = now - stableSince
      if (
        elapsed >= THEME_KEYBOARD_MAX_WAIT_MS
        || (elapsed >= THEME_KEYBOARD_MIN_WAIT_MS && stableFor >= 100)
      ) {
        resolve()
        return
      }
      requestAnimationFrame(step)
    }

    requestAnimationFrame(step)
  })
}

/** Center of the clicked control, or click point, or viewport center. */
export function resolveThemeOrigin(
  event?: {
    currentTarget?: EventTarget | null
    target?: EventTarget | null
    clientX?: number
    clientY?: number
    touches?: ArrayLike<{ clientX: number; clientY: number }>
    changedTouches?: ArrayLike<{ clientX: number; clientY: number }>
  } | null,
): ThemeOrigin {
  // 1. Target element bounding rectangle (exact button center)
  const rawTarget = (event?.currentTarget || event?.target) as Element | null
  const target = rawTarget?.closest
    ? rawTarget.closest('button, a, [role="button"], label, input, .login-ghost-link, .login-theme-inline, .sidebar-footer-btn') || rawTarget
    : rawTarget

  if (target instanceof Element) {
    const rect = target.getBoundingClientRect()
    if (rect.width > 0 && rect.height > 0) {
      return {
        x: rect.left + rect.width / 2,
        y: rect.top + rect.height / 2,
      }
    }
  }

  // 2. Query active theme toggle elements in DOM
  const activeToggle = document.querySelector('.login-theme-inline, .login-ghost-link, .sidebar-footer-btn')
  if (activeToggle) {
    const rect = activeToggle.getBoundingClientRect()
    if (rect.width > 0 && rect.height > 0) {
      return {
        x: rect.left + rect.width / 2,
        y: rect.top + rect.height / 2,
      }
    }
  }

  // 3. Pointer click or touch coordinates
  const touch = event?.touches?.[0] || event?.changedTouches?.[0]
  const px = typeof touch?.clientX === 'number' ? touch.clientX : event?.clientX
  const py = typeof touch?.clientY === 'number' ? touch.clientY : event?.clientY

  if (typeof px === 'number' && typeof py === 'number' && px > 0 && py > 0) {
    return { x: px, y: py }
  }

  // 4. Fallback: Center of viewport
  return {
    x: Math.max(0, window.innerWidth / 2),
    y: Math.max(0, window.innerHeight / 2),
  }
}

function maxRevealRadiusInRect(x: number, y: number, w: number, h: number): number {
  const corners: Array<[number, number]> = [
    [0, 0],
    [w, 0],
    [0, h],
    [w, h],
  ]
  let max = 0
  for (const [cx, cy] of corners) {
    const d = Math.hypot(x - cx, y - cy)
    if (d > max) max = d
  }
  return Math.ceil(max) + 2
}

export function prewarmThemeTransition(): Promise<void> {
  return Promise.resolve()
}

function applyThemeImmediate(theme: ThemeName, onApplied: (theme: ThemeName) => void) {
  applyThemeToDocument(theme)
  onApplied(theme)
}

/**
 * Real-time expanding hole mask theme transition ("扩散到哪变到哪").
 * Scopes to .login-panel on the login screen (affecting only the right half),
 * and disables CSS transitions on real DOM during ripple for sharp sub-element pixel splitting.
 */
export async function transitionThemeTo(
  next: ThemeName,
  origin: ThemeOrigin,
  onApplied: (theme: ThemeName) => void,
): Promise<void> {
  logicalTheme = next

  if (themeTransitionRunning) {
    return
  }

  themeTransitionRunning = true
  const targetTheme = logicalTheme
  const currentTheme = normalizeTheme(document.documentElement.dataset.theme)

  if (currentTheme === targetTheme) {
    onApplied(targetTheme)
    themeTransitionRunning = false
    return
  }

  // A mobile soft keyboard changes the visual viewport while the click is
  // dispatched. Let it close and settle before changing color-scheme; otherwise
  // both the keyboard and a fixed snapshot can flash or move mid-transition.
  if (isMobileInputActiveOrSettling()) {
    const activeElement = document.activeElement
    if (isEditableElement(activeElement)) activeElement.blur()
    await waitForMobileViewportToSettle()
    applyThemeImmediate(logicalTheme, onApplied)
    themeTransitionRunning = false
    return
  }

  if (prefersReducedMotion()) {
    applyThemeImmediate(targetTheme, onApplied)
    themeTransitionRunning = false
    return
  }

  // Scope to .login-panel on the login page (right half), or root element on dashboard
  const targetEl = (document.querySelector('.login-panel') || document.getElementById('root') || document.body) as HTMLElement
  if (!targetEl) {
    applyThemeImmediate(targetTheme, onApplied)
    themeTransitionRunning = false
    return
  }

  const rect = targetEl.getBoundingClientRect()
  const relX = origin.x - rect.left
  const relY = origin.y - rect.top
  const maxR = maxRevealRadiusInRect(relX, relY, rect.width, rect.height)

  let oldLayer: HTMLElement | null = null
  let ring: HTMLElement | null = null
  let viewportChanged = false
  const initialViewport = readViewportGeometry()
  const markViewportChange = () => {
    if (viewportGeometryChanged(initialViewport, readViewportGeometry())) {
      viewportChanged = true
    }
  }
  window.addEventListener('resize', markViewportChange, { passive: true })
  window.visualViewport?.addEventListener('resize', markViewportChange, { passive: true })
  window.visualViewport?.addEventListener('scroll', markViewportChange, { passive: true })

  try {
    // 1. Snapshot OLD theme state into overlay BEFORE switching theme
    oldLayer = targetEl.cloneNode(true) as HTMLElement
    oldLayer.id = 'oboard-theme-old-layer'
    oldLayer.className = targetEl.className
    oldLayer.dataset.theme = currentTheme
    oldLayer.classList.toggle('dark', currentTheme === 'dark')

    // Copy scroll states from original elements
    const origEls = targetEl.querySelectorAll('*')
    const cloneEls = oldLayer.querySelectorAll('*')
    origEls.forEach((orig, i) => {
      if (orig.scrollTop > 0 || orig.scrollLeft > 0) {
        const c = cloneEls[i]
        if (c) {
          c.scrollTop = orig.scrollTop
          c.scrollLeft = orig.scrollLeft
        }
      }
    })

    // Fixed overlay positioned exactly over targetEl
    oldLayer.style.position = 'fixed'
    oldLayer.style.top = `${rect.top}px`
    oldLayer.style.left = `${rect.left}px`
    oldLayer.style.width = `${rect.width}px`
    oldLayer.style.height = `${rect.height}px`
    oldLayer.style.zIndex = '999999'
    oldLayer.style.pointerEvents = 'none'
    oldLayer.style.userSelect = 'none'
    oldLayer.style.overflow = 'hidden'
    oldLayer.style.margin = '0'
    oldLayer.style.padding = `${window.getComputedStyle(targetEl).padding}`
    oldLayer.style.backgroundColor = THEME_PAGE_BG[currentTheme]

    // Create glowing water ripple edge ring at absolute screen coordinates
    ring = document.createElement('div')
    ring.className = 'oboard-theme-ripple-ring'
    ring.style.position = 'fixed'
    ring.style.top = `${origin.y}px`
    ring.style.left = `${origin.x}px`
    ring.style.width = '0px'
    ring.style.height = '0px'
    ring.style.borderRadius = '50%'
    ring.style.transform = 'translate(-50%, -50%)'
    ring.style.pointerEvents = 'none'
    ring.style.zIndex = '1000000'
    ring.style.boxShadow = targetTheme === 'dark'
      ? '0 0 24px 6px rgba(147, 197, 253, 0.45), inset 0 0 16px 4px rgba(255, 255, 255, 0.35)'
      : '0 0 24px 6px rgba(59, 130, 246, 0.4), inset 0 0 16px 4px rgba(17, 24, 39, 0.25)'
    ring.style.opacity = '0.95'

    document.body.appendChild(oldLayer)
    document.body.appendChild(ring)

    // Disable CSS transitions on real DOM during ripple so real DOM snaps instantly to targetTheme colors
    document.documentElement.classList.add(THEME_NO_TRANSITIONS_CLASS)

    // 2. Commit NEW theme immediately to real DOM underneath oldLayer
    applyThemeToDocument(targetTheme)
    onApplied(targetTheme)

    // 3. Animate expanding transparent radial mask hole on oldLayer to reveal newTheme in real time
    const startTime = performance.now()

    await new Promise<void>(resolve => {
      function step(now: number) {
        if (viewportChanged) {
          resolve()
          return
        }

        const elapsed = Math.max(0, now - startTime)
        const progress = Math.min(1, elapsed / THEME_DURATION_MS)
        // Smooth easeOutCubic
        const ease = 1 - Math.pow(1 - progress, 3)
        const currentR = Math.ceil(ease * maxR)
        const blurEdge = Math.max(0, currentR - 2)

        const maskStr = `radial-gradient(circle at ${relX}px ${relY}px, transparent 0px, transparent ${blurEdge}px, #000 ${currentR}px)`

        if (oldLayer) {
          oldLayer.style.webkitMaskImage = maskStr
          oldLayer.style.maskImage = maskStr
        }

        if (ring) {
          const d = currentR * 2
          ring.style.width = `${d}px`
          ring.style.height = `${d}px`
          ring.style.opacity = `${(0.95 * (1 - progress)).toFixed(2)}`
        }

        if (progress < 1) {
          requestAnimationFrame(step)
        } else {
          resolve()
        }
      }
      requestAnimationFrame(step)
    })
  } catch {
    applyThemeImmediate(targetTheme, onApplied)
  } finally {
    window.removeEventListener('resize', markViewportChange)
    window.visualViewport?.removeEventListener('resize', markViewportChange)
    window.visualViewport?.removeEventListener('scroll', markViewportChange)
    oldLayer?.remove()
    ring?.remove()
    document.documentElement.classList.remove(THEME_NO_TRANSITIONS_CLASS)
    themeTransitionRunning = false

    if (normalizeTheme(document.documentElement.dataset.theme) !== logicalTheme) {
      applyThemeImmediate(logicalTheme, onApplied)
    }
  }
}

export function toggleThemeWithTransition(
  event: {
    currentTarget?: EventTarget | null
    target?: EventTarget | null
    clientX?: number
    clientY?: number
    touches?: ArrayLike<{ clientX: number; clientY: number }>
    changedTouches?: ArrayLike<{ clientX: number; clientY: number }>
  } | null | undefined,
  onApplied: (theme: ThemeName) => void,
): void {
  const origin = resolveThemeOrigin(event)
  if (!themeTransitionRunning) {
    logicalTheme = normalizeTheme(document.documentElement.dataset.theme)
  }
  const next = oppositeTheme(logicalTheme)
  void transitionThemeTo(next, origin, onApplied)
}

export function getLogicalTheme(): ThemeName {
  return logicalTheme
}

// Initial theme setup on script load
try {
  const initial = normalizeTheme(localStorage.getItem(THEME_STORAGE_KEY))
  logicalTheme = initial
  applyThemeToDocument(initial)
} catch {
  // Storage unavailable
}
