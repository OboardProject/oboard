import { ArrowDown } from 'lucide-react'
import { Badge } from '../ui/badge'

export interface UserSummary {
  id?: number
  username?: string
  nickname?: string
  role?: string
}

export interface AuthenticationStatusSummary {
  totp_enabled: boolean
  passkeys_count: number
}

export interface AccountSummaryCardProps {
  user?: UserSummary
  auth: AuthenticationStatusSummary
  ageRequired: boolean
  ageReady: boolean
  onNavigateToSection: (section: 'totp' | 'passkeys' | 'age') => void
}

export function AccountSummaryCard({ user, auth, ageRequired, ageReady, onNavigateToSection }: AccountSummaryCardProps) {
  return <div className="signal-account-identity">
    <div className="signal-account-name">
      <strong>{user?.nickname || user?.username || '用户'}</strong>
      {user?.username && <span>@{user.username}</span>}
      <Badge variant="secondary">{user?.role === 'admin' ? '管理员' : '普通用户'}</Badge>
    </div>
    <nav className="signal-account-shortcuts" aria-label="账户设置快捷入口">
      <button type="button" className="ghost" onClick={() => onNavigateToSection('totp')}>{auth.totp_enabled ? '2FA 已开启' : '2FA 未开启'}<ArrowDown size={14} aria-hidden="true" /></button>
      <button type="button" className="ghost" onClick={() => onNavigateToSection('passkeys')}>通行密钥 {auth.passkeys_count} 个<ArrowDown size={14} aria-hidden="true" /></button>
      <button type="button" className="ghost" onClick={() => onNavigateToSection('age')}>{ageRequired ? 'Age 强制加密' : ageReady ? 'Age 已开启' : 'Age 未开启'}<ArrowDown size={14} aria-hidden="true" /></button>
    </nav>
  </div>
}
