// @vitest-environment node
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(path.resolve(__dirname, 'main.tsx'), 'utf8')
const stylesheet = readFileSync(path.resolve(__dirname, 'style.css'), 'utf8')

describe('Server list identity status row', () => {
  it('keeps list-mode status badges in a single nowrap cluster next to the name', () => {
    expect(source).toMatch(/className="server-list-name-row"/)
    expect(source).toMatch(/className="server-list-status"/)
    expect(source).toMatch(/<div className="server-list-status">[\s\S]*<ServerExpiryBadge server=\{server\} \/>[\s\S]*<\/div>/)
  })

  it('prevents the name row from wrapping and lets the name ellipsize first', () => {
    expect(stylesheet).toMatch(/\.server-list-name-row\s*\{[^}]*flex-wrap:\s*nowrap[^}]*min-width:\s*0/s)
    expect(stylesheet).not.toMatch(/\.server-list-name-row\s*\{[^}]*flex-wrap:\s*wrap/s)
    expect(stylesheet).toMatch(/\.server-list-status\s*\{[^}]*flex-wrap:\s*nowrap/s)
    expect(stylesheet).toMatch(/\.server-list-status\s*\{[^}]*flex:\s*0 0 auto/s)
    expect(stylesheet).toMatch(/\.server-list-name\s*\{[^}]*overflow:\s*hidden[^}]*text-overflow:\s*ellipsis[^}]*white-space:\s*nowrap/s)
  })
})
