import React, { useState, useEffect } from 'react'
import { Shield, KeyRound, Copy, Check } from 'lucide-react'
import { SettingRow } from './SettingRow'
import { Switch } from '../ui/switch'
import { FormField } from '../ui/form-field'

export interface SSHAccess {
  inbound_id: number
  name: string
  address: string
  port: number
  username: string
}

export interface AdvancedSettingsCardProps {
  initialAgeEnabled: boolean
  initialAgePublicKey: string
  ageRequired: boolean
  ageReady: boolean
  sshAccesses: SSHAccess[]
  expandedPanel: 'age' | 'ssh' | null
  setExpandedPanel: (panel: 'age' | 'ssh' | null) => void
  onSaveAge: (enabled: boolean, publicKey: string) => Promise<void>
  onCopySSH: (access: SSHAccess) => Promise<boolean>
}

export function AdvancedSettingsCard({
  initialAgeEnabled,
  initialAgePublicKey,
  ageRequired,
  ageReady,
  sshAccesses,
  expandedPanel,
  setExpandedPanel,
  onSaveAge,
  onCopySSH,
}: AdvancedSettingsCardProps) {
  // Age local states
  const [ageEnabled, setAgeEnabled] = useState(initialAgeEnabled)
  const [agePublicKey, setAgePublicKey] = useState(initialAgePublicKey)
  const [ageSaving, setAgeSaving] = useState(false)

  // Copy local state map { [inbound_id]: boolean }
  const [copiedMap, setCopiedMap] = useState<Record<number, boolean>>({})

  useEffect(() => {
    setAgeEnabled(initialAgeEnabled)
    setAgePublicKey(initialAgePublicKey)
  }, [initialAgeEnabled, initialAgePublicKey])

  const handleAgeCancel = () => {
    setAgeEnabled(initialAgeEnabled)
    setAgePublicKey(initialAgePublicKey)
    setExpandedPanel(null)
  }

  const handleAgeSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setAgeSaving(true)
    try {
      await onSaveAge(ageRequired || ageEnabled, agePublicKey)
    } finally {
      setAgeSaving(false)
    }
  }

  const handleCopy = async (access: SSHAccess) => {
    const success = await onCopySSH(access)
    if (success) {
      setCopiedMap((prev) => ({ ...prev, [access.inbound_id]: true }))
      setTimeout(() => {
        setCopiedMap((prev) => ({ ...prev, [access.inbound_id]: false }))
      }, 1800)
    }
  }

  const effectiveAgeStatusText = ageRequired
    ? '强制加密'
    : ageReady
    ? '已开启'
    : '未开启'

  return (
    <section className="sub-section account-card account-advanced-section">
      <div className="sub-section-head">
        <div>
          <h3>高级设置</h3>
          <p className="muted">较少使用的账户和代理配置。</p>
        </div>
      </div>

      <div className="account-advanced-list">
        {/* Row 1: Age Subscription Encryption */}
        <SettingRow
          id="setting-row-age"
          icon={<Shield size={18} />}
          title="订阅加密"
          description="使用 Age 加密 Mihomo 订阅内容"
          status={
            <span
              className={`status-badge ${
                ageRequired ? 'warn' : ageReady ? 'ok' : 'neutral'
              }`}
            >
              {effectiveAgeStatusText}
            </span>
          }
          action={
            <button
              type="button"
              className="secondary-btn"
              onClick={() =>
                setExpandedPanel(expandedPanel === 'age' ? null : 'age')
              }
              aria-expanded={expandedPanel === 'age'}
            >
              {expandedPanel === 'age'
                ? '收起'
                : ageReady || ageRequired
                ? '管理'
                : '配置'}
            </button>
          }
          expanded={expandedPanel === 'age'}
        >
          <form className="account-age-form" onSubmit={handleAgeSubmit}>
            <div className="switch-form-row subscription-burn-toggle">
              <span className="switch-form-label">
                {ageRequired ? '必须使用 Age 加密' : '为 Mihomo 开启 Age 加密'}
              </span>
              <Switch
                checked={ageRequired || ageEnabled}
                disabled={ageRequired}
                onChange={(checked) => setAgeEnabled(checked)}
                ariaLabel="为 Mihomo 开启 Age 加密"
              />
            </div>

            {(ageRequired || ageEnabled) && (
              <FormField
                label="Age 公钥"
                hint="只填写客户端生成的公钥，私钥不要上传到面板。"
              >
                <textarea
                  className="age-public-key-textarea"
                  value={agePublicKey}
                  onChange={(e) => setAgePublicKey(e.target.value)}
                  rows={3}
                  spellCheck={false}
                  placeholder="age1..."
                />
              </FormField>
            )}

            <div className="account-form-actions">
              <button
                type="button"
                className="ghost"
                onClick={handleAgeCancel}
                disabled={ageSaving}
              >
                取消
              </button>
              <button type="submit" disabled={ageSaving}>
                {ageSaving ? '保存中…' : '保存'}
              </button>
            </div>
          </form>
        </SettingRow>

        {/* Row 2: SSH Proxy */}
        {sshAccesses.length > 0 && (
          <SettingRow
            id="setting-row-ssh"
            icon={<KeyRound size={18} />}
            title="SSH 代理"
            description="使用代理用户名和密码连接已授权的 SSH 入口"
            status={
              <span className="status-badge ok">{`${sshAccesses.length} 个入口`}</span>
            }
            action={
              <button
                type="button"
                className="secondary-btn"
                onClick={() =>
                  setExpandedPanel(expandedPanel === 'ssh' ? null : 'ssh')
                }
                aria-expanded={expandedPanel === 'ssh'}
              >
                {expandedPanel === 'ssh' ? '收起' : '管理'}
              </button>
            }
            expanded={expandedPanel === 'ssh'}
          >
            <div className="account-ssh-panel">
              <div className="account-ssh-list">
                {sshAccesses.map((access) => {
                  const isCopied = Boolean(copiedMap[access.inbound_id])
                  return (
                    <div key={access.inbound_id} className="account-ssh-item">
                      <div className="account-ssh-info">
                        <strong>{access.name}</strong>
                        <small className="muted">{`${access.username}@${access.address}:${access.port}`}</small>
                      </div>
                      <button
                        type="button"
                        className="secondary-btn copy-btn"
                        onClick={() => void handleCopy(access)}
                      >
                        {isCopied ? <Check size={14} /> : <Copy size={14} />}
                        <span>{isCopied ? '已复制' : '复制'}</span>
                      </button>
                    </div>
                  )
                })}
              </div>
            </div>
          </SettingRow>
        )}
      </div>
    </section>
  )
}
