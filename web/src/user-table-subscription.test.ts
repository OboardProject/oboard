// @vitest-environment node
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(path.resolve(__dirname, 'main.tsx'), 'utf8')
const listStylesheet = readFileSync(path.resolve(__dirname, 'components/users/user-list.css'), 'utf8')

describe('User table subscription column', () => {
  const tableSource = source.slice(
    source.indexOf('function UserManagement('),
    source.indexOf('function UserScopeNav('),
  )

  it('shows subscription status instead of copy-link buttons in the user list', () => {
    expect(source).toMatch(/function userSubscriptionStatus\(/)
    expect(source).toMatch(/label: '复制订阅链接', action: 'copy-sub'/)
    expect(tableSource).toMatch(/className="user-subscription-status"/)
    expect(tableSource).not.toMatch(/CopySubscriptionButton/)
    expect(tableSource).not.toMatch(/快速一次性/)
  })

  it('keeps one visible row action and moves plan and ledger into the more menu', () => {
    expect(tableSource).not.toMatch(/user-table-compact-button/)
    expect(tableSource).not.toMatch(/管理 \$\{usr\.username\} 的套餐/)
    expect(source).toMatch(/\{ label: '管理套餐', action: 'plan' \}/)
    expect(source).toMatch(/\{ label: '流量明细', action: 'ledger' \}/)
  })

  it('degrades to cards on narrow containers with 44px touch targets', () => {
    expect(listStylesheet).toMatch(/@container user-list \(max-width: 820px\)/)
    expect(listStylesheet).toMatch(/button\.user-row-more-button \{[^}]*width: 44px[^}]*height: 44px/)
  })
})
