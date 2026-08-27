export function hasManagementAccess(role?: string) {
  return role === 'admin' || role === 'operator'
}

export function canManageAdministratorAccounts(role?: string) {
  return role === 'admin'
}

type UserRoleSubject = { id: number; role?: string }
type UserRoleGroup = { id: number; role?: string; enabled?: boolean }
type UserRoleMembership = { user_id: number; group_id: number; enabled?: boolean }

export function effectiveUserRole(
  user: UserRoleSubject,
  groups: UserRoleGroup[] = [],
  memberships: UserRoleMembership[] = [],
) {
  const rank: Record<string, number> = { none: -1, viewer: 0, operator: 1, admin: 2 }
  let role = user.role || 'none'
  for (const membership of memberships) {
    if (membership.user_id !== user.id || membership.enabled === false) continue
    const group = groups.find(item => item.id === membership.group_id && item.enabled !== false)
    if (group?.role && (rank[group.role] ?? -1) > (rank[role] ?? -1)) role = group.role
  }
  return role
}
