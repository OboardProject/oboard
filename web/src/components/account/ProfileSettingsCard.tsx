import React, { useState, useEffect } from 'react'
import { User } from 'lucide-react'
import { FormField } from '../ui/form-field'

export interface ProfileSettingsCardProps {
  username?: string
  initialNickname?: string
  onSave: (nickname: string) => Promise<void>
}

export function ProfileSettingsCard({
  username = '',
  initialNickname = '',
  onSave,
}: ProfileSettingsCardProps) {
  const [nickname, setNickname] = useState(initialNickname)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    setNickname(initialNickname)
  }, [initialNickname])

  const isDirty = nickname !== initialNickname

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!isDirty || saving) return
    setSaving(true)
    try {
      await onSave(nickname)
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="sub-section account-card">
      <div className="sub-section-head">
        <div>
          <h3>
            <User size={16} />个人信息
          </h3>
          <p className="muted">管理账户基本信息。</p>
        </div>
      </div>
      <form className="form account-form" onSubmit={handleSubmit}>
        <FormField label="登录用户名">
          <input value={username} readOnly className="readonly-input" />
        </FormField>
        <FormField label="昵称">
          <input
            value={nickname}
            onChange={(e) => setNickname(e.target.value)}
            maxLength={40}
            placeholder="设置一个昵称"
          />
        </FormField>
        <div className="account-form-actions">
          <button type="submit" disabled={!isDirty || saving}>
            {saving ? '保存中…' : '保存'}
          </button>
        </div>
      </form>
    </section>
  )
}
