import React, { useState } from 'react'
import { MotionDialogPanel } from '../ui/motion'
import { Select } from '../ui/select'
import { FormField } from '../ui/form-field'
import type { Server, RegionMode } from '../proxy-path/types'

function normalizeRegionCode(code?: string) {
  const v = String(code||'').trim().toUpperCase()
  return /^[A-Z]{2}$/.test(v) ? v : ''
}
function regionLabel(code?: string) {
  const v = normalizeRegionCode(code)
  if (!v) return '地区待检测'
  const overrides: Record<string,string> = { CN:'中国', HK:'香港', MO:'澳门', TW:'台湾' }
  if (overrides[v]) return overrides[v]
  try {
    const dn = new (Intl as any).DisplayNames(['zh-CN'], { type:'region' })
    const l = dn?.of(v)
    return l && l!==v ? l : v
  } catch { return v }
}
function RegionFlag({ code, size=20 }: { code?: string; size?: number }) {
  const v = normalizeRegionCode(code)
  if (!v) return <span style={{ width:size, height:size, display:'inline-grid', placeContent:'center', fontSize: size*0.7 }}>🌐</span>
  const flag = String.fromCodePoint(...Array.from(v).map(c=>127397+c.charCodeAt(0)))
  return <span style={{ fontSize: size*0.85 }}>{flag}</span>
}
function formatTableTime(v: string) {
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return v
  const pad=(n:number)=>String(n).padStart(2,'0')
  return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

const regionOptions = (() => {
  try {
    const iso = new Set('AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ CA CC CD CF CG CH CI CK CL CM CN CO CP CR CU CV CW CX CY CZ DE DG DJ DK DM DO DZ EC EE EG EH ER ES ET EU FI FJ FK FM FO FR GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY HK HM HN HR HT HU IC ID IE IL IM IN IO IQ IR IS IT JE JM JO JP KE KG KH KI KM KN KP KR KW KY KZ LA LB LC LI LK LR LS LT LU LV LY MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ NA NC NE NF NG NI NL NO NP NR NU NZ OM PA PC PE PF PG PH PK PL PM PN PR PS PT PW PY QA RE RO RS RU RW SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ UA UG UM UN US UY UZ VA VC VE VG VI VN VU WF WS XK XX YE YT ZA ZM ZW'.split(' '))
    const dn = typeof (Intl as any).DisplayNames === 'function' ? new (Intl as any).DisplayNames(['zh-CN'], { type:'region' }) : null
    const overrides: Record<string,string> = { CN:'中国', HK:'香港', MO:'澳门', TW:'台湾' }
    const vals: {code:string,label:string}[]=[]
    for (let a=65;a<=90;a++) for(let b=65;b<=90;b++){
      const code=String.fromCharCode(a,b)
      const localized=dn?.of(code)
      if(iso.has(code) && ((localized && localized!==code) || overrides[code])) vals.push({code,label: overrides[code] || localized})
    }
    return vals.sort((x,y)=>x.label.localeCompare(y.label,'zh-CN'))
  } catch { return [] }
})()

function serverExpiryInputValue(value?: string) {
  if (!value) return ''
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return String(value).slice(0,10)
  const pad=(n:number)=>String(n).padStart(2,'0')
  return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}`
}
function serverExpiryOutputValue(value: string) {
  if (!value) return null
  const d = new Date(value + 'T00:00:00')
  if (Number.isNaN(d.getTime())) return value
  return d.toISOString()
}
function addDaysToExpiryDate(current: string | undefined, days: number) {
  const base = current ? new Date(current) : new Date()
  if (Number.isNaN(base.getTime())) return serverExpiryInputValue(new Date().toISOString())
  base.setDate(base.getDate() + days)
  return serverExpiryInputValue(base.toISOString())
}

function ServerRegionField({ draft, update, servers }: { draft:any; update:(p:any)=>void; servers?: Server[] }) {
  // simplify: use Select
  return (
    <>
      <FormField label="地区模式">
        <Select value={draft.region_mode || 'auto'} onChange={e=>update({ region_mode: e.target.value as RegionMode })}>
          <option value="auto">自动识别</option>
          <option value="manual">手动指定</option>
        </Select>
      </FormField>
      { (draft.region_mode||'auto')==='manual' ? (
        <FormField label="手动地区">
          <Select value={normalizeRegionCode(draft.region_code)||'US'} onChange={e=>update({ region_code: e.target.value })}>
            {regionOptions.map(o=> <option key={o.code} value={o.code}>{o.label} ({o.code})</option>)}
          </Select>
        </FormField>
      ) : (
        <div className="access-note"><strong>自动识别</strong><span>当前识别为 {regionLabel(draft.detected_region_code)} {draft.detected_region_code ? `(${normalizeRegionCode(draft.detected_region_code)})` : ''}</span></div>
      )}
    </>
  )
}

export function ServerBasicSettingsDialog({ server, onCancel, onSubmit }: { server: Server; onCancel: () => void; onSubmit: (server:any)=>Promise<void> }) {
  const [draft, setDraft] = useState<any>(()=> ({
    ...server,
    region_mode: (server.region_mode||'auto') as RegionMode,
    region_code: server.region_code||'',
    detected_region_code: server.detected_region_code||'',
    service_start_at: serverExpiryInputValue((server as any).service_start_at),
    expires_at: serverExpiryInputValue(server.expires_at),
    renewal_cycle: (server as any).renewal_cycle||'monthly',
    auto_renew_enabled: Boolean(server.auto_renew_enabled),
    expiry_notify_enabled: server.expiry_notify_enabled!==false,
    display_tags: Array.isArray(server.display_tags)? server.display_tags: [],
  }))
  const [saving, setSaving]=useState(false)
  const update = (patch:any)=> setDraft((old:any)=>({...old,...patch}))
  const submit = async()=>{
    if(saving) return
    if(!String(draft.name||'').trim()) return
    setSaving(true)
    try{
      const payload={ ...draft, service_start_at: draft.service_start_at ? serverExpiryOutputValue(draft.service_start_at): null, clear_service_start_at: !draft.service_start_at, expires_at: draft.expires_at ? serverExpiryOutputValue(draft.expires_at): null, clear_expires_at: !draft.expires_at }
      await onSubmit(payload)
    } finally{ setSaving(false) }
  }
  const regionPreview = draft.region_mode==='manual' ? normalizeRegionCode(draft.region_code) : normalizeRegionCode(draft.detected_region_code)
  return (
    <MotionDialogPanel onCancel={onCancel} className="server-dialog server-basic-settings-dialog">
      <header className="dialog-head">
        <div>
          <h2>基础设置 · {server.name || `服务器 #${server.id}`}</h2>
          <p className="muted">服务器名称、地区与生命周期</p>
        </div>
        <button className="ghost dialog-close icon-button" onClick={onCancel} disabled={saving} aria-label="关闭">×</button>
      </header>
      <div className="dialog-body">
        <div className="form server-dialog-form labeled-form">
          <div className="form-section-title">基本信息</div>
          <FormField label="服务器名称" required>
            <input value={draft.name||''} onChange={e=>update({name: e.target.value})} placeholder="例如：server-1" />
          </FormField>
          <ServerRegionField draft={draft} update={update} />
          {regionPreview ? <div className="access-note"><span>预览：<RegionFlag code={regionPreview} size={16}/> {regionLabel(regionPreview)} ({regionPreview})</span></div> : null}

          <div className="form-section-title">生命周期</div>
          <FormField label="计费开始日" hint="用于推导流量重置日，留空则按到期日">
            <input type="date" value={draft.service_start_at||''} onChange={e=>update({service_start_at: e.target.value})} />
          </FormField>
          <FormField label="到期日" hint="留空表示不追踪到期">
            <input type="date" value={draft.expires_at||''} onChange={e=>update({expires_at: e.target.value})} />
            <div className="server-expiry-quick-row">
              <span>快速延长</span>
              {[7,30,90,365].map(d=>{
                const label = d===365? '1 年' : `${d} 天`
                return <button key={d} type="button" className="ghost" onClick={()=>update({expires_at: addDaysToExpiryDate(draft.expires_at, d)})}>+{label}</button>
              })}
              {[1,3].map(d=> <button key={d} type="button" className="ghost" onClick={()=>update({expires_at: addDaysToExpiryDate(draft.expires_at, d)})}>+{d} 天</button>)}
            </div>
          </FormField>
          <FormField label="自动续期" hint="到期后保留 3 天宽限期，第 3 天自动顺延">
            <div style={{ display:'flex', gap:8, alignItems:'center' }}>
              <label style={{display:'inline-flex', alignItems:'center', gap:6}}>
                <input type="checkbox" checked={Boolean(draft.auto_renew_enabled)} onChange={e=>update({auto_renew_enabled: e.target.checked})} />
                <span>开启自动续期</span>
              </label>
              {draft.auto_renew_enabled && (
                <Select value={draft.renewal_cycle} onChange={e=>update({renewal_cycle: e.target.value})}>
                  <option value="monthly">月付</option>
                  <option value="quarterly">季付</option>
                </Select>
              )}
            </div>
          </FormField>
          <FormField label="到期提醒">
            <label style={{display:'inline-flex', alignItems:'center', gap:6}}>
              <input type="checkbox" checked={draft.expiry_notify_enabled!==false} onChange={e=>update({expiry_notify_enabled: e.target.checked})} />
              <span>到期前按通知设置提醒</span>
            </label>
          </FormField>
        </div>
      </div>
      <footer className="dialog-actions">
        <button className="ghost" onClick={onCancel} disabled={saving}>取消</button>
        <button onClick={()=>void submit()} disabled={saving || !String(draft.name||'').trim()}>{saving? '保存中...':'保存修改'}</button>
      </footer>
    </MotionDialogPanel>
  )
}
