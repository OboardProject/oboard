import { useState, useEffect, type FormEvent } from 'react'
import { AccountFormDialog } from './AccountFormDialog'

export interface ProfileSettingsCardProps {
  username?: string
  initialNickname?: string
  onSave: (nickname: string) => Promise<void>
}

export function ProfileSettingsCard({ username = '', initialNickname = '', onSave }: ProfileSettingsCardProps) {
  const [nickname, setNickname] = useState(initialNickname)
  const [editing, setEditing] = useState(false)
  const [saving, setSaving] = useState(false)
  const [failed, setFailed] = useState(false)

  useEffect(() => { setNickname(initialNickname) }, [initialNickname])
  const isDirty = nickname !== initialNickname
  const close = () => { setEditing(false); setNickname(initialNickname); setFailed(false) }
  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (!isDirty || saving) return
    setSaving(true)
    setFailed(false)
    try {
      await onSave(nickname)
      setEditing(false)
    } catch {
      setFailed(true)
    } finally {
      setSaving(false)
    }
  }

  return <section className="signal-account-section" aria-labelledby="account-profile-title">
    <header><h2 id="account-profile-title">个人资料</h2></header>
    <div className="signal-account-section-body">
      <div className="signal-account-profile-row">
        <dl><div><dt>登录用户名</dt><dd>{username || '暂不可用'}</dd></div><div><dt>昵称</dt><dd>{initialNickname || '未设置'}</dd></div></dl>
        <button type="button" className="secondary-btn" onClick={() => setEditing(true)}>编辑资料</button>
      </div>
    </div>
    {editing && <AccountFormDialog title="编辑个人资料" dirty={isDirty} busy={saving} onClose={close} footer={<button type="submit" form="account-profile-form" disabled={!isDirty || saving}>{saving ? '保存中…' : '保存'}</button>}>
      <form id="account-profile-form" className="signal-account-form" onSubmit={handleSubmit}>
        <label>登录用户名<input value={username} readOnly autoComplete="username" /></label>
        <label>昵称<input value={nickname} onChange={e => setNickname(e.target.value)} maxLength={40} placeholder="设置一个昵称" disabled={saving} /></label>
        {failed && <p role="alert" className="signal-account-error">保存失败，修改已保留，请重试。</p>}
      </form>
    </AccountFormDialog>}
  </section>
}
