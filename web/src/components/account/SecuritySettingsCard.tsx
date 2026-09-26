import React, { useState, useEffect, useRef } from 'react'
import { ShieldCheck, Smartphone, Fingerprint, Lock, Plus } from 'lucide-react'
import { SettingRow } from './SettingRow'
import { FormField } from '../ui/form-field'

export interface PasskeyCredential {
  id: string
  name: string
  created_at: string
  last_used_at?: string
}

export interface SecuritySettingsCardProps {
  totpEnabled: boolean
  recoveryCodesRemaining: number
  passkeys: PasskeyCredential[]
  passkeySupported: boolean
  passkeyAvailable: boolean
  securityWorking: string
  expandedPanel: 'password' | 'passkeys' | null
  setExpandedPanel: (panel: 'password' | 'passkeys' | null) => void
  onBeginTOTPSetup: () => Promise<void>
  onDisableTOTP: () => Promise<void>
  onRegenerateRecoveryCodes: () => Promise<void>
  onAddPasskey: () => Promise<void>
  onRemovePasskey: (passkey: PasskeyCredential) => Promise<void>
  onChangePassword: (currentPass: string, newPass: string) => Promise<boolean>
  formatDate: (dateStr: string) => string
}

export function SecuritySettingsCard({
  totpEnabled,
  recoveryCodesRemaining,
  passkeys,
  passkeySupported,
  passkeyAvailable,
  securityWorking,
  expandedPanel,
  setExpandedPanel,
  onBeginTOTPSetup,
  onDisableTOTP,
  onRegenerateRecoveryCodes,
  onAddPasskey,
  onRemovePasskey,
  onChangePassword,
  formatDate,
}: SecuritySettingsCardProps) {
  // Password local states
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [passwordWorking, setPasswordWorking] = useState(false)
  const currentPasswordInputRef = useRef<HTMLInputElement>(null)

  // Focus current password input when password panel opens
  useEffect(() => {
    if (expandedPanel === 'password') {
      const timer = setTimeout(() => {
        currentPasswordInputRef.current?.focus()
      }, 50)
      return () => clearTimeout(timer)
    }
  }, [expandedPanel])

  const resetPasswordState = () => {
    setCurrentPassword('')
    setNewPassword('')
    setConfirmPassword('')
  }

  const handlePasswordCancel = () => {
    resetPasswordState()
    setExpandedPanel(null)
  }

  const isPasswordMismatch = Boolean(
    newPassword && confirmPassword && newPassword !== confirmPassword
  )
  const isPasswordTooShort = Boolean(newPassword && newPassword.length < 8)

  const isPasswordSubmitDisabled =
    !currentPassword ||
    !newPassword ||
    !confirmPassword ||
    isPasswordMismatch ||
    isPasswordTooShort ||
    passwordWorking

  const handlePasswordSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (isPasswordSubmitDisabled) return

    setPasswordWorking(true)
    try {
      const success = await onChangePassword(currentPassword, newPassword)
      if (success) {
        resetPasswordState()
        setExpandedPanel(null)
      }
    } finally {
      setPasswordWorking(false)
    }
  }

  // Passkey details subtext
  const latestPasskey = passkeys.length > 0 ? passkeys[0] : null
  const passkeySubtext = latestPasskey
    ? latestPasskey.last_used_at
      ? `最近使用：${formatDate(latestPasskey.last_used_at)}`
      : `添加于：${formatDate(latestPasskey.created_at)}`
    : ''

  return (
    <section className="sub-section account-card account-login-security">
      <div className="sub-section-head">
        <div>
          <h3>
            <ShieldCheck size={16} />登录与安全
          </h3>
          <p className="muted">管理密码、两步验证和通行密钥。</p>
        </div>
      </div>

      <div className="account-security-list">
        {/* Row 1: 2FA / TOTP */}
        <SettingRow
          id="setting-row-totp"
          icon={<Smartphone size={18} />}
          title="两步验证"
          description={
            totpEnabled
              ? `已开启 · 剩余 ${recoveryCodesRemaining} 枚恢复码`
              : '增强账户登录安全性'
          }
          status={
            <span className={`status-badge ${totpEnabled ? 'ok' : 'neutral'}`}>
              {totpEnabled ? '已开启' : '未开启'}
            </span>
          }
          action={
            totpEnabled ? (
              <div className="account-row-actions">
                <button
                  type="button"
                  className="ghost"
                  onClick={() => void onRegenerateRecoveryCodes()}
                  disabled={Boolean(securityWorking)}
                >
                  生成新恢复码
                </button>
                <button
                  type="button"
                  className="ghost danger-text"
                  onClick={() => void onDisableTOTP()}
                  disabled={Boolean(securityWorking)}
                >
                  停用
                </button>
              </div>
            ) : (
              <button
                type="button"
                className="secondary-btn"
                onClick={() => void onBeginTOTPSetup()}
                disabled={Boolean(securityWorking)}
              >
                {securityWorking === 'totp-setup' ? '准备中…' : '开启'}
              </button>
            )
          }
        />

        {/* Row 2: Passkey */}
        <SettingRow
          id="setting-row-passkeys"
          icon={<Fingerprint size={18} />}
          title="通行密钥"
          description={
            passkeySubtext
              ? `使用设备生物识别或系统验证登录 · ${passkeySubtext}`
              : '使用设备生物识别或系统验证登录'
          }
          status={
            <span className={`status-badge ${passkeys.length > 0 ? 'ok' : 'neutral'}`}>
              {passkeys.length > 0 ? `${passkeys.length} 个` : '无通行密钥'}
            </span>
          }
          action={
            <button
              type="button"
              className="secondary-btn"
              onClick={() =>
                setExpandedPanel(expandedPanel === 'passkeys' ? null : 'passkeys')
              }
              aria-expanded={expandedPanel === 'passkeys'}
            >
              {expandedPanel === 'passkeys' ? '收起' : '管理'}
            </button>
          }
          expanded={expandedPanel === 'passkeys'}
        >
          <div className="account-passkey-panel">
            {!passkeySupported || !passkeyAvailable ? (
              <p className="account-security-note">通行密钥需要通过 HTTPS 访问面板。</p>
            ) : null}

            {passkeys.length > 0 ? (
              <div className="account-passkey-list">
                {passkeys.map((passkey) => (
                  <div key={passkey.id} className="account-passkey-item">
                    <div className="account-passkey-info">
                      <strong>{passkey.name}</strong>
                      <small>
                        {passkey.last_used_at
                          ? `最近使用 ${formatDate(passkey.last_used_at)}`
                          : `添加于 ${formatDate(passkey.created_at)}`}
                      </small>
                    </div>
                    <button
                      type="button"
                      className="ghost danger-text"
                      onClick={() => void onRemovePasskey(passkey)}
                      disabled={Boolean(securityWorking)}
                    >
                      移除
                    </button>
                  </div>
                ))}
              </div>
            ) : (
              <p className="muted text-sm" style={{ marginBottom: 12 }}>
                尚未添加通行密钥。您可以添加 Face ID、Touch ID 或系统验证进行安全登录。
              </p>
            )}

            <button
              type="button"
              className="secondary-btn add-passkey-btn"
              onClick={() => void onAddPasskey()}
              disabled={
                Boolean(securityWorking) || !passkeySupported || !passkeyAvailable
              }
            >
              <Plus size={15} />
              <span>添加通行密钥</span>
            </button>
          </div>
        </SettingRow>

        {/* Row 3: Password */}
        <SettingRow
          id="setting-row-password"
          icon={<Lock size={18} />}
          title="密码"
          description="建议定期更新账户密码"
          action={
            <button
              type="button"
              className="secondary-btn"
              onClick={() => {
                if (expandedPanel === 'password') {
                  resetPasswordState()
                  setExpandedPanel(null)
                } else {
                  setExpandedPanel('password')
                }
              }}
              aria-expanded={expandedPanel === 'password'}
            >
              {expandedPanel === 'password' ? '收起' : '修改密码'}
            </button>
          }
          expanded={expandedPanel === 'password'}
        >
          <form className="account-password-form" onSubmit={handlePasswordSubmit}>
            <FormField label="当前密码">
              <input
                ref={currentPasswordInputRef}
                type="password"
                autoComplete="current-password"
                value={currentPassword}
                onChange={(e) => setCurrentPassword(e.target.value)}
                placeholder="请输入当前密码"
              />
            </FormField>

            <FormField
              label="新密码"
              hint={isPasswordTooShort ? '新密码至少需要 8 个字符' : '至少 8 个字符'}
            >
              <input
                type="password"
                autoComplete="new-password"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                placeholder="请输入新密码"
              />
            </FormField>

            <FormField
              label="确认新密码"
              hint={isPasswordMismatch ? '两次输入的密码不一致' : ''}
            >
              <input
                type="password"
                autoComplete="new-password"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                placeholder="请再次输入新密码"
                className={isPasswordMismatch ? 'input-error' : ''}
              />
            </FormField>

            {isPasswordMismatch && (
              <div className="field-error-text" style={{ color: 'var(--danger, #ef4444)', fontSize: 12, marginTop: 4 }}>
                两次输入的密码不一致
              </div>
            )}

            <div className="account-form-actions">
              <button
                type="button"
                className="ghost"
                onClick={handlePasswordCancel}
                disabled={passwordWorking}
              >
                取消
              </button>
              <button type="submit" disabled={isPasswordSubmitDisabled}>
                {passwordWorking ? '修改中…' : '修改密码'}
              </button>
            </div>
          </form>
        </SettingRow>
      </div>
    </section>
  )
}
