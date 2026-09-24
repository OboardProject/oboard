// Liquid glass refraction for the glass themes.
//
// The optics follow iyinchao/liquid-glass-studio (MIT): a rounded-rect SDF
// gives the distance to the edge and the outward normal, and inside a thin
// bevel the Snell edge factor -tan(asin(sin(θi) / n) - θi) with
// θi = asin((1 - d / thickness)^2) bends the backdrop inward. Instead of a
// WebGL pass over a known image, the offsets are baked into a displacement
// map that an SVG filter applies to the live backdrop, one channel per color
// so the rim shows the same slight dispersion. Only Chromium applies SVG
// filters inside backdrop-filter; other engines keep the CSS frosted glass.

const REFRACTIVE_INDEX = 1.5
const BEVEL_PX = 14
const MAX_OFFSET_PX = 9
const DISPERSION = 0.14
const MAX_FILTERS = 96
const MAX_MAP_SIDE = 640
const SCAN_DELAY_MS = 120

export const LIQUID_GLASS_SELECTOR = [
  '.ui-btn-primary',
  '.ui-btn-secondary',
  '.ui-btn-outline',
  '.ui-btn-danger',
  '.btn-secondary',
  'button.primary',
  '.login-submit',
  '.dash-manage-btn',
  '.server-action-group',
  '.dialog-actions > button',
  '.dialog-chrome-foot > button',
  '.controller-url-warning > button:not([class])',
  '.settings-actions > button:not([class])',
  '.ui-segmented-indicator',
  '.ui-tabs-list button.active',
  '.ui-tabs-trigger.active',
  '.settings-tabs button.active',
  '.dns-management-tabs button.active',
  '.server-dialog-tabs button.active',
  '.audit-console-tabs button.active',
  ':is(.node-mode-switch, .node-workspace-tabs) button.active',
  '.nav-item.active',
  'button:not([class])[aria-pressed="true"]',
  'button.ghost[aria-pressed="true"]',
  '.task-category-filter > button[aria-pressed="true"]',
  '.server-region-filter-trigger.is-active',
  '.notification-type-toggle button.active',
  '.controller-update-channel-toggle button.active',
  '.controller-update-interval button.active',
  '.age-policy-toggle button.active',
  '.udp-mode-selector button.active',
  '.automation-connect-segments button.active',
  '.latency-pill-btn.active',
].join(', ')

// Dense repeated rows would stack dozens of live backdrop filters; they keep
// the lighter CSS glass.
const EXCLUDED_CONTAINERS = 'table, [role="row"], .table-wrap, .activity-list'

const SVG_NS = 'http://www.w3.org/2000/svg'
const REFRACT_VAR = '--lg-refract'

type Shape = { width: number; height: number; radius: number }

let defs: SVGDefsElement | null = null
const filters = new Map<string, string>()
const tracked = new Set<HTMLElement>()
let resizeObserver: ResizeObserver | null = null
let mutationObserver: MutationObserver | null = null
let scanTimer: ReturnType<typeof setTimeout> | null = null
let filterSeq = 0

export function supportsBackdropRefraction(): boolean {
  if (typeof window === 'undefined' || typeof document === 'undefined') return false
  if (typeof ResizeObserver !== 'function' || typeof MutationObserver !== 'function') return false
  // userAgentData is Chromium-only, and Chromium is the only engine that
  // renders url() filters inside backdrop-filter.
  if (!('userAgentData' in navigator)) return false
  try {
    if (window.matchMedia('(prefers-reduced-transparency: reduce)').matches) return false
  } catch {
    // Older engines without the media feature keep refraction.
  }
  return typeof CSS !== 'undefined' && CSS.supports('backdrop-filter', 'blur(1px)')
}

function sdRoundRect(px: number, py: number, hw: number, hh: number, r: number): number {
  const qx = Math.abs(px) - hw + r
  const qy = Math.abs(py) - hh + r
  const ox = Math.max(qx, 0)
  const oy = Math.max(qy, 0)
  return Math.min(Math.max(qx, qy), 0) + Math.hypot(ox, oy) - r
}

function edgeFactor(depth: number): number {
  if (depth >= BEVEL_PX) return 0
  const ratio = 1 - depth / BEVEL_PX
  const thetaI = Math.asin(Math.min(1, ratio * ratio))
  const thetaT = Math.asin(Math.min(1, Math.sin(thetaI) / REFRACTIVE_INDEX))
  return -Math.tan(thetaT - thetaI)
}

