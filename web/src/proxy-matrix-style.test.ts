import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const stylesheet = readFileSync(new URL('./style.css', import.meta.url), 'utf8')

function ruleBody(selector: string) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const match = stylesheet.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`))
  expect(match, `missing CSS rule for ${selector}`).not.toBeNull()
  return match?.[1] || ''
}

describe('proxy path matrix styles', () => {
  it('keeps header button text readable on hover', () => {
    const hoverRule = ruleBody('.proxy-matrix-header-target:hover')

    expect(hoverRule).toMatch(/background:\s*transparent/)
    expect(hoverRule).toMatch(/color:\s*inherit/)
    expect(hoverRule).toMatch(/transform:\s*none/)
  })
})
