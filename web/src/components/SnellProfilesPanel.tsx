import React, { useState } from 'react'
import { Layers, Pencil, Plus, Trash2 } from 'lucide-react'
import { Select } from './ui/select'
import { SettingsGroup, SettingsRow } from './settings/SettingsLayout'

export interface SnellProfilesPanelProps {
  data: any
  client: any
  load: (section?: string, options?: any) => Promise<void>
  notify: (message: string, tone?: 'success' | 'warning' | 'danger' | 'error' | 'info') => void
}

type SnellProfile = { id: number; name: string; version: number; psk: string; obfs_mode: string; obfs_host: string; mode: string; reuse: boolean; remark: string; builtin: boolean; enabled: boolean; usage_count: number }

const versions = [4, 6]
const obfsModes = ['none', 'http']
const v6Modes = ['default', 'unshaped', 'unsafe-raw']

type Draft = {
  name: string
  version: number
  psk: string
  obfs_mode: string
  obfs_host: string
  mode: string
  reuse: boolean
  remark: string
  enabled: boolean
}

const emptyDraft = (version = 4): Draft => ({ name: '', version, psk: '', obfs_mode: 'none', obfs_host: '', mode: 'default', reuse: false, remark: '', enabled: true })

function ProfileCards({ profiles, onEdit, onDelete }: { profiles: SnellProfile[]; onEdit: (profile: SnellProfile) => void; onDelete: (profile: SnellProfile) => void }) {
  return <div className="snell-profile-grid">
    {profiles.map(profile => {
      const obfs = profile.obfs_mode && profile.obfs_mode !== 'none' ? ` · 混淆 ${profile.obfs_mode}${profile.obfs_host ? ` → ${profile.obfs_host}` : ''}` : ''
      const mode = profile.version === 6 && profile.mode && profile.mode !== 'default' ? ` · 模式 ${profile.mode}` : ''
      const reuse = profile.reuse ? ' · 复用开启' : ''
      return <div className={`snell-profile-card${profile.builtin ? ' builtin' : ''}${!profile.enabled ? ' disabled' : ''}`} key={profile.id}>
        <div className="snell-profile-card-head">
          <div>
            <strong>{profile.name}{profile.builtin && <span className="badge-soft badge-soft-info">内置</span>}</strong>
            <span>Snell v{profile.version}{obfs}{mode}{reuse}</span>
          </div>
          <span className="snell-profile-usage">{profile.usage_count > 0 ? `${profile.usage_count} 个入口使用` : '未使用'}</span>
        </div>
        {profile.remark && <p className="snell-profile-remark">{profile.remark}</p>}
        {!profile.enabled && <p className="snell-profile-disabled-note">已停用：新建入口不可再引用。</p>}
        <div className="snell-profile-card-actions">
          <button type="button" className="ghost" onClick={() => onEdit(profile)}><Pencil size={13} />编辑</button>
          {!profile.builtin && <button type="button" className="ghost danger" onClick={() => onDelete(profile)} disabled={profile.usage_count > 0} title={profile.usage_count > 0 ? '仍有入口引用，请先解绑' : undefined}><Trash2 size={13} />删除</button>}
        </div>
      </div>
    })}
  </div>
}

function ProfileEditor({ draft, setDraft, onSave, onCancel, saving }: { draft: Draft; setDraft: (draft: Draft) => void; onSave: () => void; onCancel: () => void; saving: boolean }) {
  const isV6 = Number(draft.version) === 6
  return <div className="snell-profile-editor">
    <div className="form settings-form">
      <SettingsRow label="预设名称" description="用于在入口表单中识别该套参数。">
        <input value={draft.name} onChange={event => setDraft({ ...draft, name: event.target.value })} placeholder="例如 机房 A 通用 v4" />
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
      <SettingsRow label="连接复用（reuse）" description="客户端可复用已建立的连接。">
        <input type="checkbox" checked={draft.reuse} onChange={event => setDraft({ ...draft, reuse: event.target.checked })} />
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
  const profiles: SnellProfile[] = data.snell_profiles || []
  const [editing, setEditing] = useState<null | { id?: number; draft: Draft }>(null)
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
    if (!window.confirm(`确认删除预设「${profile.name}」？`)) return
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
        <span className="muted">共 {profiles.length} 套（其中内置 {profiles.filter(p => p.builtin).length} 套）</span>
        <button type="button" className="ghost" onClick={() => setEditing({ draft: emptyDraft(4) })}><Plus size={14} />新建预设</button>
      </div>
      {editing && !editing.id && <ProfileEditor draft={editing.draft} setDraft={draft => setEditing({ draft })} onSave={saveProfile} onCancel={() => setEditing(null)} saving={saving} />}
      <ProfileCards profiles={profiles} onEdit={profile => setEditing({ id: profile.id, draft: { name: profile.name, version: profile.version, psk: profile.psk, obfs_mode: profile.obfs_mode, obfs_host: profile.obfs_host, mode: profile.mode, reuse: profile.reuse, remark: profile.remark, enabled: profile.enabled } })} onDelete={deleteProfile} />
    </SettingsGroup>
  </section>
}

export function SnellProfilesEmptyState() {
  return <div className="snell-profiles-empty"><Layers size={20} /><span>暂无 Snell 参数预设</span></div>
}