import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const css = readFileSync(new URL('../../style.css', import.meta.url), 'utf8')
const html = readFileSync(new URL('../../../index.html', import.meta.url), 'utf8')
const light = css.slice(css.indexOf(':root {'), css.indexOf(':root[data-theme="dark"]'))
const dark = css.slice(css.indexOf(':root[data-theme="dark"]'), css.indexOf('* { box-sizing'))

function token(source: string, name: string): string {
  const value = new RegExp(`--${name}:\\s*([^;]+);`).exec(source)?.[1]
  if (!value) throw new Error(`Missing token ${name}`)
  return value.trim()
}

function rgb(value: string, backdrop = [255, 255, 255]): number[] {
  if (value.startsWith('#')) return value.slice(1).match(/../g)!.map(part => parseInt(part, 16))
  const parts = value.match(/[\d.]+/g)!.map(Number)
  return parts.slice(0, 3).map((channel, i) => channel * parts[3] + backdrop[i] * (1 - parts[3]))
}

function contrast(a: number[], b: number[]): number {
  const luminance = (color: number[]) => color.map(channel => {
    const c = channel / 255
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  }).reduce((sum, value, i) => sum + value * [0.2126, 0.7152, 0.0722][i], 0)
  const x = luminance(a), y = luminance(b)
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05)
}

describe('Calm Infrastructure shared design contract', () => {
  for (const [name, palette] of [['light', light], ['dark', dark]]) {
    it(`${name} body, captions and semantic statuses meet normal-text contrast`, () => {
      for (const surface of ['bg-page', 'bg-card', 'bg-control']) {
        const background = rgb(token(palette, surface))
        for (const text of ['text-primary', 'text-secondary', 'text-muted']) {
          expect(contrast(rgb(token(palette, text)), background), `${name}/${text}/${surface}`).toBeGreaterThanOrEqual(4.5)
        }
      }
      for (const state of ['success', 'warning', 'danger']) {
        const background = rgb(token(palette, `color-${state}-bg`), rgb(token(palette, 'bg-card')))
        expect(contrast(rgb(token(palette, `color-${state}`)), background), `${name}/${state}`).toBeGreaterThanOrEqual(4.5)
      }
      const background = rgb(token(palette, 'bg-card'))
      expect(contrast(rgb(token(palette, 'focus-ring')), background)).toBeGreaterThanOrEqual(3)
      expect(contrast(rgb(token(palette, 'control-border')), background)).toBeGreaterThanOrEqual(3)
    })
  }

  it('keeps destructive action contrast independent of status text', () => {
    expect(contrast(rgb(token(light, 'danger-fill')), rgb(token(light, 'danger-contrast')))).toBeGreaterThanOrEqual(4.5)
  })

  it('allows browser zoom and applies reduced motion to pseudo-elements', () => {
    expect(html).not.toMatch(/user-scalable\s*=\s*no|maximum-scale\s*=/)
    expect(css).toMatch(/prefers-reduced-motion: reduce\)\s*\{\s*\*, \*::before, \*::after/)
    expect(/(?:^|\n)button:active\s*\{\s*transform: scale/.test(css)).toBe(false)
  })
})
