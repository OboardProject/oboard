import React from 'react'
import { Smartphone, Fingerprint, Shield } from 'lucide-react'
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

export function AccountSummaryCard({
  user,
  auth,
  ageRequired,
  ageReady,
  onNavigateToSection,
}: AccountSummaryCardProps) {
  const avatarLetter = user?.nickname
    ? user.nickname.slice(0, 1)
    : user?.username
    ? user.username.slice(0, 1).toUpperCase()
    : 'U'

  return (
    <div className="account-profile-hero">
      <div className="account-profile-avatar">{avatarLetter}</div>
      <div className="account-profile-info">
        <div className="account-profile-name-row">
          <h2 className="account-profile-title">{user?.nickname || user?.username || '用户'}</h2>
          {user?.username && <span className="account-profile-handle">@{user.username}</span>}
          <Badge variant={user?.role === 'admin' ? 'default' : 'secondary'}>
            {user?.role === 'admin' ? '管理员' : '普通用户'}
          </Badge>
        </div>
        <p className="muted" style={{ margin: 0, fontSize: 13 }}>
          维护个人信息、登录安全、双重认证与订阅加密。
        </p>
      </div>
      <div className="account-profile-badges">
        <button
          type="button"
          className={`sub-pill status-chip ${auth.totp_enabled ? 'ok' : 'neutral'}`}
          onClick={() => onNavigateToSection('totp')}
          title="点击定位到两步验证"
        >
          <Smartphone size={13} />
          <span>{auth.totp_enabled ? '2FA 已开启' : '2FA 未开启'}</span>
        </button>

        <button
          type="button"
          className={`sub-pill status-chip ${auth.passkeys_count > 0 ? 'ok' : 'neutral'}`}
          onClick={() => onNavigateToSection('passkeys')}
          title="点击定位到通行密钥"
        >
          <Fingerprint size={13} />
          <span>{`通行密钥 ${auth.passkeys_count} 个`}</span>
        </button>

        <button
          type="button"
          className={`sub-pill status-chip ${ageRequired ? 'warn' : ageReady ? 'ok' : 'neutral'}`}
          onClick={() => onNavigateToSection('age')}
          title="点击定位到订阅加密"
        >
          <Shield size={13} />
          <span>{ageRequired ? 'Age 强制加密' : ageReady ? 'Age 已开启' : 'Age 未开启'}</span>
        </button>
      </div>
    </div>
  )
}