// Peak edge factor at the very rim, used to normalise the map to [-1, 1].
const EDGE_PEAK = edgeFactor(0)

export function buildDisplacementMap(shape: Shape, scale = 1): Uint8ClampedArray {
  const w = Math.max(1, Math.round(shape.width * scale))
  const h = Math.max(1, Math.round(shape.height * scale))
  const hw = shape.width / 2
  const hh = shape.height / 2
  const r = Math.min(shape.radius, hw, hh)
  const data = new Uint8ClampedArray(w * h * 4)
  const eps = 0.5
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const px = (x + 0.5) / scale - hw
      const py = (y + 0.5) / scale - hh
      const d = sdRoundRect(px, py, hw, hh, r)
      let dx = 0
      let dy = 0
      if (d < 0) {
        const factor = edgeFactor(-d) / EDGE_PEAK
        if (factor > 0) {
          const nx = sdRoundRect(px + eps, py, hw, hh, r) - sdRoundRect(px - eps, py, hw, hh, r)
          const ny = sdRoundRect(px, py + eps, hw, hh, r) - sdRoundRect(px, py - eps, hw, hh, r)
          const len = Math.hypot(nx, ny) || 1
          // Sample from further inside the glass: opposite the outward normal.
          dx = (-nx / len) * factor
          dy = (-ny / len) * factor
        }
      }
      const i = (y * w + x) * 4
      data[i] = 128 + dx * 127
      data[i + 1] = 128 + dy * 127
      data[i + 2] = 128
      data[i + 3] = 255
    }
  }
  return data
}

function mapDataUrl(shape: Shape): string | null {
  const scale = Math.min(window.devicePixelRatio || 1, 2, MAX_MAP_SIDE / Math.max(shape.width, shape.height))
  const canvas = document.createElement('canvas')
  canvas.width = Math.max(1, Math.round(shape.width * scale))
  canvas.height = Math.max(1, Math.round(shape.height * scale))
  const ctx = canvas.getContext('2d')
  if (!ctx) return null
  const image = ctx.createImageData(canvas.width, canvas.height)
  image.data.set(buildDisplacementMap(shape, scale))
  ctx.putImageData(image, 0, 0)
  return canvas.toDataURL('image/png')
}

function ensureDefs(): SVGDefsElement {
  if (defs?.isConnected) return defs
  const svg = document.createElementNS(SVG_NS, 'svg')
  svg.setAttribute('aria-hidden', 'true')
  svg.setAttribute('width', '0')
  svg.setAttribute('height', '0')
  svg.style.position = 'absolute'
  svg.style.width = '0'
  svg.style.height = '0'
  svg.style.overflow = 'hidden'
  svg.style.pointerEvents = 'none'
  svg.id = 'oboard-liquid-glass-filters'
  defs = document.createElementNS(SVG_NS, 'defs')
  svg.appendChild(defs)
  document.body.appendChild(svg)
  return defs
}

function channelPass(parent: SVGFilterElement, scale: number, row: string, result: string) {
  const displace = document.createElementNS(SVG_NS, 'feDisplacementMap')
  displace.setAttribute('in', 'SourceGraphic')
  displace.setAttribute('in2', 'map')
  displace.setAttribute('scale', scale.toFixed(2))
  displace.setAttribute('xChannelSelector', 'R')
  displace.setAttribute('yChannelSelector', 'G')
  displace.setAttribute('result', `${result}-d`)
  const isolate = document.createElementNS(SVG_NS, 'feColorMatrix')
  isolate.setAttribute('in', `${result}-d`)
  isolate.setAttribute('type', 'matrix')
  isolate.setAttribute('values', `${row} 0 0 0 1 0`)
  isolate.setAttribute('result', result)
  parent.append(displace, isolate)
}

