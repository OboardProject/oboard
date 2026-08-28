import React, { useState } from 'react'
import { Select } from '../../ui/select'
import { FormField } from '../../ui/form-field'
import type { Server, EntryIPMode } from '../../proxy-path/types'

const entryIPModes: EntryIPMode[] = ['auto','ipv4','ipv6','custom']
const ipStacks = ['auto','ipv4_only','ipv6_only','dual_stack','prefer_ipv4','prefer_ipv6']
const udpModes = ['allow','block','uot']
const listenModes = ['auto','dual','ipv4_only']
const listenModeLabels: Record<string,string> = { auto:'自动', dual:'双栈', ipv4_only:'仅 IPv4' }

function labelValue(v:any){ const m:Record<string,string>={ auto:'自动', ipv4:'IPv4', ipv6:'IPv6', custom:'自定义', allow:'允许', block:'阻断', uot:'UoT', auto_detect:'自动', dual_stack:'双栈', ipv4_only:'仅 IPv4', ipv6_only:'仅 IPv6', prefer_ipv4:'优先 IPv4', prefer_ipv6:'优先 IPv6' }; return m[String(v)]||String(v) }

function parsePortRangeInput(value: string){
  const match=value.trim().match(/^(\d+)\s*-\s*(\d+)$/)
  if(!match) return { valid:false, error:'请输入“起点-终点”，例如 10000-20000' }
  const start=Number(match[1]), end=Number(match[2])
  if(!Number.isSafeInteger(start)||!Number.isSafeInteger(end)||start<100||end<100||start>65535||end>65535) return { valid:false, error:'端口必须在 100-65535 之间' }
  if(start>end) return { valid:false, error:'起点不能大于终点' }
  return { valid:true as const, start, end }
}

function PortRangeInput({ start, end, onChange, onValidityChange }: { start:number; end:number; onChange:(s:number,e:number)=>void; onValidityChange:(v:boolean)=>void }) {
  const [value, setValue]=React.useState(`${start}-${end}`)
  const parsed = parsePortRangeInput(value)
  React.useEffect(()=> onValidityChange((parsed as any).valid), [onValidityChange, (parsed as any).valid])
  const updateValue=(next:string)=>{
    setValue(next)
    const np=parsePortRangeInput(next)
    if((np as any).valid) onChange((np as any).start,(np as any).end)
  }
  return (
    <div className={`port-range-control ${(parsed as any).valid ? 'valid':'invalid'}`}>
      <input type="text" value={value} onChange={e=>updateValue(e.target.value)} onBlur={()=>(parsed as any).valid && setValue(`${(parsed as any).start}-${(parsed as any).end}`)} placeholder="10000-20000" aria-label="端口范围" />
      {!(parsed as any).valid ? <small className="port-range-error" role="alert">{(parsed as any).error}</small> : null}
    </div>
  )
}

