import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const stylesheet = readFileSync(new URL('./style.css', import.meta.url), 'utf8')

function layerValue(name: string) {
  const match = stylesheet.match(new RegExp(`--z-${name}:\\s*(\\d+)`))
  expect(match, `missing --z-${name}`).not.toBeNull()
  return Number(match?.[1])
}

describe('global overlay layers', () => {
  it('keeps blocking surfaces ordered and toast above every overlay', () => {
    const layers = [
      'navigation',
      'dialog',
      'dialog-nested',
      'dialog-system',
      'popover',
      'loading',
      'theme-transition',
      'toast',
    ].map(layerValue)

    expect(layers).toEqual([...layers].sort((a, b) => a - b))
    expect(new Set(layers).size).toBe(layers.length)
    expect(layerValue('toast')).toBe(2147483647)
  })

  it('binds shared portal surfaces to semantic layers', () => {
    expect(stylesheet).toMatch(/\.top-toast-viewport\s*\{[^}]*z-index:\s*var\(--z-toast\)/s)
    expect(stylesheet).toMatch(/\.dialog-backdrop\s*\{[^}]*z-index:\s*var\(--z-dialog\)/s)
    expect(stylesheet).toMatch(/\.dialog-backdrop-nested\s*\{[^}]*z-index:\s*var\(--z-dialog-nested\)/s)
    expect(stylesheet).toMatch(/\.dialog-backdrop-system\s*\{[^}]*z-index:\s*var\(--z-dialog-system\)/s)
    expect(stylesheet).toMatch(/\.dialog-root\s*\{[^}]*z-index:\s*var\(--z-dialog-system\)/s)
    expect(stylesheet).toMatch(/\.custom-select-menu\s*\{[^}]*z-index:\s*var\(--z-popover\)/s)
    expect(stylesheet).toMatch(/\.portal-loader\s*\{[^}]*z-index:\s*var\(--z-loading\)/s)
  })
})
