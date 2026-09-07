// @vitest-environment node
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(path.resolve(__dirname, 'main.tsx'), 'utf8')
const stylesheet = readFileSync(path.resolve(__dirname, 'style.css'), 'utf8')

describe('User table subscription column', () => {
  const tableSource = source.slice(
    source.indexOf('function UserManagement('),
    source.indexOf('function UserScopeNav('),
  )

  it('shows subscription status instead of copy-link buttons in the user list', () => {
    expect(source).toMatch(/function userSubscriptionStatus\(/)
    expect(source).toMatch(/label: '复制订阅', action: 'copy-sub'/)
    expect(tableSource).toMatch(/className="user-subscription-status"/)
    expect(tableSource).not.toMatch(/CopySubscriptionButton/)
    expect(tableSource).not.toMatch(/快速一次性/)
  })

  it('keeps table action chips compact and independent of the 42px control height', () => {
    expect(source).toMatch(/className="user-table-compact-button"/)
    expect(stylesheet).toMatch(/button\.user-table-compact-button\s*\{[^}]*min-height:\s*28px[^}]*height:\s*28px/s)
    expect(stylesheet).not.toMatch(/\.user-subscription-actions\s*\{/)
  })
})