function filterFor(shape: Shape): string | null {
  const key = `${shape.width}x${shape.height}r${shape.radius}`
  const cached = filters.get(key)
  if (cached) return cached
  const href = mapDataUrl(shape)
  if (!href) return null

  if (filters.size >= MAX_FILTERS) {
    const [oldestKey, oldestId] = filters.entries().next().value as [string, string]
    filters.delete(oldestKey)
    defs?.querySelector(`#${oldestId}`)?.remove()
  }

  const id = `oboard-lg-${++filterSeq}`
  const filter = document.createElementNS(SVG_NS, 'filter')
  filter.id = id
  filter.setAttribute('x', '0')
  filter.setAttribute('y', '0')
  filter.setAttribute('width', String(shape.width))
  filter.setAttribute('height', String(shape.height))
  filter.setAttribute('filterUnits', 'userSpaceOnUse')
  filter.setAttribute('color-interpolation-filters', 'sRGB')

  const image = document.createElementNS(SVG_NS, 'feImage')
  image.setAttribute('href', href)
  image.setAttribute('x', '0')
  image.setAttribute('y', '0')
  image.setAttribute('width', String(shape.width))
  image.setAttribute('height', String(shape.height))
  image.setAttribute('preserveAspectRatio', 'none')
  image.setAttribute('result', 'map')
  filter.appendChild(image)

  // feDisplacementMap moves by scale * (channel - 0.5), so a full-range
  // channel reaches MAX_OFFSET_PX at scale 2 * MAX_OFFSET_PX.
  const base = MAX_OFFSET_PX * 2
  channelPass(filter, base * (1 + DISPERSION), '1 0 0 0 0  0 0 0 0 0  0 0 0 0 0 ', 'r')
  channelPass(filter, base, '0 0 0 0 0  0 1 0 0 0  0 0 0 0 0 ', 'g')
  channelPass(filter, base * (1 - DISPERSION), '0 0 0 0 0  0 0 0 0 0  0 0 1 0 0 ', 'b')
  const rg = document.createElementNS(SVG_NS, 'feBlend')
  rg.setAttribute('in', 'r')
  rg.setAttribute('in2', 'g')
  rg.setAttribute('mode', 'screen')
  rg.setAttribute('result', 'rg')
  const rgb = document.createElementNS(SVG_NS, 'feBlend')
  rgb.setAttribute('in', 'rg')
  rgb.setAttribute('in2', 'b')
  rgb.setAttribute('mode', 'screen')
  filter.append(rg, rgb)

  ensureDefs().appendChild(filter)
  filters.set(key, id)
  return id
}

function readShape(el: HTMLElement, width: number, height: number): Shape | null {
  const w = Math.round(width)
  const h = Math.round(height)
  if (w < 8 || h < 8) return null
  const style = getComputedStyle(el)
  const radius = Number.parseFloat(style.borderTopLeftRadius) || 0
  return { width: w, height: h, radius: Math.round(Math.min(radius, w / 2, h / 2)) }
}

function applyTo(el: HTMLElement, width: number, height: number) {
  const shape = readShape(el, width, height)
  const id = shape ? filterFor(shape) : null
  if (id) el.style.setProperty(REFRACT_VAR, `url(#${id})`)
  else el.style.removeProperty(REFRACT_VAR)
}

function untrack(el: HTMLElement) {
  tracked.delete(el)
  resizeObserver?.unobserve(el)
  el.style.removeProperty(REFRACT_VAR)
}

function scan() {
  scanTimer = null
  const next = new Set<HTMLElement>()
  document.querySelectorAll<HTMLElement>(LIQUID_GLASS_SELECTOR).forEach(el => {
    if (!el.closest(EXCLUDED_CONTAINERS) && !el.closest('#oboard-theme-old-layer')) next.add(el)
  })
  tracked.forEach(el => {
    if (!next.has(el)) untrack(el)
  })
  next.forEach(el => {
    if (tracked.has(el)) return
    tracked.add(el)
    resizeObserver?.observe(el, { box: 'border-box' })
  })
}

function scheduleScan() {
  if (scanTimer !== null) return
  scanTimer = setTimeout(scan, SCAN_DELAY_MS)
}

export function startLiquidGlass() {
  if (resizeObserver || !supportsBackdropRefraction() || !document.body) return
  resizeObserver = new ResizeObserver(entries => {
    for (const entry of entries) {
      const el = entry.target as HTMLElement
      const box = entry.borderBoxSize?.[0]
      applyTo(el, box ? box.inlineSize : el.offsetWidth, box ? box.blockSize : el.offsetHeight)
    }
  })
  mutationObserver = new MutationObserver(scheduleScan)
  mutationObserver.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['class'] })
  document.documentElement.classList.add('lg-refraction')
  scan()
}

export function stopLiquidGlass() {
  if (scanTimer !== null) clearTimeout(scanTimer)
  scanTimer = null
  mutationObserver?.disconnect()
  mutationObserver = null
  tracked.forEach(el => el.style.removeProperty(REFRACT_VAR))
  tracked.clear()
  resizeObserver?.disconnect()
  resizeObserver = null
  document.documentElement.classList.remove('lg-refraction')
}

export function syncLiquidGlass(glass: boolean) {
  if (glass) startLiquidGlass()
  else stopLiquidGlass()
}
