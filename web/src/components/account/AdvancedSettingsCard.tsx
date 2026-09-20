import { useState, useEffect, useRef, type FormEvent } from 'react'
import { Shield, KeyRound, Copy, Check } from 'lucide-react'
import { SettingRow } from './SettingRow'
import { Switch } from '../ui/switch'
import { AccountFormDialog } from './AccountFormDialog'

export interface SSHAccess {
  node_id: string
  device_id?: string
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
  onSaveAge: (enabled: boolean, publicKey: string) => Promise<boolean>
  onCopySSH: (access: SSHAccess) => Promise<boolean>
}

export function AdvancedSettingsCard({ initialAgeEnabled, initialAgePublicKey, ageRequired, ageReady, sshAccesses, expandedPanel, setExpandedPanel, onSaveAge, onCopySSH }: AdvancedSettingsCardProps) {
  const [ageEnabled, setAgeEnabled] = useState(initialAgeEnabled)
  const [agePublicKey, setAgePublicKey] = useState(initialAgePublicKey)
  const [ageSaving, setAgeSaving] = useState(false)
  const [ageFailed, setAgeFailed] = useState(false)
  const [copiedMap, setCopiedMap] = useState<Record<string, boolean>>({})
  const [copying, setCopying] = useState('')
  const copyInFlight = useRef(false)
  const copyTimers = useRef<ReturnType<typeof setTimeout>[]>([])
  useEffect(() => () => { copyTimers.current.forEach(clearTimeout) }, [])
  useEffect(() => {
    setAgeEnabled(initialAgeEnabled)
    setAgePublicKey(initialAgePublicKey)
  }, [initialAgeEnabled, initialAgePublicKey])

  const closeAge = () => {
    setAgeEnabled(initialAgeEnabled)
    setAgePublicKey(initialAgePublicKey)
    setAgeFailed(false)
    setExpandedPanel(null)
  }
  const ageDirty = ageEnabled !== initialAgeEnabled || agePublicKey !== initialAgePublicKey
  const ageInvalid = (ageRequired || ageEnabled) && !agePublicKey.trim().startsWith('age1')
  const handleAgeSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (ageSaving || ageInvalid) return
    setAgeSaving(true)
    setAgeFailed(false)
    try {
      if (await onSaveAge(ageRequired || ageEnabled, agePublicKey)) setExpandedPanel(null)
      else setAgeFailed(true)
    } finally {
      setAgeSaving(false)
    }
  }
  const accessKey = (access: SSHAccess) => `${access.node_id}:${access.device_id || ''}:${access.username}`
  const handleCopy = async (access: SSHAccess) => {
    if (copyInFlight.current) return
    copyInFlight.current = true
    const key = accessKey(access)
    setCopying(key)
    setCopiedMap(prev => ({ ...prev, [key]: false }))
    try {
      if (await onCopySSH(access)) {
        setCopiedMap(prev => ({ ...prev, [key]: true }))
        copyTimers.current.push(setTimeout(() => setCopiedMap(prev => ({ ...prev, [key]: false })), 1800))
      }
    } finally {
      setCopying('')
      copyInFlight.current = false
    }
  }

  return <section className="signal-account-section" aria-labelledby="account-advanced-title">
    <header><h2 id="account-advanced-title">高级订阅设置</h2></header>
    <div className="signal-account-section-body">
      <SettingRow id="setting-row-age" icon={<Shield size={18} />} title="订阅加密" description="使用 Age 加密 Mihomo 订阅内容"
        status={<span className="signal-account-status">{ageRequired ? '强制加密' : ageReady ? '已开启' : '未开启'}</span>}
        action={<button type="button" className="secondary-btn" onClick={() => setExpandedPanel('age')} aria-haspopup="dialog">{ageReady || ageRequired ? '管理' : '配置'}</button>} />
      {sshAccesses.length > 0 && <SettingRow id="setting-row-ssh" icon={<KeyRound size={18} />} title="SSH 代理" description="使用代理用户名和密码连接已授权的 SSH 入口"
        status={<span className="signal-account-status">{sshAccesses.length} 个入口</span>}
        action={<button type="button" className="secondary-btn" onClick={() => setExpandedPanel(expandedPanel === 'ssh' ? null : 'ssh')} aria-expanded={expandedPanel === 'ssh'} aria-controls={expandedPanel === 'ssh' ? 'setting-row-ssh-content' : undefined}>{expandedPanel === 'ssh' ? '收起' : '查看入口'}</button>}
        expanded={expandedPanel === 'ssh'}>
        <ul className="signal-account-credential-list">
          {sshAccesses.map(access => {
            const key = accessKey(access)
            const isCopied = Boolean(copiedMap[key])
            return <li key={key}>
              <div><strong>{access.name}</strong><small>{`${access.username}@${access.address}:${access.port}`}</small></div>
              <button type="button" className="secondary-btn copy-btn" aria-label={`复制 SSH 链接 ${access.name}`} disabled={Boolean(copying)} onClick={() => void handleCopy(access)}>
                {isCopied ? <Check size={14} aria-hidden="true" /> : <Copy size={14} aria-hidden="true" />}<span aria-live="polite">{copying === key ? '复制中…' : isCopied ? '已复制' : '复制'}</span>
              </button>
            </li>
          })}
        </ul>
      </SettingRow>}
    </div>
    {expandedPanel === 'age' && <AccountFormDialog title="订阅加密" dirty={ageDirty} busy={ageSaving} onClose={closeAge} footer={<button type="submit" form="account-age-form" disabled={ageSaving || ageInvalid}>{ageSaving ? '保存中…' : '保存'}</button>}>
      <form id="account-age-form" className="signal-account-form" onSubmit={handleAgeSubmit}>
        <div className="signal-account-switch-row"><span>{ageRequired ? '必须使用 Age 加密' : '为 Mihomo 开启 Age 加密'}</span><Switch checked={ageRequired || ageEnabled} disabled={ageRequired || ageSaving} onChange={setAgeEnabled} ariaLabel="为 Mihomo 开启 Age 加密" /></div>
        {(ageRequired || ageEnabled) && <>
          <label>Age 公钥<textarea className="age-public-key-textarea" value={agePublicKey} onChange={e => setAgePublicKey(e.target.value)} rows={3} spellCheck={false} placeholder="age1..." disabled={ageSaving} aria-describedby="account-age-hint" aria-invalid={ageInvalid} /></label>
          <p id="account-age-hint" className="signal-account-note">公钥以 age1 开头。只填写客户端生成的公钥，私钥不要上传到面板。</p>
        </>}
        {ageRequired && <p className="signal-account-note">管理员已要求加密，无法在此关闭。</p>}
        {ageFailed && <p role="alert" className="signal-account-error">设置未保存，修改已保留，请重试。</p>}
      </form>
    </AccountFormDialog>}
  </section>
}
