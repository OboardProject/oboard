export type ThemeName = 'light' | 'dark'
export type ThemePreference = ThemeName | 'auto'

const THEME_STORAGE_KEY = 'oboard.theme'
const THEME_TRANSITION_CLASS = 'theme-crossfade'
const THEME_KEYBOARD_SETTLE_MS = 900
const THEME_KEYBOARD_MIN_WAIT_MS = 160
const THEME_KEYBOARD_MAX_WAIT_MS = 520

export function normalizeTheme(value: string | null | undefined): ThemeName {
  return value === 'dark' ? 'dark' : 'light'
}

export function getThemePreference(): ThemePreference {
  try {
    const value = localStorage.getItem(THEME_STORAGE_KEY)
    return value === 'light' || value === 'dark' ? value : 'auto'
  } catch {
    return 'auto'
  }
}

export function saveThemePreference(preference: ThemePreference) {
  try {
    localStorage.setItem(THEME_STORAGE_KEY, preference)
  } catch {
    // The choice still applies for this session when storage is unavailable.
  }
}

export function resolveTheme(preference: ThemePreference): ThemeName {
  if (preference !== 'auto') return preference
  let dark = false
  try {
    dark = window.matchMedia('(prefers-color-scheme: dark)').matches
  } catch {
    // Use the light palette when system preferences are unavailable.
  }
  return dark ? 'dark' : 'light'
}

export function watchSystemTheme(preference: ThemePreference, onChange: (theme: ThemeName) => void) {
  if (preference !== 'auto' || typeof window.matchMedia !== 'function') return
  const media = window.matchMedia('(prefers-color-scheme: dark)')
  const update = () => onChange(resolveTheme(preference))
  media.addEventListener('change', update)
  return () => media.removeEventListener('change', update)
}

export const DEFAULT_ACCENT_COLORS: Record<ThemeName, string> = {
  light: '#007aff',
  dark: '#60a5fa',
}
const ACCENT_COLOR_STORAGE_KEYS: Record<ThemeName, string> = {
  light: 'oboard.accent_color',
  dark: 'oboard.accent_color.dark',
}

export const ACCENT_COLOR_PRESETS = {
  light: [
    { name: '经典蓝', color: '#007aff' },
    { name: '极光青', color: '#0e7490' },
    { name: '翡翠绿', color: '#047857' },
    { name: '罗兰紫', color: '#7c3aed' },
    { name: '晚霞橙', color: '#c2410c' },
    { name: '热烈红', color: '#be123c' },
    { name: '流光金', color: '#a16207' },
  ],
  dark: [
    { name: '拓扑蓝', color: '#60a5fa' },
    { name: '极光青', color: '#22d3ee' },
    { name: '翡翠绿', color: '#34d399' },
    { name: '罗兰紫', color: '#a78bfa' },
    { name: '晚霞橙', color: '#fb923c' },
    { name: '热烈红', color: '#fb7185' },
    { name: '流光金', color: '#fbbf24' },
  ],
} as const

export function normalizeAccentColor(value: string | null | undefined, theme: ThemeName): string {
  if (value && /^#[0-9a-fA-F]{6}$/.test(value)) {
    return value
  }
  return DEFAULT_ACCENT_COLORS[theme]
}

export function getAccentColor(theme: ThemeName): string {
  try {
    const value = localStorage.getItem(ACCENT_COLOR_STORAGE_KEYS[theme])
    return normalizeAccentColor(value, theme)
  } catch {
    return DEFAULT_ACCENT_COLORS[theme]
  }
}

export function saveAccentColor(theme: ThemeName, color: string) {
  try {
    localStorage.setItem(ACCENT_COLOR_STORAGE_KEYS[theme], normalizeAccentColor(color, theme))
  } catch {
    // The choice still applies for this session when storage is unavailable.
  }
}

