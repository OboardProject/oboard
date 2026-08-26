import React, { useState } from 'react'
import { Layers, Pencil, Plus, Trash2 } from 'lucide-react'
import { useDialogs } from './ui/dialog-context'
import { Dialog } from './ui/dialog'
import { Select } from './ui/select'
import { SettingsGroup, SettingsRow } from './settings/SettingsLayout'

export interface SnellProfilesPanelProps {
  data: any
  client: any
  load: (section?: string, options?: any) => Promise<void>
  notify: (message: string, tone?: 'success' | 'warning' | 'danger' | 'error' | 'info') => void
}

export type SnellProfile = { id: number; name: string; version: number; psk: string; obfs_mode: string; obfs_host: string; mode: string; reuse: boolean; tcp_fast_open: boolean; remark: string; builtin: boolean; enabled: boolean; usage_count: number }

const versions = [4, 6]
const obfsModes = ['none', 'http']
const v6Modes = ['default', 'unshaped', 'unsafe-raw']

export type SnellDraft = {
  name: string
  version: number
  psk: string
  obfs_mode: string
  obfs_host: string
  mode: string
  reuse: boolean
  tcp_fast_open: boolean
  remark: string
  enabled: boolean
}

export const emptySnellDraft = (version = 4): SnellDraft => ({ name: '', version, psk: '', obfs_mode: 'none', obfs_host: '', mode: 'default', reuse: false, tcp_fast_open: true, remark: '', enabled: true })

export function snellDraftFromProfile(profile: SnellProfile): SnellDraft {
  return { name: profile.name, version: profile.version, psk: profile.psk, obfs_mode: profile.obfs_mode, obfs_host: profile.obfs_host, mode: profile.mode, reuse: profile.reuse, tcp_fast_open: Boolean(profile.tcp_fast_open), remark: profile.remark, enabled: profile.enabled }
}

function profileMeta(profile: SnellProfile) {
  const parts = [`v${profile.version}`]
  if (profile.obfs_mode && profile.obfs_mode !== 'none') {
    parts.push(profile.obfs_host ? `HTTP · ${profile.obfs_host}` : 'HTTP')
  }
  if (profile.version === 6 && profile.mode && profile.mode !== 'default') {
    parts.push(profile.mode === 'unshaped' ? '无整形' : profile.mode)
  }
  if (profile.reuse) parts.push('复用')
  if (profile.tcp_fast_open) parts.push('TFO')
  parts.push(profile.usage_count > 0 ? `${profile.usage_count} 个入口` : '未使用')
  return parts
}

export function SnellProfileCards({ profiles, editingID, onEdit, onDelete }: { profiles: SnellProfile[]; editingID?: number; onEdit: (profile: SnellProfile) => void; onDelete: (profile: SnellProfile) => void }) {
  return <div className="snell-profile-grid">
    {profiles.map(profile => (
      <article className={`snell-profile-card${profile.builtin ? ' is-builtin' : ''}${profile.enabled === false ? ' is-disabled' : ''}${editingID === profile.id ? ' is-editing' : ''}`} key={profile.id}>
        <div className="snell-profile-main">
          <div className="snell-profile-identity">
            <strong className="snell-profile-name">{profile.name}</strong>
            {profile.builtin && <span className="badge neutral">内置</span>}
            {profile.enabled === false && <span className="badge neutral">已停用</span>}
          </div>
          <p className="snell-profile-meta">{profileMeta(profile).join(' · ')}</p>
          {profile.remark && <p className="snell-profile-remark">{profile.remark}</p>}
        </div>
        <div className="snell-profile-card-actions">
          <button type="button" className="ghost icon-button" onClick={() => onEdit(profile)} title={`编辑 ${profile.name}`} aria-label={`编辑 ${profile.name}`}><Pencil size={14} /></button>
          {!profile.builtin && <button type="button" className="ghost icon-button danger-text" onClick={() => onDelete(profile)} disabled={profile.usage_count > 0} title={profile.usage_count > 0 ? '仍有入口引用，请先解绑' : `删除 ${profile.name}`} aria-label={`删除 ${profile.name}`}><Trash2 size={14} /></button>}
        </div>
      </article>
    ))}
  </div>
}

