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
    expect(stylesheet).toMatch(/\.dialog-layer\s*\{[^}]*z-index:\s*calc\(var\(--z-dialog\) \+ var\(--dialog-layer-index, 0\)\)/s)
    expect(stylesheet).toMatch(/\.dialog-layer\[data-modal-top="false"\] \.dialog-backdrop\s*\{[^}]*background:\s*transparent/s)
    expect(stylesheet).not.toMatch(/--z-dialog-(nested|system)/)
    expect(stylesheet).not.toMatch(/\.dialog-backdrop-(nested|system)/)
    expect(stylesheet).toMatch(/\.custom-select-menu\s*\{[^}]*z-index:\s*var\(--z-popover\)/s)
    expect(stylesheet).toMatch(/\.portal-loader\s*\{[^}]*z-index:\s*var\(--z-loading\)/s)
  })

  it('optimizes mobile dialog touch-action and prevents mobile auto-zoom', () => {
    expect(stylesheet).toMatch(/\.dialog-layer\s*\{[^}]*touch-action:\s*pan-y/s)
    expect(stylesheet).toMatch(/\.dialog-backdrop\s*\{[^}]*touch-action:\s*manipulation/s)
    expect(stylesheet).toMatch(/\.dialog\s*\{[^}]*touch-action:\s*pan-y/s)
    expect(stylesheet).toMatch(/\.dialog-body\s*\{[^}]*touch-action:\s*pan-y/s)
    expect(stylesheet).toMatch(/\.dialog-body\s*\{[^}]*overscroll-behavior:\s*contain/s)
    expect(stylesheet).toMatch(/@media\s*\((?:max-width:\s*1024px\),\s*\(pointer:\s*coarse|\(pointer:\s*coarse\),\s*\(max-width:\s*1024px)\)[^}]*font-size:\s*16px\s*!important/s)
  })

  it('prevents text selection and touch callouts on proxy topology graph', () => {
    expect(stylesheet).toMatch(/\.proxy-flow[^}]*user-select:\s*none\s*!important/s)
    expect(stylesheet).toMatch(/\.proxy-flow[^}]*-webkit-touch-callout:\s*none\s*!important/s)
  })
})