function applyAccentColorToElement(root: HTMLElement, color: string, theme: ThemeName) {
  const validColor = normalizeAccentColor(color, theme)
  const hoverColor = `color-mix(in srgb, ${validColor} 85%, ${theme === 'dark' ? '#fff' : '#111827'})`
  const contrastColor = accentContrastColor(validColor)
  root.style.setProperty('--theme-accent-color', validColor)
  root.style.setProperty('--theme-accent-hover', hoverColor)
  root.style.setProperty('--theme-accent-contrast', contrastColor)
  root.style.setProperty('--color-primary', validColor)
  root.style.setProperty('--primary', validColor)
  root.style.setProperty('--focus-ring', validColor)
  root.style.setProperty('--border-focus', validColor)
  root.style.setProperty('--color-primary-hover', hoverColor)
  root.style.setProperty('--primary-2', hoverColor)
  root.style.setProperty('--color-primary-light', `color-mix(in srgb, ${validColor} 12%, transparent)`)
  root.style.setProperty('--primary-soft', `color-mix(in srgb, ${validColor} 14%, transparent)`)
  root.style.setProperty('--primary-softer', `color-mix(in srgb, ${validColor} 6%, transparent)`)
  root.style.setProperty('--accent-contrast', contrastColor)
  root.style.setProperty('--primary-contrast', contrastColor)
  root.style.setProperty('--accent-color', validColor)
  root.style.setProperty('--accent-color-hover', hoverColor)
  root.style.setProperty('--accent-color-soft', `color-mix(in srgb, ${validColor} 12%, transparent)`)
  root.style.setProperty('--color-accent', validColor)
  root.style.setProperty('--color-accent-hover', hoverColor)
  root.style.setProperty('--color-accent-soft', `color-mix(in srgb, ${validColor} 12%, transparent)`)
  root.style.setProperty('--accent', validColor)
  root.style.setProperty('--accent-soft', `color-mix(in srgb, ${validColor} 12%, transparent)`)
}

export function applyAccentColorToDocument(color: string) {
  if (typeof document === 'undefined') return
  applyAccentColorToElement(document.documentElement, color, normalizeTheme(document.documentElement.dataset.theme))
}

// White text needs about 3:1 against a filled control; light accents such as
// the gold preset get dark text instead.
export function accentContrastColor(color: string): string {
  const channel = (offset: number) => {
    const value = Number.parseInt(color.slice(offset, offset + 2), 16) / 255
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
  }
  const luminance = 0.2126 * channel(1) + 0.7152 * channel(3) + 0.0722 * channel(5)
  return (1.05) / (luminance + 0.05) >= 3 ? '#ffffff' : '#111827'
}

const THEME_PAGE_BG: Record<ThemeName, string> = {
  light: '#f8f9fa',
  dark: '#16181d',
}

export function applyThemeToDocument(theme: ThemeName) {
  const root = document.documentElement
  root.dataset.theme = theme
  root.classList.toggle('dark', theme === 'dark')
  const pageBg = getComputedStyle(root).getPropertyValue('--bg-page').trim() || THEME_PAGE_BG[theme]
  root.style.colorScheme = theme
  root.style.backgroundColor = pageBg
  if (document.body) {
    document.body.style.backgroundColor = pageBg
  }
  applyAccentColorToDocument(getAccentColor(theme))
}

// Logical theme state for click coalescing (must track intended end-state).
let logicalTheme: ThemeName = resolveTheme(getThemePreference())

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

export function prewarmThemeTransition(): Promise<void> {
  return Promise.resolve()
}

function applyThemeImmediate(theme: ThemeName, onApplied: (theme: ThemeName) => void) {
  applyThemeToDocument(theme)
  onApplied(theme)
}

export async function transitionThemeTo(
  next: ThemeName,
  onApplied: (theme: ThemeName) => void,
): Promise<void> {
  logicalTheme = next
  if (themeTransitionRunning) return
  themeTransitionRunning = true

  try {
    if (normalizeTheme(document.documentElement.dataset.theme) === logicalTheme) {
      onApplied(logicalTheme)
      return
    }
    if (isMobileInputActiveOrSettling()) {
      const activeElement = document.activeElement
      if (isEditableElement(activeElement)) activeElement.blur()
      await waitForMobileViewportToSettle()
    }
    const targetTheme = logicalTheme
    if (prefersReducedMotion() || !document.startViewTransition) {
      applyThemeImmediate(targetTheme, onApplied)
      return
    }
    document.documentElement.classList.add(THEME_TRANSITION_CLASS)
    const transition = document.startViewTransition(() => applyThemeImmediate(targetTheme, onApplied))
    await transition.finished
  } catch {
    applyThemeImmediate(logicalTheme, onApplied)
  } finally {
    document.documentElement.classList.remove(THEME_TRANSITION_CLASS)
    themeTransitionRunning = false
    if (normalizeTheme(document.documentElement.dataset.theme) !== logicalTheme) {
      void transitionThemeTo(logicalTheme, onApplied)
    }
  }
}

export function getLogicalTheme(): ThemeName {
  return logicalTheme
}

// Initial theme setup on script load
if (typeof document !== 'undefined') {
  applyThemeToDocument(logicalTheme)
}
