import React, { useEffect, useState } from 'react'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import { Input } from '../../components/ui/input'
import * as api from './api'
import type { Plugin, PluginRevision } from './types'

type RequestV2 = (path: string, init?: RequestInit) => Promise<any>

export function PluginTriggerDialog({ plugins, servers, request, onClose, onSaved }: {
  plugins: Plugin[]; servers: Array<{ id: number; name: string }>; request: RequestV2; onClose: () => void; onSaved: () => void
}) {
  const [pluginID, setPluginID] = useState('')
  const [revisionID, setRevisionID] = useState('')
  const [revisions, setRevisions] = useState<PluginRevision[]>([])
  const [name, setName] = useState('')
  const [params, setParams] = useState('{}')
  const [subjects, setSubjects] = useState<number[]>([])
  const [targets, setTargets] = useState<number[]>([])
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => {
    let active = true
    if (!pluginID) return
    setLoading(true)
    void api.getPlugin(request, Number(pluginID)).then(detail => {
      if (active) setRevisions((detail.revisions || []).filter(item => item.plugin_id === Number(pluginID) && item.status === 'published'))
    }).catch(err => { if (active) setError(err.message || '读取版本失败') }).finally(() => { if (active) setLoading(false) })
    return () => { active = false }
  }, [pluginID, request])
  const scope = (label: string, selected: number[], update: (ids: number[]) => void) => <fieldset className="space-y-2 rounded-xl border border-border p-3"><legend>{label}</legend>{servers.map(server => <label key={server.id} className="flex items-center gap-2"><input type="checkbox" disabled={saving} checked={selected.includes(server.id)} onChange={e => update(e.target.checked ? [...selected, server.id] : selected.filter(id => id !== server.id))} />{server.name}</label>)}</fieldset>
  return <Dialog isOpen onClose={() => { if (!saving) onClose() }} title="新建离线触发器" size="lg">
    <form className="space-y-4" onSubmit={async e => {
      e.preventDefault()
      setError('')
      if (saving) return
      try {
        if (!name.trim()) throw new Error('请输入触发器名称')
        if (!pluginID || !revisions.some(item => item.id === Number(revisionID))) throw new Error('请选择插件和已发布的固定版本')
        if (!subjects.length || !targets.length) throw new Error('请选择监听服务器和执行目标')
        let input: unknown
        try { input = JSON.parse(params) } catch { throw new Error('参数必须是有效的 JSON 对象') }
        if (!input || typeof input !== 'object' || Array.isArray(input)) throw new Error('参数必须是有效的 JSON 对象')
        setSaving(true)
        await api.createTrigger(request, { plugin_id: Number(pluginID), revision_id: Number(revisionID), name: name.trim(), kind: 'event', spec: { event: 'server.offline', timezone: 'UTC', sustain_seconds: 0, subject_server_ids: subjects, target_server_ids: targets }, params: input, enabled: false })
        onSaved()
      } catch (err: any) { setError(err.message || '保存触发器失败') } finally { setSaving(false) }
    }}>
      <p className="text-sm text-muted-foreground">服务器离线时触发，绑定固定版本，不会追随最新发布。保存后保持关闭，审核授权后再启用。</p>
      <label className="block">名称<Input required value={name} disabled={saving} onChange={e => setName(e.target.value)} /></label>
      <label className="block">插件<select className="w-full rounded-xl border border-border bg-background p-2" required disabled={saving} value={pluginID} onChange={e => { setPluginID(e.target.value); setRevisionID(''); setRevisions([]); setError(''); setLoading(false) }}><option value="">请选择插件</option>{plugins.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <label className="block">固定发布版本<select className="w-full rounded-xl border border-border bg-background p-2" required disabled={!pluginID || loading || saving} value={revisionID} onChange={e => setRevisionID(e.target.value)}><option value="">{loading ? '正在加载版本…' : '请选择固定版本'}</option>{revisions.map(item => <option key={item.id} value={item.id}>版本 {item.revision_number} · #{item.id}</option>)}</select></label>
      {pluginID && !loading && !revisions.length && <p className="text-sm">此插件没有可选的已发布版本。</p>}
      {scope('监听服务器（离线事件来源）', subjects, setSubjects)}
      {scope('执行目标（仍受插件授权限制）', targets, setTargets)}
      <label className="block">运行参数（JSON 对象）<textarea className="w-full rounded-xl border border-border bg-background p-2 font-mono" rows={4} required value={params} disabled={saving} onChange={e => setParams(e.target.value)} /></label>
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      <div className="flex justify-end gap-2"><Button type="button" variant="ghost" disabled={saving} onClick={onClose}>取消</Button><Button type="submit" disabled={saving || loading || !revisionID}>{saving ? '保存中…' : '保存触发器'}</Button></div>
    </form>
  </Dialog>
}
