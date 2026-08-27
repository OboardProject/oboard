import { describe, expect, it } from 'vitest'

import { canManageAdministratorAccounts, effectiveUserRole, hasManagementAccess } from './permissions'

describe('operator permissions', () => {
  it('grants operators the complete management surface without administrator-account authority', () => {
    expect(hasManagementAccess('operator')).toBe(true)
    expect(canManageAdministratorAccounts('operator')).toBe(false)
    expect(canManageAdministratorAccounts('admin')).toBe(true)
  })

  it('recognizes administrators inherited through an enabled group', () => {
    expect(effectiveUserRole(
      { id: 7, role: 'viewer' },
      [{ id: 3, role: 'admin', enabled: true }],
      [{ user_id: 7, group_id: 3, enabled: true }],
    )).toBe('admin')
  })
})
