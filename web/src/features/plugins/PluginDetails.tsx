import React, { useEffect, useRef, useState } from 'react'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import { Input } from '../../components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../../components/ui/tabs'
import * as api from './api'
import { PluginPageRenderer, runStatusLabel } from './PluginPageRenderer'
import { PluginRunDialog } from './PluginRunDialog'
import { PluginWebhookDialog } from './PluginWebhookDialog'
import { SchemaForm, schemaDefaults } from './SchemaForm'
import type { Plugin, PluginGrant, PluginInstallation, PluginPackageVersion, PluginRevision, PluginRun, PluginTrigger, PluginsWorkspaceProps } from './types'

export function PluginDetails({ plugin, request, isAdmin, servers, onClose, onChanged, onDevelop }: {
  plugin: Plugin; request: PluginsWorkspaceProps['client']['requestV2']; isAdmin: boolean
  servers: Array<{ id: number; name: string }>; onClose: () => void; onChanged: () => void; onDevelop: () => void
}) {
  const [item, setItem] = useState(plugin)
  const [tab, setTab] = useState('overview')
  const [versions, setVersions] = useState<PluginPackageVersion[]>([])
  const [installation, setInstallation] = useState<PluginInstallation | null>(null)
  const [published, setPublished] = useState<PluginRevision | null>(null)
  const [grants, setGrants] = useState<PluginGrant[]>([])
  const [triggers, setTriggers] = useState<PluginTrigger[]>([])
  const [runs, setRuns] = useState<PluginRun[]>([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const pending = useRef(false)
  const [loading, setLoading] = useState(true)
  const [configOpen, setConfigOpen] = useState(false)
  const [webhookOpen, setWebhookOpen] = useState(false)
  const [config, setConfig] = useState<Record<string, unknown>>({})
  const [secretOpen, setSecretOpen] = useState(false)
  const [secretName, setSecretName] = useState('')
  const [secretValue, setSecretValue] = useState('')
  const [grantOpen, setGrantOpen] = useState(false)
  const [caps, setCaps] = useState<string[]>([])
  const [serverIDs, setServerIDs] = useState<number[]>([])
  const [origins, setOrigins] = useState<string[]>([])
  const [methods, setMethods] = useState<string[]>([])
  const [secretGrants, setSecretGrants] = useState<string[]>([])
  const [uninstallOpen, setUninstallOpen] = useState(false)
  const [retainData, setRetainData] = useState(true)
  const [runID, setRunID] = useState<number | null>(null)
  const [activation, setActivation] = useState<PluginPackageVersion | null>(null)
  const active = versions.find(version => version.revision_id === installation?.active_revision_id)
  const currentRevision = installation ? (installation.installed ? active?.revision_id : undefined) : published?.id
  const capabilities: string[] = installation ? active?.manifest.capabilities || [] : published?.manifest?.capabilities || []
  const configSchema = active?.manifest.config_schema || active?.manifest.params_schema || {}
  const secrets = active?.manifest.secrets || []
  const network = active?.manifest.network
  const refresh = async () => {
    const [detail, packages, triggerPage, runPage, grantPage] = await Promise.all([
      api.getPlugin(request, plugin.id), api.packageVersions(request, plugin.id), api.listTriggers(request, plugin.id),
      request(`/plugins/${plugin.id}/runs?limit=50`), isAdmin ? api.listGrants(request, plugin.id) : Promise.resolve({ grants: [] }),
    ])
    const configured = packages.versions?.length ? await api.getConfig(request, plugin.id) : null
    setItem(detail.plugin); setPublished(detail.published || null); setVersions(packages.versions || []); setInstallation(configured?.installation || null)
    setTriggers(triggerPage.triggers || []); setRuns(runPage.runs || []); setGrants(grantPage.grants || [])
  }
  useEffect(() => { void refresh().catch(e => setError(e.message || '读取插件详情失败')).finally(() => setLoading(false)) }, [plugin.id])
  const perform = async (action: () => Promise<unknown>) => {
    if (pending.current) return
    pending.current = true; setBusy(true); setError('')
    try { await action(); await refresh(); onChanged() }
    catch (e) { setError(e instanceof Error ? e.message : '操作失败') }
    finally { pending.current = false; setBusy(false) }
  }
  return <>
    <Dialog isOpen onClose={() => { if (!busy) onClose() }} title={item.name} size="xl"><div className="space-y-4">
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      {loading ? <p>正在读取插件详情…</p> : <Tabs value={tab} onValueChange={setTab}>
        <TabsList className="flex-wrap"><TabsTrigger value="overview">概览</TabsTrigger><TabsTrigger value="versions">版本</TabsTrigger><TabsTrigger value="permissions">权限</TabsTrigger><TabsTrigger value="triggers">触发器</TabsTrigger><TabsTrigger value="runs">执行记录</TabsTrigger>{active?.ui?.pages?.length ? <TabsTrigger value="page">插件页面</TabsTrigger> : null}<TabsTrigger value="develop">开发</TabsTrigger></TabsList>
        <TabsContent value="overview"><div className="space-y-4">
          <p className="text-sm">{item.description || '暂无说明'}</p>
          <p className="text-sm">{item.status === 'enabled' ? '已启用' : item.status === 'draft' ? '草稿' : '已停用'} · 当前版本 {active?.version || (published ? `#${published.revision_number || published.id}` : '未发布')}</p>
          <div className="flex flex-wrap gap-2">
            <Button disabled={busy || !currentRevision} onClick={() => void perform(() => api.updatePluginStatus(request, item, item.status !== 'enabled'))}>{item.status === 'enabled' ? '停用插件' : '启用插件'}</Button>
            {active && <Button variant="outline" onClick={() => { setError(''); setConfig({ ...schemaDefaults(configSchema), ...installation?.config }); setConfigOpen(true) }} disabled={busy}>配置插件</Button>}
            {isAdmin && secrets.length > 0 && <Button variant="outline" onClick={() => { setError(''); setSecretName(secrets[0].name); setSecretValue(''); setSecretOpen(true) }}>管理密钥</Button>}
            {installation?.installed && <Button variant="outline" onClick={() => { setRetainData(true); setUninstallOpen(true) }}>卸载</Button>}
          </div><p className="text-xs text-muted-foreground">启用不会自动授予权限。停用会禁止新执行，已下发动作仍需查看最终状态。</p>
        </div></TabsContent>
        <TabsContent value="versions"><div className="space-y-3">
          {!versions.length && <p className="text-sm text-muted-foreground">暂无安装包版本。源码草稿可在开发页维护。</p>}
          {versions.map(version => <section key={version.revision_id} className="space-y-2 rounded-xl border border-border p-3 text-sm"><div className="flex flex-wrap items-center justify-between gap-2"><strong>{version.version} {version.revision_id === active?.revision_id ? '· 当前版本' : ''}</strong><Button size="sm" variant="outline" disabled={busy || version.revision_id === active?.revision_id} onClick={() => setActivation(version)}>激活版本</Button></div><p className="break-all">SHA-256：{version.sha256}</p>{version.source_repository && <p className="break-all">来源：{version.source_repository}</p>}{version.source_commit && <p className="break-all">固定提交：{version.source_commit}</p>}</section>)}
        </div></TabsContent>
        <TabsContent value="permissions"><div className="space-y-3"><p className="text-sm">当前版本申请：{capabilities.join('、') || '无'}</p><p className="text-xs text-muted-foreground">授权只对固定版本有效；页面操作还受当前用户权限限制。节点电源操作需要独立的主机授权。</p>
          {isAdmin ? <><Button disabled={!currentRevision || busy} onClick={() => { setError(''); setCaps([]); setServerIDs([]); setOrigins([]); setMethods([]); setSecretGrants([]); setGrantOpen(true) }}>授予当前版本权限</Button>{grants.map(grant => <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-border p-3" key={grant.id}><div className="text-sm">版本 #{grant.revision_id} · {grant.revoked_at ? '已撤销' : '已授权'}<p className="text-xs text-muted-foreground">{Array.isArray(grant.capabilities) ? grant.capabilities.join('、') : '权限记录'}</p></div>{!grant.revoked_at && <Button size="sm" variant="outline" disabled={busy} onClick={() => void perform(() => api.revokeGrant(request, grant.id))}>撤销</Button>}</div>)}</> : <p className="text-sm text-muted-foreground">请管理员审核授权。</p>}
        </div></TabsContent>
        <TabsContent value="triggers"><div className="space-y-3">{isAdmin && <Button size="sm" variant="outline" onClick={() => setWebhookOpen(true)}>管理 Webhook</Button>}{!triggers.length && <p className="text-sm text-muted-foreground">没有触发器。可从触发器页新建固定版本的自动化规则。</p>}{triggers.map(trigger => <div key={trigger.id} className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-border p-3"><div className="text-sm">{trigger.name}<p className="text-xs text-muted-foreground">{trigger.kind} · 版本 #{trigger.revision_id} · {trigger.enabled ? '已启用' : '已暂停'}</p></div><Button size="sm" variant="outline" disabled={busy} onClick={() => void perform(() => api.updateTrigger(request, trigger.id, { enabled: !trigger.enabled }, trigger.binding_revision))}>{trigger.enabled ? '暂停' : '启用'}</Button></div>)}</div></TabsContent>
        <TabsContent value="runs"><div className="space-y-2">{!runs.length && <p className="text-sm text-muted-foreground">暂无执行记录。</p>}{runs.map(run => <button className="plugin-library-row" key={run.id} onClick={() => setRunID(run.id)}><span>#{run.id} · {runStatusLabel(run.status)}</span><span className="text-xs text-muted-foreground">{run.created_at}</span></button>)}</div></TabsContent>
        {active?.ui && <TabsContent value="page"><PluginPageRenderer key={active.revision_id} document={active.ui} pluginID={item.id} revisionID={active.revision_id} request={request} disabled={item.status !== 'enabled' || !installation?.installed} onViewRun={setRunID} /></TabsContent>}
        <TabsContent value="develop"><div className="space-y-3"><p className="text-sm text-muted-foreground">源码编辑用于开发与调试。修改后生成新版本，必须重新审核授权。普通安装请使用 ZIP 或 GitHub 仓库。</p><Button variant="outline" onClick={onDevelop}>打开源码编辑器</Button></div></TabsContent>
      </Tabs>}
    </div></Dialog>
    <Dialog isOpen={configOpen} onClose={() => { if (!busy) setConfigOpen(false) }} title="插件配置" size="lg"><form className="space-y-4" onSubmit={e => { e.preventDefault(); void perform(async () => { await api.saveConfig(request, item.id, config); setConfigOpen(false) }) }}>
      <SchemaForm key={`${active?.revision_id}-${configOpen}`} schema={configSchema} value={config} onChange={setConfig} />
      {!Object.keys(configSchema.properties || {}).length && <p className="text-sm text-muted-foreground">该版本没有声明配置项。</p>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}<Button type="submit" disabled={busy}>保存配置</Button>
    </form></Dialog>
    <Dialog isOpen={secretOpen} onClose={() => { if (!busy) { setSecretOpen(false); setSecretValue('') } }} title="管理员密钥" size="lg"><form className="space-y-4" onSubmit={e => { e.preventDefault(); void perform(async () => { await api.setSecret(request, item.id, secretName, secretValue); setSecretValue(''); setSecretOpen(false) }) }}>
      <p className="text-sm text-muted-foreground">密钥只写入，不回显。仅可管理当前版本声明的密钥；留空不能读取已保存密钥。</p>
      <label className="block text-sm">密钥名称<select required className="block w-full rounded-xl border border-border bg-background p-2" value={secretName} onChange={e => { setSecretName(e.target.value); setSecretValue('') }}>{secrets.map(secret => <option key={secret.name} value={secret.name}>{secret.purpose || secret.name} ({secret.name})</option>)}</select></label>
      <label className="block text-sm">新密钥值<Input type="password" required autoComplete="new-password" value={secretValue} onChange={e => setSecretValue(e.target.value)} /></label>
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}<div className="flex gap-2"><Button type="submit" disabled={busy}>保存密钥</Button><Button type="button" variant="outline" disabled={busy || !secretName} onClick={() => void perform(async () => { await api.deleteSecret(request, item.id, secretName); setSecretValue(''); setSecretOpen(false) })}>删除该名称密钥</Button></div>
    </form></Dialog>
    <Dialog isOpen={grantOpen} onClose={() => { if (!busy) setGrantOpen(false) }} title="审核当前版本权限" size="lg"><form className="space-y-4" onSubmit={e => { e.preventDefault(); void perform(async () => { await api.createGrant(request, { plugin_id: item.id, revision_id: currentRevision, capabilities: caps, resource_scope: { servers: { mode: 'selected', ids: serverIDs } }, constraints: caps.includes('network.request') ? { network: { allowed_origins: origins, allowed_methods: methods, max_request_bytes: network?.max_request_bytes, max_response_bytes: network?.max_response_bytes }, secrets: secretGrants } : {} }); setGrantOpen(false) }) }}>
      <p className="text-sm">版本 #{currentRevision}。未选择的能力和服务器不予授权。</p><fieldset className="space-y-2"><legend className="font-semibold">能力</legend>{capabilities.map((cap: string) => <label className="block text-sm" key={cap}><input type="checkbox" checked={caps.includes(cap)} onChange={e => setCaps(e.target.checked ? [...caps, cap] : caps.filter(value => value !== cap))} /> {cap}</label>)}</fieldset>
      <fieldset className="space-y-2"><legend className="font-semibold">服务器范围</legend>{servers.map(server => <label className="block text-sm" key={server.id}><input type="checkbox" checked={serverIDs.includes(server.id)} onChange={e => setServerIDs(e.target.checked ? [...serverIDs, server.id] : serverIDs.filter(value => value !== server.id))} /> {server.name}</label>)}</fieldset>
      {caps.includes('network.request') && <fieldset className="space-y-2"><legend className="font-semibold">联网与密钥范围</legend><p className="text-xs text-muted-foreground">仅授予选中的公开 HTTPS 目标、请求方法和密钥；密钥由宿主注入，不交给插件源码。</p>
        {network?.allowed_origins.map(origin => <label className="block break-all text-sm" key={origin}><input type="checkbox" checked={origins.includes(origin)} onChange={e => setOrigins(e.target.checked ? [...origins, origin] : origins.filter(value => value !== origin))} /> {origin}</label>)}
        {network?.allowed_methods.map(method => <label className="block text-sm" key={method}><input type="checkbox" checked={methods.includes(method)} onChange={e => setMethods(e.target.checked ? [...methods, method] : methods.filter(value => value !== method))} /> {method}</label>)}
        {secrets.map(secret => <label className="block text-sm" key={secret.name}><input type="checkbox" checked={secretGrants.includes(secret.name)} onChange={e => setSecretGrants(e.target.checked ? [...secretGrants, secret.name] : secretGrants.filter(value => value !== secret.name))} /> 密钥：{secret.purpose || secret.name} ({secret.name})</label>)}
        {!network && <p role="alert" className="text-sm text-destructive">该版本未声明联网目标，无法授予联网权限。</p>}
      </fieldset>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}<Button type="submit" disabled={busy || !caps.length || caps.includes('network.request') && (!origins.length || !methods.length)}>确认授权</Button>
    </form></Dialog>
    <Dialog isOpen={!!activation} onClose={() => { if (!busy) setActivation(null) }} title="确认激活版本"><div className="space-y-4"><p className="text-sm">切换到 {activation?.version}。新执行使用该版本，旧执行保留原快照。授权不会从其他版本继承；主控会校验现有配置，但不会回退插件私有数据，请确认目标版本兼容。</p>{error && <p role="alert" className="text-destructive text-sm">{error}</p>}<Button disabled={busy} onClick={() => void perform(async () => { await api.activateVersion(request, item.id, activation!.revision_id); setActivation(null) })}>确认激活</Button></div></Dialog>
    <Dialog isOpen={uninstallOpen} onClose={() => { if (!busy) setUninstallOpen(false) }} title="卸载插件"><div className="space-y-4"><p className="text-sm">卸载会停止新执行和触发器。已下发的节点动作不能撤回。</p><label className="block text-sm"><input type="checkbox" checked={retainData} onChange={e => setRetainData(e.target.checked)} /> 保留插件配置、私有状态与密钥</label>{!retainData && <p className="text-destructive text-sm">插件私有数据将被删除，不能通过重新安装恢复。</p>}{error && <p role="alert" className="text-destructive text-sm">{error}</p>}<Button disabled={busy} onClick={async () => { setBusy(true); setError(''); try { await api.uninstallPlugin(request, item.id, retainData); onChanged(); onClose() } catch (e) { setError(e instanceof Error ? e.message : '卸载失败') } finally { setBusy(false) } }}>确认卸载</Button></div></Dialog>
    {isAdmin && webhookOpen && <PluginWebhookDialog pluginID={item.id} isAdmin={isAdmin} request={request} onClose={() => setWebhookOpen(false)} />}
    {runID && <PluginRunDialog id={runID} request={request} onClose={() => setRunID(null)} />}
  </>
}
