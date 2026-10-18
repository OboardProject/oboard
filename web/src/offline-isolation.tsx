import { useState } from 'react'
import { Clock3, Loader2, RotateCcw, ServerOff } from 'lucide-react'
import { Dialog } from './components/ui/dialog'
import './offline-isolation.css'

type Inbound = { id: number; name: string }
type Incident = { id: number; version: number; server_id: number; server_name: string; inbounds: Inbound[] }
type ActiveIsolation = { id: number; incident_id: number; server_id: number; server_name: string; inbound_name: string; recovery_policy: string; restore_at?: string }
type IsolationData = { incidents?: Incident[]; active?: ActiveIsolation[] }
type Client = { requestV2: (path: string, init?: RequestInit) => Promise<any> }

const hourChoices = [1, 6, 12, 24]

function shortTime(at: Date) {
  const pad = (n: number) => String(n).padStart(2, '0')
  const sameYear = at.getFullYear() === new Date().getFullYear()
  return `${sameYear ? '' : `${at.getFullYear()}-`}${pad(at.getMonth() + 1)}-${pad(at.getDate())} ${pad(at.getHours())}:${pad(at.getMinutes())}`
}

function remainingText(at: Date, now: number) {
  const minutes = Math.ceil((at.getTime() - now) / 60000)
  if (minutes <= 0) return '即将恢复'
  if (minutes < 60) return `剩余 ${minutes} 分钟`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `剩余 ${hours} 小时${minutes % 60 ? ` ${minutes % 60} 分钟` : ''}`
  return `剩余 ${Math.floor(hours / 24)} 天`
}

export function isolationRecovery(item: ActiveIsolation, now = Date.now()): { label: string; detail: string } {
  if (item.recovery_policy === 'auto') return { label: '服务器恢复在线后', detail: '自动恢复' }
  if (item.restore_at) {
    const at = new Date(item.restore_at)
    if (!Number.isNaN(at.getTime())) return { label: `${shortTime(at)} 恢复`, detail: remainingText(at, now) }
  }
  return { label: '需要手动恢复', detail: '不会自动恢复' }
}

function serverName(item: { server_name?: string; server_id: number }) {
  return item.server_name || `服务器 #${item.server_id}`
}

export function OfflineIsolationPrompt({ isolation, client, onManage, onChanged }: { isolation?: IsolationData; client: Client; onManage: () => void; onChanged: () => void }) {
  const incidents = isolation?.incidents || []
  const [open, setOpen] = useState(false)
  if (!incidents.length) return null
  const names = incidents.map(serverName)
  const inboundCount = incidents.reduce((total, item) => total + item.inbounds.length, 0)
  return (
    <section className="offline-isolation-prompt" role="status">
      <span className="offline-isolation-icon" aria-hidden="true"><ServerOff size={16} /></span>
      <div className="offline-isolation-copy">
        <strong>{incidents.length === 1 ? `${names[0]} 已离线` : `${incidents.length} 台服务器已离线`}</strong>
        <p>{incidents.length > 1 && <span className="offline-isolation-names">{names.join('、')}。</span>}{inboundCount > 0 ? `${inboundCount} 个入口仍在订阅里，可暂时剔除。` : '可暂时把它的入口从订阅里剔除。'}</p>
      </div>
      <div className="offline-isolation-actions">
        <button type="button" className="ghost" onClick={onManage}>查看记录</button>
        <button type="button" onClick={() => setOpen(true)}>临时剔除</button>
      </div>
      {open && <OfflineIsolationDialog incidents={incidents} client={client} onClose={() => setOpen(false)} onChanged={onChanged} />}
    </section>
  )
}

