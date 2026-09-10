// @vitest-environment node
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(path.resolve(__dirname, 'main.tsx'), 'utf8')
const stylesheet = readFileSync(path.resolve(__dirname, 'style.css'), 'utf8')

describe('DNS zone bar toolbar layout', () => {
  it('does not render redundant 域名当前记录 header in records tab', () => {
    expect(source).not.toMatch(/<h3>域名当前记录<\/h3>/)
  })

  it('renders actions directly inside dns-zone-bar alongside the selector', () => {
    expect(source).toMatch(/className="dns-zone-bar"/)
    expect(source).toMatch(/className="dns-zone-main"/)
    expect(source).toMatch(/className="dns-zone-actions settings-card-actions"/)
    expect(source).toMatch(/<button type="button" onClick=\{openCreateRecord\}[^>]*><Plus size=\{14\} \/>添加记录<\/button>/)
  })

  it('defines flex layout and padding for dns-zone-bar and its actions', () => {
    expect(stylesheet).toMatch(/\.dns-management-card\s*>\s*\.dns-zone-bar\s*\{[^}]*padding-top:\s*18px/s)
    expect(stylesheet).toMatch(/\.dns-zone-main\s*\{[^}]*display:\s*flex/s)
    expect(stylesheet).toMatch(/\.dns-zone-actions\s*\{[^}]*display:\s*inline-flex/s)
  })
})
