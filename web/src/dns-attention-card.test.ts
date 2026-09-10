// @vitest-environment node
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(path.resolve(__dirname, 'main.tsx'), 'utf8')
const stylesheet = readFileSync(path.resolve(__dirname, 'style.css'), 'utf8')

describe('DNS attention card button and hover styling', () => {
  it('renders DNSAttentionCard with button.dns-attention-main trigger', () => {
    expect(source).toMatch(/function DNSAttentionCard\(/)
    expect(source).toMatch(/<button type="button" className="dns-attention-main" onClick=\{onOpen\}/)
  })

  it('resets button styles for .dns-attention-main to prevent global button:hover dark fill', () => {
    expect(stylesheet).toMatch(/button\.dns-attention-main\s*\{[^}]*background:\s*transparent/s)
    expect(stylesheet).toMatch(/button\.dns-attention-main:hover,\s*button\.dns-attention-main:active,\s*button\.dns-attention-main:focus\s*\{[^}]*background:\s*transparent\s*!important/s)
    expect(stylesheet).toMatch(/button\.dns-attention-main\s*\{[^}]*transform:\s*none\s*!important/s)
  })

  it('provides smooth border-color hover transition on the attention card', () => {
    expect(stylesheet).toMatch(/\.dns-attention-card\s*\{[^}]*transition:[^}]*border-color/s)
    expect(stylesheet).toMatch(/\.dns-attention-card:hover\s*\{[^}]*border-color:/s)
    expect(stylesheet).toMatch(/\.dns-attention-card:has\(\.status-pill\.danger\):hover\s*\{[^}]*border-color:/s)
  })
})