export function OfflineIsolationList({ isolation, client, onChanged }: { isolation?: IsolationData; client: Client; onChanged: () => void }) {
  const incidents = isolation?.incidents || []
  const active = isolation?.active || []
  const [open, setOpen] = useState(false)
  const [busyID, setBusyID] = useState(0)
  const [error, setError] = useState('')
  if (!incidents.length && !active.length) return null
  const restore = async (item: ActiveIsolation) => {
    setBusyID(item.id)
    setError('')
    try {
      await client.requestV2(`/node-incidents/${item.incident_id}/restore`, { method: 'POST', body: JSON.stringify({ isolation_id: item.id }) })
      onChanged()
    } catch (err) {
      setError(err instanceof Error ? err.message : '恢复失败')
    } finally {
      setBusyID(0)
    }
  }
  const now = Date.now()
  const sorted = [...active].sort((a, b) => serverName(a).localeCompare(serverName(b)) || a.inbound_name.localeCompare(b.inbound_name))
  if (!active.length) {
    const inboundCount = incidents.reduce((total, item) => total + item.inbounds.length, 0)
    return (
      <section className="offline-isolation-list is-compact" role="status">
        <span className="offline-isolation-icon" aria-hidden="true"><ServerOff size={16} /></span>
        <div className="offline-isolation-copy">
          <strong>{incidents.length === 1 ? `${serverName(incidents[0])} 已离线` : `${incidents.length} 台服务器已离线`}</strong>
          <p>{inboundCount > 0 ? `${inboundCount} 个入口仍在订阅里，可暂时剔除，已连接的会话不受影响。` : '可暂时把它的入口从订阅里剔除，已连接的会话不受影响。'}</p>
        </div>
        <button type="button" onClick={() => setOpen(true)}>剔除离线入口</button>
        {open && <OfflineIsolationDialog incidents={incidents} client={client} onClose={() => setOpen(false)} onChanged={onChanged} />}
      </section>
    )
  }
  return (
    <section className="offline-isolation-list">
      <div className="offline-isolation-list-head">
        <div className="offline-isolation-copy">
          <h2>临时剔除<span className="offline-isolation-count">{active.length}</span></h2>
          <p>这些入口暂不出现在新拉取的订阅里，已连接的会话不受影响。</p>
        </div>
        {incidents.length > 0 && <button type="button" className="ghost" onClick={() => setOpen(true)}>剔除离线入口</button>}
      </div>
      {error && <p className="form-error" role="alert">{error}</p>}
      <ul className="offline-isolation-items">
        {sorted.map(item => {
          const recovery = isolationRecovery(item, now)
          return (
            <li key={item.id}>
              <div className="offline-isolation-target">
                <strong title={item.inbound_name}>{item.inbound_name}</strong>
                <span title={serverName(item)}>{serverName(item)}</span>
              </div>
              <div className="offline-isolation-recovery" title={item.restore_at ? new Date(item.restore_at).toLocaleString() : undefined}>
                <Clock3 size={16} aria-hidden="true" />
                <div><span>{recovery.label}</span><small>{recovery.detail}</small></div>
              </div>
              <button type="button" className="ghost" disabled={busyID === item.id} aria-label={`恢复 ${serverName(item)} ${item.inbound_name} 的订阅`} onClick={() => void restore(item)}>
                {busyID === item.id ? <Loader2 size={16} className="spin" aria-hidden="true" /> : <RotateCcw size={16} aria-hidden="true" />}
                {busyID === item.id ? '恢复中…' : '恢复订阅'}
              </button>
            </li>
          )
        })}
      </ul>
      {open && <OfflineIsolationDialog incidents={incidents} client={client} onClose={() => setOpen(false)} onChanged={onChanged} />}
    </section>
  )
}

function OfflineIsolationDialog({ incidents, client, onClose, onChanged }: { incidents: Incident[]; client: Client; onClose: () => void; onChanged: () => void }) {
  const [selected, setSelected] = useState<number[]>(() => incidents.map(item => item.id))
  const [mode, setMode] = useState<'until_online' | 'duration'>('until_online')
  const [hours, setHours] = useState(6)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const toggle = (id: number) => setSelected(current => current.includes(id) ? current.filter(item => item !== id) : [...current, id])
  const submit = async () => {
    const chosen = incidents.filter(item => selected.includes(item.id))
    if (!chosen.length) {
      setError('请选择至少一台服务器')
      return
    }
    setBusy(true)
    setError('')
    try {
      for (const incident of chosen) {
        const preview = await client.requestV2(`/node-incidents/${incident.id}/preview`, {
          method: 'POST',
          body: JSON.stringify({
            action: 'isolate',
            inbound_ids: incident.inbounds.map(item => item.id),
            recovery_policy: mode === 'until_online' ? 'auto' : 'manual',
            duration_minutes: mode === 'duration' ? hours * 60 : undefined,
          }),
        })
        await client.requestV2(`/node-incidents/${incident.id}/confirm`, {
          method: 'POST',
          body: JSON.stringify({ confirmation_token: preview.confirmation_token }),
        })
      }
      onChanged()
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : '剔除失败')
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      isOpen
      onClose={busy ? () => undefined : onClose}
      title="临时剔除离线入口"
      footer={
        <>
          <button type="button" className="ghost" disabled={busy} onClick={onClose}>取消</button>
          <button type="button" disabled={busy || !selected.length} onClick={() => void submit()}>{busy ? '提交中…' : '确认剔除'}</button>
        </>
      }
    >
      <div className="offline-isolation-dialog">
        <p className="muted">只影响之后拉取的订阅，不会断开已经连上的会话，也不需要重新下发配置。</p>
        <fieldset>
          <legend>离线服务器</legend>
          {incidents.map(incident => (
            <label key={incident.id}>
              <input type="checkbox" checked={selected.includes(incident.id)} onChange={() => toggle(incident.id)} />
              <span className="offline-isolation-choice"><strong>{serverName(incident)}</strong><small>{incident.inbounds.length ? incident.inbounds.map(item => item.name).join('、') : '没有已发布入口'}</small></span>
            </label>
          ))}
        </fieldset>
        <fieldset>
          <legend>恢复方式</legend>
          <label><input type="radio" name="isolation-mode" checked={mode === 'until_online'} onChange={() => setMode('until_online')} /> 直至服务器恢复在线</label>
          <label><input type="radio" name="isolation-mode" checked={mode === 'duration'} onChange={() => setMode('duration')} /> 指定剔除时长</label>
          {mode === 'duration' && (
            <div className="offline-isolation-hours">
              {hourChoices.map(choice => (
                <button key={choice} type="button" className={hours === choice ? '' : 'ghost'} onClick={() => setHours(choice)}>{choice} 小时</button>
              ))}
              <label>自定义<input type="number" min={1} max={168} value={hours} onChange={event => setHours(Math.min(168, Math.max(1, Number(event.target.value) || 1)))} aria-label="剔除小时数" />小时</label>
            </div>
          )}
        </fieldset>
        {error && <p className="form-error" role="alert">{error}</p>}
      </div>
    </Dialog>
  )
}
