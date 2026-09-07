// @vitest-environment node
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(path.resolve(__dirname, 'main.tsx'), 'utf8')
const stylesheet = readFileSync(path.resolve(__dirname, 'style.css'), 'utf8')

describe('DNS credential domain list', () => {
  it('binds domains as a numbered list without a per-zone server picker', () => {
    expect(source).toMatch(/className="dns-zone-editor-list"/)
    expect(source).toMatch(/添加域名/)
    expect(source).not.toMatch(/dns-zone-server-field/)
    expect(source).not.toMatch(/不指定服务器/)
    expect(stylesheet).toMatch(/\.dns-zone-editor-list\s*\{/)
    expect(stylesheet).toMatch(/\.dns-zone-editor-simple[^{]*\.dns-zone-editor-row\s*\{[^}]*grid-template-columns:\s*38px minmax\(0,\s*1fr\) 38px/s)
  })
})
