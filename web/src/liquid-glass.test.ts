// @vitest-environment jsdom
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { buildDisplacementMap, stopLiquidGlass, supportsBackdropRefraction, syncLiquidGlass } from './liquid-glass'
import { accentContrastColor } from './theme'

const css = readFileSync(path.resolve(__dirname, 'styles/liquid-glass.css'), 'utf8')

function offsetAt(map: Uint8ClampedArray, width: number, x: number, y: number) {
  const i = (y * width + x) * 4
  return { dx: map[i] - 128, dy: map[i + 1] - 128 }
}

describe('liquid glass displacement map', () => {
  const shape = { width: 120, height: 40, radius: 20 }
  const map = buildDisplacementMap(shape)

  it('leaves the flat centre of the glass unrefracted', () => {
    expect(offsetAt(map, 120, 60, 20)).toEqual({ dx: 0, dy: 0 })
  })

  it('pulls the backdrop inward at the rim, strongest at the edge', () => {
    const top = offsetAt(map, 120, 60, 0)
    const bottom = offsetAt(map, 120, 60, 39)
    const nearTop = offsetAt(map, 120, 60, 6)
    expect(top.dy).toBeGreaterThan(50)
    expect(bottom.dy).toBeLessThan(-50)
    expect(Math.abs(top.dx)).toBeLessThanOrEqual(1)
    expect(nearTop.dy).toBeGreaterThan(0)
    expect(nearTop.dy).toBeLessThan(top.dy)
  })

  it('follows the rounded end cap normals', () => {
    const leftCap = offsetAt(map, 120, 0, 20)
    const rightCap = offsetAt(map, 120, 119, 20)
    expect(leftCap.dx).toBeGreaterThan(50)
    expect(rightCap.dx).toBeLessThan(-50)
  })
})

describe('liquid glass runtime', () => {
  afterEach(() => stopLiquidGlass())

  it('stays off outside Chromium and never adds the refraction class', () => {
    expect(supportsBackdropRefraction()).toBe(false)
    syncLiquidGlass(true)
    expect(document.documentElement.classList.contains('lg-refraction')).toBe(false)
    expect(document.getElementById('oboard-liquid-glass-filters')).toBeNull()
  })

  it('references the per-element refraction where the backdrop filter is declared', () => {
    // A var() inside a custom property resolves on :root, where the
    // per-element --lg-refract is not set.
    expect(css).not.toMatch(/--lg-[a-z-]+:[^;]*var\(--lg-refract/)
    expect(css).toContain('backdrop-filter: var(--lg-backdrop-pre) var(--lg-refract,) var(--lg-backdrop-post) !important;')
    // Chromium aliases -webkit-backdrop-filter, so it must come first.
    expect(css).not.toMatch(/backdrop-filter: var\(--lg-backdrop-pre\)[^\n]*\n\s*-webkit-backdrop-filter/)
  })
})

describe('accent contrast', () => {
  it('keeps white text on the default accent and switches light accents to dark text', () => {
    expect(accentContrastColor('#007aff')).toBe('#ffffff')
    expect(accentContrastColor('#8b5cf6')).toBe('#ffffff')
    expect(accentContrastColor('#eab308')).toBe('#111827')
    expect(accentContrastColor('#06b6d4')).toBe('#111827')
  })
})
