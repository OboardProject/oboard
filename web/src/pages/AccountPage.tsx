import React, { useState, useEffect } from 'react'
import { AnimatePresence } from 'motion/react'
import { AccountSummaryCard } from '../components/account/AccountSummaryCard'
import { ProfileSettingsCard } from '../components/account/ProfileSettingsCard'
import { SecuritySettingsCard, PasskeyCredential } from '../components/account/SecuritySettingsCard'
import { AdvancedSettingsCard, SSHAccess } from '../components/account/AdvancedSettingsCard'

export interface User {
  id: number
  username: string
  nickname?: string
  role?: string
  proxy_password?: string
  totp_enabled?: boolean
  subscription_age_enabled?: boolean
  subscription_age_public_key?: string
  subscription_age_policy?: 'optional' | 'required' | string
}

export interface AuthenticationStatus {
  totp_enabled: boolean
  recovery_codes_remaining: number
  passkeys: PasskeyCredential[]
  passkey_supported: boolean
}

export interface AccountPageProps {
  data: any
  client: any
  load: () => Promise<void>
  notify?: (message: string, tone?: 'success' | 'error' | 'warning' | 'info') => void
  useDialogs: any
  passkeyAvailable: () => boolean
  createPasskeyCredential: (options: any) => Promise<any>
  sshShareURI: (address: string, port: number, username: string, proxyPass: string) => string
  copyText: (text: string) => Promise<boolean>
  formatDate: (dateStr: string) => string
  localizeErrorMessage: (msg: any) => string
  Panel: React.ComponentType<any>
  TOTPSetupDialog: React.ComponentType<any>
  RecoveryCodesDialog: React.ComponentType<any>
}

