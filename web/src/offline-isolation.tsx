import { useState } from 'react'
import { Dialog } from './components/ui/dialog'

type Inbound = { id: number; name: string }
type Incident = { id: number; version: number; server_id: number; server_name: string; inbounds: Inbound[] }
type ActiveIsolation = { id: number; incident_id: number; server_id: number; server_name: string; inbound_name: string; recovery_policy: string; restore_at?: string }
type IsolationData = { incidents?: Incident[]; active?: ActiveIsolation[] }
type Client = { requestV2: (path: string, init?: RequestInit) => Promise<any> }

const hourChoices = [1, 6, 12, 24]

function isolationLabel(item: ActiveIsolation) {
  if (item.recovery_policy === 'auto') return '直至服务器恢复在线'
  if (item.restore_at) {
    const at = new Date(item.restore_at)
    if (!Number.isNaN(at.getTime())) return `至 ${at.toLocaleString()}`
  }
  return '直到手动恢复'
}

export function OfflineIsolationPrompt({ isolation, client, onManage, onChanged }: { isolation?: IsolationData; client: Client; onManage: () => void; onChanged: () => void }) {
  const incidents = isolation?.incidents || []
  const [open, setOpen] = useState(false)
  if (!incidents.length) return null
  const names = incidents.map(item => item.server_name || `服务器 #${item.server_id}`).join('、')
  return (
    <section className="offline-isolation-prompt" role="status">
      <div>
        <strong>{incidents.length === 1 ? `${names} 已离线` : `${incidents.length} 台服务器已离线`}</strong>
        <p>可以把已发布入口暂时从订阅里拿掉。可设定剔除时长，或保持到服务器恢复在线。</p>
      </div>
      <div className="offline-isolation-actions">
        <button type="button" onClick={() => setOpen(true)}>临时剔除</button>
        <button type="button" className="ghost" onClick={onManage}>查看剔除记录</button>
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
  return (
    <section className="offline-isolation-list">
      <div className="offline-isolation-list-head">
        <div>
          <h2>临时剔除</h2>
          <p>这些入口当前不会出现在新的订阅里。</p>
        </div>
        {incidents.length > 0 && <button type="button" onClick={() => setOpen(true)}>剔除离线入口</button>}
      </div>
      {error && <p className="form-error" role="alert">{error}</p>}
      {active.length === 0 ? <p className="muted">当前没有正在生效的剔除。</p> : (
        <div className="activity-list">
          {active.map(item => (
            <div className="activity-item" key={item.id}>
              <div>
                <strong>{item.server_name || `服务器 #${item.server_id}`} · {item.inbound_name}</strong>
                <span>{isolationLabel(item)}</span>
              </div>
              <button type="button" className="ghost" disabled={busyID === item.id} onClick={() => void restore(item)}>{busyID === item.id ? '恢复中…' : '恢复订阅'}</button>
            </div>
          ))}
        </div>
      )}
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
              <span>{incident.server_name || `服务器 #${incident.server_id}`} · {incident.inbounds.map(item => item.name).join('、')}</span>
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
