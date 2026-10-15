import { useCallback, useEffect, useId, useState } from 'react'
import { AnimatePresence } from 'motion/react'
import { KeyRound, X } from 'lucide-react'
import { SettingsGroup, SettingsRow } from './SettingsLayout'
import { MotionDialogPanel } from '../ui/motion'
import { FormField } from '../ui/form-field'
import { ipDataStatusText } from '../ip-data/labels'

type Request = (path: string, init?: RequestInit) => Promise<any>
type Provider = {
  name: string; label: string; roles: string[]; key_required: boolean; key_label: string; keyless?: string; signup_url: string
  configured: boolean; used_today: number; daily_budget: number; cooldown_until?: string; last_status?: string; last_error?: string; last_checked_at?: string
}
type TestResult = { provider: string; status: string; error?: string; asn?: string; as_name?: string; country_code?: string; country?: string }
type Props = { client: { request: Request }; notify: (message: string, tone?: string) => void }

const roleText: Record<string, string> = { attribution: '归属', quality: '质量', route: '路由' }
const groups = [
  { role: 'attribution', title: 'IP 归属信息', description: '以 IPinfo 为准，其余来源用于交叉校验；IPinfo 不可用时由其余来源投票，在线来源全部失败时使用离线库。' },
  { role: 'quality', title: 'IP 质量信息', description: '代理、VPN、Tor、机房和滥用由多个来源分别判断后投票；任一来源不可用时自动跳过。' },
  { role: 'route', title: '路由数据', description: '路由监控以 Agent 的 NextTrace 结果为主；缺失的跳由主控 NextTrace 补全，仍缺失时使用 IP 归属来源的 ASN。' },
]

function providerState(p: Provider): { tone: string; text: string } {
  if (p.cooldown_until && new Date(p.cooldown_until) > new Date()) return { tone: 'warning', text: `暂停至 ${new Date(p.cooldown_until).toLocaleTimeString()}` }
  if (p.key_required && !p.configured) return { tone: '', text: '未配置' }
  if (p.last_status && p.last_status !== 'ok') return { tone: 'danger', text: ipDataStatusText[p.last_status] || p.last_status }
  return { tone: p.configured ? 'ok' : '', text: p.configured ? '已配置 Key' : '免 Key' }
}

export function IPDataSettings({ client, notify }: Props) {
  const [providers, setProviders] = useState<Provider[]>([])
  const [offline, setOffline] = useState<{ available?: boolean; error?: string }>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState<Provider | null>(null)
  const apply = (value: any) => { setProviders(value?.providers || []); setOffline(value?.offline || {}) }
  const load = useCallback(async (signal?: AbortSignal) => {
    try { apply(await client.request('/ip-data/keys', { signal })); setError('') }
    catch (e: any) { if (!signal?.aborted) setError(e?.message || '读取数据源失败') }
    finally { if (!signal?.aborted) setLoading(false) }
  }, [client])
  useEffect(() => { const abort = new AbortController(); void load(abort.signal); return () => abort.abort() }, [load])

  return <>
    {groups.map(group => <SettingsGroup key={group.role} title={group.title} description={group.description}>
      {loading ? <p role="status" className="muted">正在读取数据源…</p> : providers.filter(p => p.roles[0] === group.role).map(p => {
        const state = providerState(p)
        return <SettingsRow key={p.name} label={p.label} description={[p.roles.map(role => roleText[role]).join(' · '), p.key_required ? '必须填写 Key' : p.keyless].filter(Boolean).join('。')}>
          <div className="ip-data-provider-control">
            <span className={`status-pill ${state.tone}`.trim()}>{state.text}</span>
            {p.daily_budget > 0 && <small className="muted">今日 {p.used_today}/{p.daily_budget}</small>}
            <button type="button" className="ghost" onClick={() => setEditing(p)}><KeyRound size={14} aria-hidden="true" />{p.configured ? '管理 Key' : '填写 Key'}</button>
          </div>
        </SettingsRow>
      })}
      {group.role === 'attribution' && !loading && <SettingsRow label="离线库（ip2region）" description="随主控发布的签名离线库，只有国家与地区，没有 ASN；作为归属信息的最后降级来源。">
        <span className={`status-pill ${offline.available ? 'ok' : 'warning'}`}>{offline.available ? '可用' : offline.error || '未加载'}</span>
      </SettingsRow>}
    </SettingsGroup>)}
    {error && <p role="alert" className="settings-feedback">{error}</p>}
    <AnimatePresence>{editing && <KeyDialog provider={editing} client={client} notify={notify} onSaved={apply} onClose={() => setEditing(null)} />}</AnimatePresence>
  </>
}