export function AccountPage({
  data,
  client,
  load,
  notify,
  useDialogs,
  passkeyAvailable,
  createPasskeyCredential,
  sshShareURI,
  copyText,
  formatDate,
  localizeErrorMessage,
  Panel,
  TOTPSetupDialog,
  RecoveryCodesDialog,
}: AccountPageProps) {
  const dialogs = useDialogs()
  const user: User | undefined = data.account_user || data.current_user
  const sshAccesses: SSHAccess[] = data.ssh_accesses || []

  // Accordion / Disclosure state management
  const [expandedSecurityPanel, setExpandedSecurityPanel] = useState<'password' | 'passkeys' | null>(null)
  const [expandedAdvancedPanel, setExpandedAdvancedPanel] = useState<'age' | 'ssh' | null>(null)

  const [authentication, setAuthentication] = useState<AuthenticationStatus>({
    totp_enabled: Boolean(user?.totp_enabled),
    recovery_codes_remaining: 0,
    passkeys: data.passkeys || [],
    passkey_supported: passkeyAvailable(),
  })
  const [totpSetup, setTOTPSetup] = useState<{ secret: string; qr_data_url: string } | null>(null)
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null)
  const [securityWorking, setSecurityWorking] = useState('')

  const refreshAuthentication = async () => {
    try {
      const result = (await client.request('/me/authentication')) as AuthenticationStatus
      setAuthentication(result)
    } catch {
      // Ignore background auth status fetch error
    }
  }

  useEffect(() => {
    let active = true
    client
      .request('/me/authentication')
      .then((result: AuthenticationStatus) => {
        if (active) setAuthentication(result)
      })
      .catch(() => undefined)
    return () => {
      active = false
    }
  }, [user?.id, client])

  const agePolicy = user?.subscription_age_policy || 'optional'
  const ageRequired = agePolicy === 'required'
  const ageReady =
    Boolean(user?.subscription_age_public_key) &&
    (ageRequired || Boolean(user?.subscription_age_enabled))

  // Scroll to section when status chip is clicked
  const handleNavigateToSection = (section: 'totp' | 'passkeys' | 'age') => {
    let elementId = ''
    if (section === 'totp') {
      elementId = 'setting-row-totp'
    } else if (section === 'passkeys') {
      elementId = 'setting-row-passkeys'
      setExpandedSecurityPanel('passkeys')
    } else if (section === 'age') {
      elementId = 'setting-row-age'
      setExpandedAdvancedPanel('age')
    }

    if (elementId) {
      setTimeout(() => {
        const el = document.getElementById(elementId)
        if (el) {
          el.scrollIntoView({ behavior: 'smooth', block: 'center' })
          el.focus({ preventScroll: true })
          el.classList.add('row-highlight')
          setTimeout(() => el.classList.remove('row-highlight'), 1600)
        }
      }, 50)
    }
  }

  // Profile save
  const handleSaveProfile = async (nickname: string) => {
    try {
      await client.request('/me', { method: 'PATCH', body: JSON.stringify({ nickname }) })
      await load()
      notify?.('个人信息已保存', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
      throw error
    }
  }

  // Password change
  const handleChangePassword = async (currentPass: string, newPass: string) => {
    if (newPass.length < 8) {
      notify?.('新密码至少需要 8 个字符', 'warning')
      return false
    }
    try {
      await client.request('/auth/password', {
        method: 'POST',
        body: JSON.stringify({ current_password: currentPass, new_password: newPass }),
      })
      notify?.('密码已修改', 'success')
      return true
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
      return false
    }
  }

  // 2FA / TOTP actions
  const beginTOTPSetup = async () => {
    const currentPassword = await dialogs.prompt({
      title: '开启双重认证',
      message: '先验证当前登录密码，再绑定认证器。',
      placeholder: '当前密码',
      inputType: 'password',
      confirmText: '继续',
    })
    if (currentPassword === null) return
    setSecurityWorking('totp-setup')
    try {
      const result = (await client.request('/me/totp/setup/begin', {
        method: 'POST',
        body: JSON.stringify({ current_password: currentPassword }),
      })) as { secret: string; qr_data_url: string }
      setTOTPSetup(result)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setSecurityWorking('')
    }
  }

  const completeTOTPSetup = async (result: { recovery_codes: string[]; csrf_token?: string }) => {
    if (result.csrf_token) sessionStorage.setItem('oboard.csrf', result.csrf_token)
    setTOTPSetup(null)
    setRecoveryCodes(result.recovery_codes)
    await refreshAuthentication()
    await load()
    notify?.('双重认证已开启', 'success')
  }

  const disableTOTP = async () => {
    const confirmed = await dialogs.confirm({
      title: '停用双重认证？',
      message: '停用后，账号只需要密码或通行密钥即可登录。',
      confirmText: '继续停用',
      tone: 'danger',
    })
    if (!confirmed) return
    const currentPassword = await dialogs.prompt({
      title: '验证登录密码',
      placeholder: '当前密码',
      inputType: 'password',
      confirmText: '下一步',
    })
    if (currentPassword === null) return
    const code = await dialogs.prompt({
      title: '验证认证器',
      message: '输入六位验证码，也可以使用一枚恢复码。',
      placeholder: '验证码或恢复码',
      inputType: 'text',
      confirmText: '停用',
    })
    if (code === null) return
    setSecurityWorking('totp-disable')
    try {
      const result = (await client.request('/me/totp/disable', {
        method: 'POST',
        body: JSON.stringify({ current_password: currentPassword, code }),
      })) as { csrf_token?: string }
      if (result.csrf_token) sessionStorage.setItem('oboard.csrf', result.csrf_token)
      await refreshAuthentication()
      await load()
      notify?.('双重认证已停用', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setSecurityWorking('')
    }
  }

  const regenerateRecoveryCodes = async () => {
    const currentPassword = await dialogs.prompt({
      title: '生成新的恢复码',
      message: '生成后，之前的恢复码会立即失效。',
      placeholder: '当前密码',
      inputType: 'password',
      confirmText: '下一步',
    })
    if (currentPassword === null) return
    const code = await dialogs.prompt({
      title: '验证认证器',
      placeholder: '六位验证码或恢复码',
      inputType: 'text',
      confirmText: '生成',
    })
    if (code === null) return
    setSecurityWorking('totp-recovery')
    try {
      const result = (await client.request('/me/totp/recovery-codes', {
        method: 'POST',
        body: JSON.stringify({ current_password: currentPassword, code }),
      })) as { recovery_codes: string[] }
      setRecoveryCodes(result.recovery_codes)
      await refreshAuthentication()
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setSecurityWorking('')
    }
  }

  // Passkey actions
  const addPasskey = async () => {
    const name = await dialogs.prompt({
      title: '添加通行密钥',
      message: '名称用于区分这台设备或密码管理器。',
      defaultValue: '我的通行密钥',
      placeholder: '例如：MacBook',
      confirmText: '下一步',
    })
    if (name === null) return
    const currentPassword = await dialogs.prompt({
      title: '验证登录密码',
      placeholder: '当前密码',
      inputType: 'password',
      confirmText: '添加',
    })
    if (currentPassword === null) return
    const code = authentication.totp_enabled
      ? await dialogs.prompt({
          title: '验证认证器',
          message: '输入六位验证码，也可以使用一枚恢复码。',
          placeholder: '验证码或恢复码',
          confirmText: '添加',
        })
      : ''
    if (code === null) return
    setSecurityWorking('passkey-add')
    try {
      const begin = (await client.request('/me/passkeys/register/begin', {
        method: 'POST',
        body: JSON.stringify({ name, current_password: currentPassword, code }),
      })) as { options: any; challenge_token: string }
      const credential = await createPasskeyCredential(begin.options)
      await client.request('/me/passkeys/register/finish', {
        method: 'POST',
        body: JSON.stringify({ challenge_token: begin.challenge_token, credential }),
      })
      await refreshAuthentication()
      await load()
      notify?.('通行密钥已添加', 'success')
    } catch (error: any) {
      const message =
        String(error?.name || '') === 'NotAllowedError'
          ? '未完成通行密钥创建'
          : localizeErrorMessage(error?.message || error)
      notify?.(message, 'error')
    } finally {
      setSecurityWorking('')
    }
  }

  const removePasskey = async (passkey: PasskeyCredential) => {
    const confirmed = await dialogs.confirm({
      title: '移除通行密钥？',
      message: `将移除“${passkey.name}”，这台设备之后不能再用它登录。`,
      confirmText: '移除',
      tone: 'danger',
    })
    if (!confirmed) return
    const currentPassword = await dialogs.prompt({
      title: '验证登录密码',
      placeholder: '当前密码',
      inputType: 'password',
      confirmText: '移除',
    })
    if (currentPassword === null) return
    const code = authentication.totp_enabled
      ? await dialogs.prompt({
          title: '验证认证器',
          message: '输入六位验证码，也可以使用一枚恢复码。',
          placeholder: '验证码或恢复码',
          confirmText: '移除',
        })
      : ''
    if (code === null) return
    setSecurityWorking(`passkey-${passkey.id}`)
    try {
      await client.request(`/me/passkeys/${passkey.id}`, {
        method: 'DELETE',
        body: JSON.stringify({ current_password: currentPassword, code }),
      })
      await refreshAuthentication()
      await load()
      notify?.('通行密钥已移除', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setSecurityWorking('')
    }
  }

  // Age save
  const handleSaveAge = async (enabled: boolean, publicKey: string) => {
    if ((enabled || ageRequired) && !publicKey.trim()) {
      notify?.('请填写 Age 公钥；私钥只保留在客户端', 'warning')
      return
    }
    if ((enabled || ageRequired) && publicKey.trim() && !publicKey.trim().startsWith('age1')) {
      notify?.('Age 公钥必须以 age1 开头', 'warning')
      return
    }
    try {
      await client.request('/me/subscription-age', {
        method: 'PATCH',
        body: JSON.stringify({ enabled: ageRequired || enabled, public_key: publicKey.trim() }),
      })
      await load()
      notify?.('订阅加密设置已保存', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    }
  }

  // SSH copy
  const handleCopySSH = async (access: SSHAccess) => {
    const uri = sshShareURI(access.address, access.port, access.username, user?.proxy_password || '')
    const ok = await copyText(uri)
    notify?.(ok ? 'SSH 链接已复制' : '复制失败，请手动复制', ok ? 'success' : 'error')
    return ok
  }

  return (
    <>
      <Panel title="我的账户" className="account-panel">
        <div className="account-layout">
          {/* Top Hero: User Overview */}
          <AccountSummaryCard
            user={user}
            auth={{
              totp_enabled: authentication.totp_enabled,
              passkeys_count: authentication.passkeys.length,
            }}
            ageRequired={ageRequired}
            ageReady={ageReady}
            onNavigateToSection={handleNavigateToSection}
          />

          {/* First Layer Grid: Profile (5 cols) & Login Security (7 cols) */}
          <div className="account-settings-grid">
            <ProfileSettingsCard
              username={user?.username}
              initialNickname={user?.nickname}
              onSave={handleSaveProfile}
            />

            <SecuritySettingsCard
              totpEnabled={authentication.totp_enabled}
              recoveryCodesRemaining={authentication.recovery_codes_remaining}
              passkeys={authentication.passkeys}
              passkeySupported={authentication.passkey_supported}
              passkeyAvailable={passkeyAvailable()}
              securityWorking={securityWorking}
              expandedPanel={expandedSecurityPanel}
              setExpandedPanel={setExpandedSecurityPanel}
              onBeginTOTPSetup={beginTOTPSetup}
              onDisableTOTP={disableTOTP}
              onRegenerateRecoveryCodes={regenerateRecoveryCodes}
              onAddPasskey={addPasskey}
              onRemovePasskey={removePasskey}
              onChangePassword={handleChangePassword}
              formatDate={formatDate}
            />
          </div>

          {/* Second Layer: Advanced Settings */}
          <AdvancedSettingsCard
            initialAgeEnabled={Boolean(user?.subscription_age_enabled)}
            initialAgePublicKey={user?.subscription_age_public_key || ''}
            ageRequired={ageRequired}
            ageReady={ageReady}
            sshAccesses={sshAccesses}
            expandedPanel={expandedAdvancedPanel}
            setExpandedPanel={setExpandedAdvancedPanel}
            onSaveAge={handleSaveAge}
            onCopySSH={handleCopySSH}
          />
        </div>
      </Panel>

      <AnimatePresence>
        {totpSetup && (
          <TOTPSetupDialog
            setup={totpSetup}
            client={client}
            onCancel={() => setTOTPSetup(null)}
            onComplete={completeTOTPSetup}
          />
        )}
      </AnimatePresence>

      <AnimatePresence>
        {recoveryCodes && (
          <RecoveryCodesDialog
            codes={recoveryCodes}
            onClose={() => setRecoveryCodes(null)}
          />
        )}
      </AnimatePresence>
    </>
  )
}
