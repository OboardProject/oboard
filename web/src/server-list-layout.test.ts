// @vitest-environment node
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(path.resolve(__dirname, 'main.tsx'), 'utf8')
const stylesheet = readFileSync(path.resolve(__dirname, 'style.css'), 'utf8')

describe('Server management toolbar', () => {
  it('renders server filter settings in a floating popover instead of an in-flow wrapping drawer', () => {
    expect(source).toMatch(/function ServerFilterDropdown\(/)
    expect(source).toMatch(/className="server-filter-popover"/)
    expect(source).not.toMatch(/className="server-list-filter-drawer"/)
    expect(stylesheet).toMatch(/\.server-filter-popover\s*\{[^}]*position:\s*fixed/s)
    expect(stylesheet).toMatch(/\.server-filter-popover\s*\{[^}]*z-index:\s*var\(--z-popover\)/s)
    expect(stylesheet).not.toMatch(/\.server-list-filter-drawer\s*\{/)
  })

  it('keeps server list search input compact and prevents excessive stretching on desktop', () => {
    expect(source).toContain('className={`server-list-search${serverSearchOpen')
    expect(source).toContain('className={`ghost icon-button server-search-toggle')
    expect(stylesheet).toMatch(/\.server-list-search\s*\{[^}]*width:\s*260px/s)
    expect(stylesheet).toMatch(/\.server-list-search\s*\{[^}]*flex:\s*0\s+1\s+260px/s)
    expect(stylesheet).toMatch(/\.server-list-search\.is-open\s*\{[^}]*display:\s*flex/s)
    expect(stylesheet).not.toMatch(/\.server-list-search\s*\{[^}]*flex:\s*1\s+1\s+240px/s)
  })
})
