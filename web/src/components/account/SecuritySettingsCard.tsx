import { useState, type FormEvent } from 'react'
import { Smartphone, Fingerprint, Lock, Plus } from 'lucide-react'
import { SettingRow } from './SettingRow'
import { AccountFormDialog } from './AccountFormDialog'

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

export function SecuritySettingsCard({ totpEnabled, recoveryCodesRemaining, passkeys, passkeySupported, passkeyAvailable, securityWorking, expandedPanel, setExpandedPanel, onBeginTOTPSetup, onDisableTOTP, onRegenerateRecoveryCodes, onAddPasskey, onRemovePasskey, onChangePassword, formatDate }: SecuritySettingsCardProps) {
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [passwordWorking, setPasswordWorking] = useState(false)
  const [passwordFailed, setPasswordFailed] = useState(false)
  const busy = Boolean(securityWorking) || passwordWorking
  const closePassword = () => {
    setCurrentPassword('')
    setNewPassword('')
    setConfirmPassword('')
    setPasswordFailed(false)
    setExpandedPanel(null)
  }
  const mismatch = Boolean(newPassword && confirmPassword && newPassword !== confirmPassword)
  const tooShort = Boolean(newPassword && newPassword.length < 8)
  const submitDisabled = !currentPassword || !newPassword || !confirmPassword || mismatch || tooShort || busy
  const handlePasswordSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (submitDisabled) return
    setPasswordWorking(true)
    setPasswordFailed(false)
    try {
      if (await onChangePassword(currentPassword, newPassword)) closePassword()
      else setPasswordFailed(true)
    } finally {
      setPasswordWorking(false)
    }
  }

  return <section className="signal-account-section" aria-labelledby="account-security-title">
    <header><h2 id="account-security-title">登录与安全</h2></header>
    <div className="signal-account-section-body">
      <SettingRow id="setting-row-totp" icon={<Smartphone size={18} />} title="两步验证"
        description={totpEnabled ? `剩余 ${recoveryCodesRemaining} 枚恢复码` : '登录时使用认证器验证码'}
        status={<span className="signal-account-status">{totpEnabled ? '已开启' : '未开启'}</span>}
        action={totpEnabled ? <>
          <button type="button" className="secondary-btn" onClick={() => void onRegenerateRecoveryCodes()} disabled={busy}>{securityWorking === 'totp-recovery' ? '生成中…' : '生成新恢复码'}</button>
          <button type="button" className="ghost danger-text" onClick={() => void onDisableTOTP()} disabled={busy}>{securityWorking === 'totp-disable' ? '停用中…' : '停用'}</button>
        </> : <button type="button" className="secondary-btn" onClick={() => void onBeginTOTPSetup()} disabled={busy}>{securityWorking === 'totp-setup' ? '准备中…' : '开启'}</button>}
      />
      <SettingRow id="setting-row-passkeys" icon={<Fingerprint size={18} />} title="通行密钥" description="使用设备生物识别或系统验证登录"
        status={<span className="signal-account-status">{passkeys.length > 0 ? `${passkeys.length} 个` : '未添加'}</span>}
        action={<button type="button" className="secondary-btn" onClick={() => setExpandedPanel(expandedPanel === 'passkeys' ? null : 'passkeys')} disabled={busy} aria-expanded={expandedPanel === 'passkeys'} aria-controls={expandedPanel === 'passkeys' ? 'setting-row-passkeys-content' : undefined}>{expandedPanel === 'passkeys' ? '收起' : '管理'}</button>}
        expanded={expandedPanel === 'passkeys'}>
        {!passkeySupported || !passkeyAvailable ? <p className="signal-account-note">当前浏览器无法添加通行密钥，请使用支持通行密钥的浏览器并通过 HTTPS 访问。</p> : null}
        {passkeys.length > 0 ? <ul className="signal-account-credential-list">
          {passkeys.map(passkey => <li key={passkey.id}>
            <div><strong>{passkey.name}</strong><small>{passkey.last_used_at ? `最近使用 ${formatDate(passkey.last_used_at)}` : `添加于 ${formatDate(passkey.created_at)}`}</small></div>
            <button type="button" className="ghost danger-text" aria-label={`移除通行密钥 ${passkey.name}`} onClick={() => void onRemovePasskey(passkey)} disabled={busy}>{securityWorking === `passkey-${passkey.id}` ? '移除中…' : '移除'}</button>
          </li>)}
        </ul> : <p className="signal-account-note">尚未添加通行密钥。</p>}
        <button type="button" className="secondary-btn" onClick={() => void onAddPasskey()} disabled={busy || !passkeySupported || !passkeyAvailable}><Plus size={15} aria-hidden="true" />{securityWorking === 'passkey-add' ? '添加中…' : '添加通行密钥'}</button>
      </SettingRow>
      <SettingRow id="setting-row-password" icon={<Lock size={18} />} title="登录密码" description="修改时需要验证当前密码"
        action={<button type="button" className="secondary-btn" onClick={() => setExpandedPanel('password')} disabled={busy} aria-haspopup="dialog">修改密码</button>} />
    </div>
    {expandedPanel === 'password' && <AccountFormDialog title="修改密码" dirty={Boolean(currentPassword || newPassword || confirmPassword)} busy={passwordWorking} onClose={closePassword} footer={<button type="submit" form="account-password-form" disabled={submitDisabled}>{passwordWorking ? '修改中…' : '修改密码'}</button>}>
      <form id="account-password-form" className="signal-account-form" onSubmit={handlePasswordSubmit}>
        <label>当前密码<input type="password" autoComplete="current-password" value={currentPassword} onChange={e => setCurrentPassword(e.target.value)} disabled={passwordWorking} required /></label>
        <label>新密码<input type="password" autoComplete="new-password" value={newPassword} onChange={e => setNewPassword(e.target.value)} disabled={passwordWorking} minLength={8} required aria-invalid={tooShort} aria-describedby="account-password-length" /></label>
        <p id="account-password-length" className={tooShort ? 'signal-account-error' : 'signal-account-note'}>新密码至少需要 8 个字符</p>
        <label>确认新密码<input type="password" autoComplete="new-password" value={confirmPassword} onChange={e => setConfirmPassword(e.target.value)} disabled={passwordWorking} required aria-invalid={mismatch} aria-describedby={mismatch ? 'account-password-mismatch' : undefined} /></label>
        {mismatch && <p id="account-password-mismatch" role="alert" className="signal-account-error">两次输入的密码不一致</p>}
        {passwordFailed && <p role="alert" className="signal-account-error">密码未修改，请检查输入后重试。</p>}
      </form>
    </AccountFormDialog>}
  </section>
}