export function SnellProfileEditor({ title, draft, setDraft, onSave, onCancel, saving, hideTitle }: { title: string; draft: SnellDraft; setDraft: (draft: SnellDraft) => void; onSave: () => void; onCancel: () => void; saving: boolean; hideTitle?: boolean }) {
  const isV6 = Number(draft.version) === 6
  return <div className={`snell-profile-editor${hideTitle ? ' is-dialog' : ''}`}>
    {!hideTitle && <h4>{title}</h4>}
    <div className="form settings-form">
      <SettingsRow label="预设名称" description="用于在入口表单中识别该套参数。">
        <input autoFocus value={draft.name} onChange={event => setDraft({ ...draft, name: event.target.value })} placeholder="例如 机房 A 通用 v4" />
      </SettingsRow>
      <SettingsRow label="Snell 版本" description="v4 参数简单；v6 为测试版协议，仅部分客户端支持。">
        <Select value={String(draft.version)} onChange={event => setDraft({ ...draft, version: Number(event.target.value), obfs_mode: Number(event.target.value) === 6 ? 'none' : draft.obfs_mode, mode: Number(event.target.value) === 4 ? 'default' : draft.mode })} aria-label="Snell 版本">
          {versions.map(version => <option key={version} value={String(version)}>v{version}</option>)}
        </Select>
      </SettingsRow>
      <SettingsRow label="PSK" description={isV6 ? 'v6 要求 12-255 字节。留空时入口会使用绑定用户的代理密码。' : '至少 8 字符。留空时入口会使用绑定用户的代理密码。'}>
        <input value={draft.psk} onChange={event => setDraft({ ...draft, psk: event.target.value })} placeholder="留空 = 使用用户密码" autoComplete="new-password" />
      </SettingsRow>
      {!isV6 && <>
        <SettingsRow label="混淆模式" description="HTTP 混淆；Host 留空时客户端使用默认 bing.com。">
          <Select value={draft.obfs_mode} onChange={event => setDraft({ ...draft, obfs_mode: event.target.value, obfs_host: event.target.value === 'none' ? '' : draft.obfs_host })} aria-label="混淆模式">
            {obfsModes.map(mode => <option key={mode} value={mode}>{mode === 'none' ? '无' : 'HTTP'}</option>)}
          </Select>
        </SettingsRow>
        {draft.obfs_mode !== 'none' && <SettingsRow label="混淆 Host" description="客户端握手时使用的伪装域名。">
          <input value={draft.obfs_host} onChange={event => setDraft({ ...draft, obfs_host: event.target.value })} placeholder="例如 bing.com" />
        </SettingsRow>}
      </>}
      {isV6 && <SettingsRow label="传输模式" description="v6 协议形态；unsafe-raw 特征最弱但可能影响兼容性。">
        <Select value={draft.mode} onChange={event => setDraft({ ...draft, mode: event.target.value })} aria-label="v6 传输模式">
          {v6Modes.map(mode => <option key={mode} value={mode}>{mode === 'default' ? '默认' : mode === 'unshaped' ? '无整形' : 'unsafe-raw'}</option>)}
        </Select>
      </SettingsRow>}
      <SettingsRow label="连接复用（reuse）" description="Snell 自带的连接复用，不使用 sing-box 通用 MUX。">
        <input type="checkbox" checked={draft.reuse} onChange={event => setDraft({ ...draft, reuse: event.target.checked })} />
      </SettingsRow>
      <SettingsRow label="TCP Fast Open" description="Snell 始终跑在 TCP 上。仅当服务器内核开放了 server 位（net.ipv4.tcp_fastopen 含 2）时才会真正生效。">
        <input type="checkbox" checked={draft.tcp_fast_open} onChange={event => setDraft({ ...draft, tcp_fast_open: event.target.checked })} />
      </SettingsRow>
      <SettingsRow label="备注" description="可选说明，例如适用机房或用途。">
        <input value={draft.remark} onChange={event => setDraft({ ...draft, remark: event.target.value })} placeholder="可选" />
      </SettingsRow>
      <div className="settings-actions">
        <button onClick={onSave} disabled={saving || !draft.name.trim()}>{saving ? '保存中...' : '保存预设'}</button>
        <button type="button" className="ghost" onClick={onCancel}>取消</button>
      </div>
    </div>
  </div>
}

export function SnellProfilesPanel({ data, client, load, notify }: SnellProfilesPanelProps) {
  const dialogs = useDialogs()
  const profiles: SnellProfile[] = data.snell_profiles || []
  const [editing, setEditing] = useState<null | { id?: number; draft: SnellDraft }>(null)
  const [saving, setSaving] = useState(false)

  const saveProfile = async () => {
    if (!editing) return
    setSaving(true)
    try {
      const body = { ...editing.draft }
      if (editing.id) {
        await client.request(`/snell-profiles/${editing.id}`, { method: 'PUT', body: JSON.stringify(body) })
        notify('预设已更新，引用入口将在下次部署时生效', 'success')
      } else {
        await client.request('/snell-profiles', { method: 'POST', body: JSON.stringify(body) })
        notify('预设已创建，可在入口表单中套用', 'success')
      }
      setEditing(null)
      await load()
    } catch (error: any) {
      notify(String(error?.message || error || '保存失败'), 'error')
    } finally {
      setSaving(false)
    }
  }

  const deleteProfile = async (profile: SnellProfile) => {
    if (!await dialogs.confirm({ title: `删除预设「${profile.name}」？`, message: '删除后无法恢复。', confirmText: '删除', tone: 'danger' })) return
    try {
      await client.request(`/snell-profiles/${profile.id}`, { method: 'DELETE' })
      notify('预设已删除', 'success')
      await load()
    } catch (error: any) {
      notify(String(error?.message || error || '删除失败'), 'error')
    }
  }

  return <section id="settings-panel-snell" role="tabpanel" className="settings-card">
    <SettingsGroup title="Snell 参数预设" description="预设让多个服务器入口快速使用同一套 Snell 参数；修改预设后，引用它的入口会在下次部署时应用新参数。内置预设不可删除。">
       <div className="snell-profiles-head">
         <span className="muted">共 {profiles.length} 套，内置 {profiles.filter(p => p.builtin).length} 套</span>
         <button type="button" onClick={() => setEditing({ draft: emptySnellDraft(4) })}><Plus size={14} />新建预设</button>
       </div>
       <SnellProfileCards profiles={profiles} editingID={editing?.id} onEdit={profile => setEditing({ id: profile.id, draft: snellDraftFromProfile(profile) })} onDelete={deleteProfile} />
     </SettingsGroup>
     {editing && (
       <Dialog isOpen={Boolean(editing)} onClose={() => setEditing(null)} title={editing.id ? '编辑预设' : '新建预设'} size="lg">
         <SnellProfileEditor title={editing.id ? '编辑预设' : '新建预设'} draft={editing.draft} setDraft={draft => setEditing({ id: editing.id, draft })} onSave={saveProfile} onCancel={() => setEditing(null)} saving={saving} hideTitle />
       </Dialog>
     )}
   </section>
}

export function SnellProfilesEmptyState() {
  return <div className="snell-profiles-empty"><Layers size={20} /><span>暂无 Snell 参数预设</span></div>
}