function KeyDialog({ provider, client, notify, onSaved, onClose }: { provider: Provider; client: Props['client']; notify: Props['notify']; onSaved: (value: any) => void; onClose: () => void }) {
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState<'' | 'save' | 'clear' | 'test'>('')
  const [error, setError] = useState('')
  const [configured, setConfigured] = useState(provider.configured)
  const [test, setTest] = useState<TestResult | null>(null)
  const id = useId()
  const save = async (clear: boolean) => {
    if (busy || (!clear && !value.trim())) return
    setBusy(clear ? 'clear' : 'save'); setError(''); setTest(null)
    try {
      const body = clear ? { keys: {}, clear: [provider.name] } : { keys: { [provider.name]: value.trim() }, clear: [] }
      const result = await client.request('/ip-data/keys', { method: 'POST', body: JSON.stringify(body) })
      onSaved(result); setValue(''); setConfigured(!clear)
      notify(clear ? `已清除 ${provider.label} Key` : `${provider.label} Key 已保存`, 'success')
    } catch (e: any) { setError(e?.message || '保存失败，请重试') }
    finally { setBusy('') }
  }
  const runTest = async () => {
    if (busy) return
    setBusy('test'); setError('')
    try {
      const result = await client.request('/ip-data/keys/test', { method: 'POST', body: JSON.stringify({ provider: provider.name }) })
      setTest(result?.result || null)
      onSaved(await client.request('/ip-data/keys'))
    } catch (e: any) { setError(e?.message || '测试失败') }
    finally { setBusy('') }
  }
  return <MotionDialogPanel onCancel={() => { if (!busy) onClose() }} className="ip-data-key-dialog" aria-labelledby={`${id}-title`}>
    <header className="dialog-head"><h2 id={`${id}-title`}>{provider.label} {provider.key_label}</h2><button type="button" className="ghost icon-button dialog-close" disabled={Boolean(busy)} onClick={onClose} aria-label="关闭"><X size={16} aria-hidden="true" /></button></header>
    <form className="dialog-body form" onSubmit={event => { event.preventDefault(); void save(false) }} aria-busy={Boolean(busy)}>
      <p className="muted">{provider.key_required ? '此来源必须填写 Key 才会查询。' : provider.keyless}。Key 加密保存在主控，保存后不会再显示。 <a href={provider.signup_url} target="_blank" rel="noreferrer noopener">获取 Key</a></p>
      <FormField label={provider.key_label} hint={configured ? '已配置；留空不修改，填写新值将替换。' : '粘贴从数据源后台获取的 Key。'}>
        <input type="password" autoComplete="off" spellCheck={false} value={value} onChange={event => { setValue(event.target.value); setError('') }} placeholder={configured ? '已配置（不显示）' : ''} disabled={Boolean(busy)} aria-invalid={Boolean(error)} aria-describedby={error ? `${id}-error` : undefined} />
      </FormField>
      {error && <p id={`${id}-error`} role="alert" className="settings-feedback">{error}</p>}
      {test && <p role="status" className="settings-feedback">{test.status === 'ok'
        ? `测试成功：${[test.asn, test.as_name, test.country_code || test.country].filter(Boolean).join(' · ') || '已返回数据'}`
        : `测试未通过：${ipDataStatusText[test.status] || test.status}${test.error ? `，${test.error}` : ''}`}</p>}
      <footer className="dialog-actions">
        {configured && <button type="button" className="ghost danger-ghost" disabled={Boolean(busy)} onClick={() => void save(true)}>{busy === 'clear' ? '清除中…' : '清除 Key'}</button>}
        <button type="button" className="ghost" disabled={Boolean(busy)} onClick={() => void runTest()}>{busy === 'test' ? '测试中…' : '测试连接'}</button>
        <button type="submit" disabled={Boolean(busy) || !value.trim()}>{busy === 'save' ? '保存中…' : '保存'}</button>
      </footer>
    </form>
  </MotionDialogPanel>
}