export function NetworkSettingsTab({ server, onSave, disabled, disabledReason }: { server: Server; onSave:(patch:any)=>Promise<void>; disabled?: boolean; disabledReason?: string }) {
  const [draft, setDraft]=useState<any>(()=> ({
    entry_ip_mode: (server.entry_ip_mode||'auto') as EntryIPMode,
    entry_address: server.entry_address||'',
    listen_ip: server.listen_ip||'',
    listen_mode: server.listen_mode||'auto',
    ip_stack: server.ip_stack||'auto',
    udp_inbound_mode: server.udp_inbound_mode||'allow',
    port_range_start: server.port_range_start||10000,
    port_range_end: server.port_range_end||20000,
    internal_port_range_start: server.internal_port_range_start||30000,
    internal_port_range_end: server.internal_port_range_end||59999,
  }))
  const [portValid, setPortValid]=useState(true)
  const [internalValid, setInternalValid]=useState(true)
  const [saving, setSaving]=useState(false)
  const update=(patch:any)=> setDraft((o:any)=>({...o,...patch}))
  const entryInvalid = Boolean(String(draft.entry_address||'').trim()) && draft.entry_ip_mode!=='custom'
  const save=async()=>{
    if(saving|| !portValid|| !internalValid || entryInvalid || disabled) return
    setSaving(true)
    try{ await onSave(draft) } finally{ setSaving(false) }
  }
  return (
    <div className="server-network-settings-tab">
      <section className="server-detail-section">
        <h3>公网地址</h3>
        <FormField label="入口地址模式" hint="自动：忽略手动入口；自定义：才生效">
          <Select value={draft.entry_ip_mode} onChange={e=>{ const n=e.target.value as EntryIPMode; update(n==='custom' ? {entry_ip_mode:n} : {entry_ip_mode:n, entry_address:''})}} disabled={disabled}>
            {entryIPModes.map(x=> <option key={x} value={x}>{labelValue(x)}</option>)}
          </Select>
        </FormField>
        {draft.entry_ip_mode==='custom' ? (
          <FormField label="自定义入口地址" hint="可填写域名、IPv4 或 IPv6">
            <input value={draft.entry_address} onChange={e=>update({entry_address: e.target.value})} placeholder="例如 1.2.3.4 或 example.com" disabled={disabled} />
          </FormField>
        ) : draft.entry_address ? (
          <div className="access-note warning"><strong>入口地址未生效</strong><span>已填写自定义入口「{draft.entry_address}」，但当前策略为 {labelValue(draft.entry_ip_mode)}，请设为自定义或<button type="button" className="link" onClick={()=>update({entry_address:''})}>清除</button></span></div>
        ) : null}
      </section>

      <section className="server-detail-section">
        <h3>监听</h3>
        <FormField label="监听 IP" hint="通常保持 0.0.0.0；填写具体地址可覆盖监听模式">
          <input value={draft.listen_ip} onChange={e=>update({listen_ip: e.target.value})} placeholder="0.0.0.0" disabled={disabled} />
        </FormField>
        <FormField label="监听模式" hint="自动：有全局 IPv6 地址时同时监听 IPv4 和 IPv6">
          <Select value={draft.listen_mode} onChange={e=>update({listen_mode: e.target.value})} disabled={disabled}>
            {listenModes.map(x=> <option key={x} value={x}>{listenModeLabels[x]}</option>)}
          </Select>
        </FormField>
        <FormField label="IP Stack" hint="出口优先使用的 IP 类型">
          <Select value={draft.ip_stack} onChange={e=>update({ip_stack: e.target.value})} disabled={disabled}>
            {ipStacks.map(x=> <option key={x} value={x}>{labelValue(x)}</option>)}
          </Select>
        </FormField>
        <FormField label="UDP 入站" hint="选择 UDP 的处理方式">
          <Select value={draft.udp_inbound_mode} onChange={e=>update({udp_inbound_mode: e.target.value})} disabled={disabled}>
            {udpModes.map(m=> <option key={m} value={m}>{labelValue(m)}</option>)}
          </Select>
        </FormField>
      </section>

      <section className="server-detail-section">
        <h3>端口</h3>
        <FormField label="自动端口范围" hint="自动托管的公网监听端口池；耗尽时部署会报错">
          <PortRangeInput start={draft.port_range_start} end={draft.port_range_end} onChange={(s,e)=>update({port_range_start:s, port_range_end:e})} onValidityChange={setPortValid} />
        </FormField>
        <FormField label="内部回环端口范围" hint="仅监听 127.0.0.1 / ::1 的内部组件端口池">
          <PortRangeInput start={draft.internal_port_range_start} end={draft.internal_port_range_end} onChange={(s,e)=>update({internal_port_range_start:s, internal_port_range_end:e})} onValidityChange={setInternalValid} />
        </FormField>
        {disabled && <small className="muted">{disabledReason}</small>}
      </section>

      <div className="server-workspace-actions">
        <button type="button" className="ghost" disabled={saving} onClick={()=>setDraft({
          entry_ip_mode: (server.entry_ip_mode||'auto'),
          entry_address: server.entry_address||'',
          listen_ip: server.listen_ip||'',
          listen_mode: server.listen_mode||'auto',
          ip_stack: server.ip_stack||'auto',
          udp_inbound_mode: server.udp_inbound_mode||'allow',
          port_range_start: server.port_range_start||10000,
          port_range_end: server.port_range_end||20000,
          internal_port_range_start: server.internal_port_range_start||30000,
          internal_port_range_end: server.internal_port_range_end||59999,
        })}>重置</button>
        <button type="button" onClick={()=>void save()} disabled={saving || !portValid || !internalValid || entryInvalid || disabled}>{saving? '保存中...':'保存网络设置'}</button>
      </div>
    </div>
  )
}
