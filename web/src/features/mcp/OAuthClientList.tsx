import React, { useCallback, useEffect, useState } from 'react'
import { Edit3, Globe, PauseCircle, Play, Plus, Trash2, X } from 'lucide-react'
import { AnimatePresence } from 'motion/react'
import { FormField } from '../../components/ui/form-field'
import { MotionDialogPanel } from '../../components/ui/motion'
import { createClient, deleteClient, listClients, updateClient, type RequestV2 } from './api'
import type { OAuthClient, ToastTone } from './types'

interface OAuthClientListProps {
  requestV2: RequestV2
  notify: (message: string, tone?: ToastTone) => void
  confirm: (options: { title: string; message: string; confirmText?: string; tone?: 'danger' }) => Promise<boolean>
}

interface ClientDraft {
  name: string
  redirects: string
  metadataURI: string
}

export function OAuthClientList({ requestV2, notify, confirm }: OAuthClientListProps) {
  const [clients, setClients] = useState<OAuthClient[]>([])
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingID, setEditingID] = useState('')
  const [draft, setDraft] = useState<ClientDraft>({ name: '', redirects: 'http://127.0.0.1/callback', metadataURI: '' })
  const [working, setWorking] = useState('')

  const load = useCallback(async () => {
    try {
      setClients(await listClients(requestV2))
    } catch (error: any) {
      notify?.(error?.message || 'OAuth Client 列表加载失败', 'error')
    }
  }, [requestV2, notify])

  useEffect(() => { void load() }, [load])

  const openDialog = (client?: OAuthClient) => {
    setEditingID(client?.id || '')
    setDraft(client
      ? { name: client.name, redirects: (client.redirect_uris || []).join('\n'), metadataURI: client.metadata_uri || '' }
      : { name: '', redirects: 'http://127.0.0.1/callback', metadataURI: '' })
    setDialogOpen(true)
  }

  const splitValues = (raw: string) => raw.split('\n').map(item => item.trim()).filter(Boolean)

  const save = async (event: React.FormEvent) => {
    event.preventDefault()
    setWorking(editingID ? 'oauth-update' : 'oauth-create')
    try {
      if (editingID) {
        await updateClient(requestV2, editingID, { client_name: draft.name, redirect_uris: splitValues(draft.redirects), ...(draft.metadataURI.trim() ? { metadata_uri: draft.metadataURI.trim() } : {}) })
      } else {
        await createClient(requestV2, { client_name: draft.name, redirect_uris: splitValues(draft.redirects), ...(draft.metadataURI.trim() ? { metadata_uri: draft.metadataURI.trim() } : {}) })
      }
      setDialogOpen(false)
      await load()
      notify?.(editingID ? 'OAuth Client 已更新' : 'OAuth Client 已创建', 'success')
    } catch (error: any) {
      notify?.(error?.message || '保存失败', 'error')
    } finally {
      setWorking('')
    }
  }

  const toggle = async (client: OAuthClient) => {
    setWorking(`oauth-${client.id}`)
    try {
      await updateClient(requestV2, client.id, { enabled: !client.enabled })
      await load()
    } catch (error: any) {
      notify?.(error?.message || '切换状态失败', 'error')
    } finally {
      setWorking('')
    }
  }

  const remove = async (client: OAuthClient) => {
    const confirmed = await confirm({
      title: `删除 ${client.name}？`,
      message: '已签发的授权码和访问令牌会立即失效，此操作不能撤销。',
      confirmText: '删除',
      tone: 'danger',
    })
    if (!confirmed) return
    setWorking(`oauth-delete-${client.id}`)
    try {
      await deleteClient(requestV2, client.id)
      if (editingID === client.id) setDialogOpen(false)
      await load()
      notify?.('OAuth Client 已删除', 'success')
    } catch (error: any) {
      notify?.(error?.message || '删除失败', 'error')
    } finally {
      setWorking('')
    }
  }

  return (
    <>
      <section className="settings-card">
        <div className="settings-card-head automation-section-head"><div><h3>OAuth 2.1 Client</h3><p className="muted">供远程 MCP 使用 PKCE S256 授权。客户端仅保存身份、回调与启用状态；可申请的权限在授权时由用户决定。</p></div><button type="button" onClick={() => openDialog()}><Plus size={14} />注册</button></div>
        <div className="automation-list">{clients.length ? clients.map((item) => <div className="automation-row" key={item.id}><div><div className="automation-row-title"><strong>{item.name}</strong><span className={`automation-state ${item.enabled ? 'is-enabled' : ''}`}>{item.enabled ? '已启用' : '已停用'}</span>{item.identity_type === 'cimd' && <span className="automation-state">CIMD</span>}</div><span>{item.id}</span><small>{item.identity_type === 'cimd' && item.metadata_uri ? `CIMD · ${item.metadata_uri}` : '预注册客户端'} · {item.redirect_uris.join(', ')}</small></div><div><button className="ghost icon-button" onClick={() => openDialog(item)} title="编辑" aria-label={`编辑 ${item.name}`}><Edit3 size={15} /></button><button className="ghost icon-button" onClick={() => void toggle(item)} title={item.enabled ? '禁用' : '启用'} aria-label={item.enabled ? '禁用' : '启用'}>{item.enabled ? <PauseCircle size={15} /> : <Play size={15} />}</button><button className="ghost icon-button danger-text" onClick={() => void remove(item)} title="删除" aria-label={`删除 ${item.name}`}><Trash2 size={15} /></button></div></div>) : <div className="automation-empty"><Globe size={20} /><span>还没有 OAuth Client</span><button type="button" className="ghost" onClick={() => openDialog()}>注册客户端</button></div>}</div>
      </section>

      <AnimatePresence>{dialogOpen && <MotionDialogPanel onCancel={() => setDialogOpen(false)} className="automation-dialog">
        <header className="dialog-head"><div><h2>{editingID ? '编辑 OAuth Client' : '注册 OAuth Client'}</h2><p className="muted">客户端不声明权限上限；权限在用户授权时决定。</p></div><button type="button" className="ghost dialog-close icon-button" onClick={() => setDialogOpen(false)} aria-label="关闭" title="关闭"><X size={16} /></button></header>
        <div className="dialog-body">
          <form id="oauth-client-form" className="form automation-dialog-form" onSubmit={save}>
            {editingID && <FormField label="Client ID" hint="MCP 客户端配置时使用的 client_id。">
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <input readOnly value={editingID} onFocus={event => event.target.select()} />
                <button type="button" className="ghost" onClick={() => void navigator.clipboard?.writeText(editingID).then(() => notify?.('Client ID 已复制', 'success'))} title="复制 Client ID">复制</button>
              </div>
            </FormField>}
            <FormField label="名称" required><input autoFocus required value={draft.name} onChange={event => setDraft({ ...draft, name: event.target.value })} placeholder="例如：Hermes MCP" /></FormField>
            <FormField label="回调地址" required hint="每行一个完整地址。"><textarea required rows={2} value={draft.redirects} onChange={event => setDraft({ ...draft, redirects: event.target.value })} /></FormField>
            <FormField label="CIMD 元数据地址（可选）" hint="HTTPS URL；文档中的 client_id 必须与地址完全一致。"><input value={draft.metadataURI} onChange={event => setDraft({ ...draft, metadataURI: event.target.value })} placeholder="https://example.com/.well-known/oauth-client-metadata.json" /></FormField>
          </form>
        </div>
        <footer className="dialog-actions"><button type="button" className="ghost" onClick={() => setDialogOpen(false)}>取消</button><button type="submit" form="oauth-client-form" disabled={Boolean(working) || !draft.name.trim() || !draft.redirects.trim()}>{editingID ? '保存' : '注册'}</button></footer>
      </MotionDialogPanel>}</AnimatePresence>
    </>
  )
}
