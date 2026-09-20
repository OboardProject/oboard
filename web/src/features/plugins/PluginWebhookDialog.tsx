import React, { useEffect, useRef, useState } from 'react'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import { Input } from '../../components/ui/input'
import * as api from './api'
import type { PluginTrigger, PluginsWorkspaceProps } from './types'

type Props = { pluginID: number; isAdmin: boolean; request: PluginsWorkspaceProps['client']['requestV2']; onClose: () => void }

export function webhookURL(id: string) {
  const href = document.querySelector('base')?.getAttribute('href') || '/'
  const basePath = new URL(href, window.location.origin).pathname.replace(/\/+$/, '')
  return `${window.location.origin}${basePath}/api/v1/plugin-webhooks/receive/${encodeURIComponent(id)}`
}

export function PluginWebhookDialog(props: Props) {
  return props.isAdmin ? <WebhookManager key={props.pluginID} {...props} /> : null
}

function WebhookManager({ pluginID, request, onClose }: Props) {
  const [items, setItems] = useState<api.PluginWebhook[]>([])
  const [bindings, setBindings] = useState<PluginTrigger[]>([])
  const [grants, setGrants] = useState<api.PluginWebhookGrant[]>([])
  const [bindingID, setBindingID] = useState('')
  const [grantID, setGrantID] = useState('')
  const [issued, setIssued] = useState<{ id: string; secret: string } | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const pending = useRef(false)
  const mounted = useRef(false)
  const load = async () => {
    const [endpoints, triggers, approvals] = await Promise.all([
      api.listWebhooks(request, pluginID), api.listTriggers(request, pluginID), api.listWebhookGrants(request, pluginID),
    ])
    if (!mounted.current) return
    setItems(endpoints.webhooks || [])
    setBindings((triggers.triggers || []).filter(item => item.plugin_id === pluginID && item.kind === 'event' && item.spec?.event === 'plugin.webhook'))
    setGrants(approvals.grants || [])
  }
  useEffect(() => {
    mounted.current = true
    void load().catch(() => { if (mounted.current) setError('读取 Webhook 失败，请关闭后重试。') }).finally(() => { if (mounted.current) setLoading(false) })
    return () => { mounted.current = false }
  }, [])
  const binding = bindings.find(item => item.id === Number(bindingID))
  const matching = grants.filter(grant => grant.plugin_id === pluginID && grant.binding_id === binding?.id && grant.revision_id === binding?.revision_id && !grant.revoked_at && (!grant.expires_at || Date.parse(grant.expires_at) > Date.now()))
  const perform = async (action: () => Promise<{ webhook: api.PluginWebhook; secret?: string }>) => {
    if (pending.current || issued) return
    pending.current = true; setBusy(true); setError(''); setNotice('')
    try {
      const result = await action()
      if (!mounted.current) return
      if (result.secret) setIssued({ id: result.webhook.id, secret: result.secret })
      setItems(previous => [...previous.filter(item => item.id !== result.webhook.id), result.webhook])
    } catch {
      if (mounted.current) {
        setError('操作未完成或状态已变化，请核对最新状态后重试。若密钥响应丢失，请轮换密钥。')
        try { await load() } catch { /* Retain the last known state; generation checks prevent stale writes. */ }
      }
    } finally { pending.current = false; if (mounted.current) setBusy(false) }
  }
  const copy = async (value: string) => {
    try { await navigator.clipboard.writeText(value); if (mounted.current) setNotice('已复制，请妥善保管剪贴板内容。') }
    catch { if (mounted.current) setNotice('无法访问剪贴板，请手动选择并复制。') }
  }
  const close = () => { if (!pending.current) { setIssued(null); setNotice(''); onClose() } }
  return <Dialog isOpen onClose={close} title="Webhook 管理" size="lg"><div className="space-y-4">
    <p className="text-sm text-muted-foreground">仅管理员可管理。端点绑定固定触发器与授权；请求必须签名。修改触发器后需重新审核并创建端点。启用端点不会启用插件或触发器。</p>
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {notice && <p role="status" className="text-sm">{notice}</p>}
    {issued && <section className="space-y-3 rounded-xl border border-border p-3" aria-label="一次性密钥">
      <p className="text-sm font-semibold">密钥仅此次显示，关闭后无法读取。轮换后旧密钥立即失效。</p>
      <label className="block text-sm">接收地址<Input readOnly value={webhookURL(issued.id)} /></label>
      <label className="block text-sm">一次性签名密钥<Input readOnly type="password" autoComplete="off" value={issued.secret} /></label>
      <div className="flex flex-wrap gap-2"><Button variant="outline" onClick={() => void copy(webhookURL(issued.id))}>复制地址</Button><Button variant="outline" onClick={() => void copy(issued.secret)}>复制密钥</Button><Button onClick={() => { setIssued(null); setNotice('') }}>已保存，清除密钥</Button></div>
    </section>}
    {loading ? <p>正在读取 Webhook…</p> : <>
      <form className="space-y-3" onSubmit={event => { event.preventDefault(); if (binding && matching.some(grant => grant.id === Number(grantID))) void perform(() => api.createWebhook(request, binding.id, Number(grantID))) }}>
        <label className="block text-sm">固定触发器<select className="block w-full rounded-xl border border-border bg-background p-2" required disabled={busy || !!issued} value={bindingID} onChange={event => { setBindingID(event.target.value); setGrantID('') }}><option value="">请选择 Webhook 触发器</option>{bindings.map(item => <option key={item.id} value={item.id}>{item.name} · 版本 #{item.revision_id} · 绑定版本 {item.binding_revision}</option>)}</select></label>
        <label className="block text-sm">匹配的授权<select className="block w-full rounded-xl border border-border bg-background p-2" required disabled={busy || !!issued || !binding} value={grantID} onChange={event => setGrantID(event.target.value)}><option value="">请选择授权</option>{matching.map(grant => <option key={grant.id} value={grant.id}>授权 #{grant.id} · 版本 #{grant.revision_id}</option>)}</select></label>
        {!bindings.length && <p className="text-sm text-muted-foreground">请先创建 Webhook 事件触发器，再由管理员审核绑定授权。</p>}
        {binding && !matching.length && <p className="text-sm text-muted-foreground">此触发器没有有效的绑定授权，请先完成审核。创建时还会验证授权与当前内容一致。</p>}
        <Button type="submit" disabled={busy || !!issued || !binding || !matching.some(grant => grant.id === Number(grantID))}>创建端点</Button>
      </form>
      <div className="space-y-3">{!items.length && <p className="text-sm text-muted-foreground">尚无 Webhook 端点。</p>}{items.map(item => <section key={item.id} className="space-y-2 rounded-xl border border-border p-3" aria-label={`端点 ${item.id}`}>
        <p className="text-sm">{bindings.find(value => value.id === item.binding_id)?.name || `触发器 #${item.binding_id}`} · {item.enabled ? '已启用' : '已停用'} · 密钥与状态代次 {item.generation}</p>
        <p className="text-xs text-muted-foreground">固定版本 #{item.revision_id} · 绑定版本 {item.binding_revision} · 授权 #{item.grant_id}</p>
        <p className="break-all text-sm">{webhookURL(item.id)}</p>
        <div className="flex flex-wrap gap-2"><Button size="sm" variant="outline" onClick={() => void copy(webhookURL(item.id))}>复制接收地址</Button><Button size="sm" variant="outline" disabled={busy || !!issued} onClick={() => void perform(() => api.changeWebhook(request, item, !item.enabled))}>{item.enabled ? '停用端点' : '启用端点'}</Button><Button size="sm" variant="outline" disabled={busy || !!issued} onClick={() => void perform(() => api.changeWebhook(request, item, item.enabled, true))}>轮换密钥</Button></div>
      </section>)}</div>
    </>}
    <div className="flex justify-end"><Button variant="ghost" disabled={busy} onClick={close}>关闭</Button></div>
  </div></Dialog>
}
