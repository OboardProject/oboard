import './signal-plugins.css'
import React, { useEffect, useMemo, useState } from 'react'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import { Input } from '../../components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../../components/ui/tabs'
import { useRegisterPageRefresh } from '../../page-refresh-context'
import { canManageAdministratorAccounts } from '../../permissions'
import * as api from './api'
import { PackageInstallDialog } from './PackageInstallDialog'
import { PluginDetails } from './PluginDetails'
import { PluginRunDialog } from './PluginRunDialog'
import { PluginTriggerDialog } from './PluginTriggerDialog'
import { runStatusLabel } from './PluginPageRenderer'
import type { Plugin, PluginRevision, PluginRun, PluginTrigger, PluginsWorkspaceProps } from './types'

function newIdempotency(prefix: string) {
  return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2, 8)}`
}

export function PluginsWorkspace({ tab, data, client, notify, onNavigate }: PluginsWorkspaceProps) {
  const requestV2 = client.requestV2
  const role = data?.session?.role || data?.current_user?.role
  const isAdmin = canManageAdministratorAccounts(role)
  const servers: Array<{ id: number; name: string }> = data?.servers || []
  const [plugins, setPlugins] = useState<Plugin[]>([])
  const [triggers, setTriggers] = useState<PluginTrigger[]>([])
  const [runs, setRuns] = useState<PluginRun[]>([])
  const [runtime, setRuntime] = useState<any>(null)
  const [selected, setSelected] = useState<Plugin | null>(null)
  const [detail, setDetail] = useState<any>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [editorOpen, setEditorOpen] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [source, setSource] = useState(api.defaultSource)
  const [manifestText, setManifestText] = useState(JSON.stringify(api.defaultManifest, null, 2))
  const [paramsText, setParamsText] = useState('{}')
  const [packageOpen, setPackageOpen] = useState(false)
  const [detailsPlugin, setDetailsPlugin] = useState<Plugin | null>(null)
  const [runID, setRunID] = useState<number | null>(null)
  const [loading, setLoading] = useState(false)
  const [runtimeLoading, setRuntimeLoading] = useState(true)
  const [runtimeError, setRuntimeError] = useState('')
  const [installOpen, setInstallOpen] = useState(false)
  const [triggerOpen, setTriggerOpen] = useState(false)
  const [editorBusy, setEditorBusy] = useState(false)
  const [editorStatus, setEditorStatus] = useState('')
  const editorSession = React.useRef(0)
  const editorOperation = React.useRef(false)
  const workspaceRead = React.useRef(0)

  const refreshRuntime = async () => {
    setRuntimeLoading(true)
    try {
      const status = await api.runtimeStatus(requestV2)
      setRuntime(status.status || null)
      setRuntimeError('')
    } catch (error: any) {
      setRuntimeError(error.message || '读取运行环境失败')
    } finally {
      setRuntimeLoading(false)
    }
  }

  const refreshWorkspace = async () => {
    const read = ++workspaceRead.current
    setLoading(true)
    try {
      const [listed, trig] = await Promise.all([
        api.listPlugins(requestV2),
        api.listTriggers(requestV2),
      ])
      if (read !== workspaceRead.current) return
      const pluginItems = listed.plugins || []
      setPlugins(pluginItems)
      setTriggers(trig.triggers || [])
      if (tab === 'plugin-runs') {
        const pages = await Promise.all(pluginItems.slice(0, 8).map(item => requestV2(`/plugins/${item.id}/runs?limit=8`)))
        const recent = pages.flatMap(page => page.runs || [])
        if (read === workspaceRead.current) setRuns(recent.sort((a, b) => String(b.created_at).localeCompare(String(a.created_at))))
      }
    } catch (error: any) {
      if (read === workspaceRead.current) notify(error.message || '加载插件失败', 'error')
    } finally {
      if (read === workspaceRead.current) setLoading(false)
    }
  }

  const refresh = async () => {
    await Promise.all([refreshRuntime(), refreshWorkspace()])
  }

  useEffect(() => { void refreshRuntime() }, [])
  useEffect(() => { void refreshWorkspace() }, [tab])
  useRegisterPageRefresh(() => refresh())

  const openEditor = async (item: Plugin) => {
    const session = ++editorSession.current
    try {
      const next = await api.getPlugin(requestV2, item.id)
      if (session !== editorSession.current) return
      setEditorStatus('')
      setSelected(item)
      setDetail(next)
      setSource(next.draft?.source || next.published?.source || api.defaultSource)
      setManifestText(JSON.stringify(next.draft?.manifest || next.published?.manifest || api.defaultManifest, null, 2))
      setEditorOpen(true)
    } catch (error: any) {
      notify(error.message || '读取插件失败', 'error')
    }
  }

  const parseManifest = () => {
    try {
      return JSON.parse(manifestText)
    } catch {
      throw new Error('运行规范不是合法 JSON')
    }
  }

  const saveCurrentDraft = async () => {
    if (!selected) throw new Error('请先选择插件')
    const session = editorSession.current
    const revision = await api.saveDraft(requestV2, selected.id, source, parseManifest())
    if (session === editorSession.current) {
      setDetail((current: typeof detail) => ({ ...current, draft: revision.revision }))
      setEditorStatus('已保存提交的草稿内容')
    }
    return revision.revision
  }

  const runEditorOperation = async (operation: () => Promise<void>) => {
    if (editorOperation.current) return
    const session = editorSession.current
    editorOperation.current = true
    setEditorBusy(true)
    setEditorStatus('')
    try { await operation() } catch (error: unknown) {
      if (session === editorSession.current) setEditorStatus(error instanceof Error ? error.message : String(error))
    } finally { editorOperation.current = false; setEditorBusy(false) }
  }

  const closeEditor = () => { editorSession.current++; setEditorOpen(false) }

  const view = tab === 'plugin-triggers' ? 'triggers' : tab === 'plugin-runs' ? 'runs' : 'library'
  const selectedCapabilities = useMemo(() => {
    try {
      return (JSON.parse(manifestText).capabilities || []).join('、')
    } catch {
      return '无法解析'
    }
  }, [manifestText])

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold">插件</h2>
          <p className="text-sm text-muted-foreground">按授权执行自动化任务。</p>
        </div>
        <div className="flex gap-2">
          <Button variant="ghost" onClick={() => void refresh()} disabled={loading || runtimeLoading}>刷新</Button>
          {view === 'library' && <><Button variant="outline" onClick={() => { setName(''); setDescription(''); setCreateOpen(true) }}>新建开发草稿</Button><Button onClick={() => setPackageOpen(true)}>安装插件</Button></>}
          {view === 'triggers' && <Button onClick={() => setTriggerOpen(true)}>新建离线触发器</Button>}
        </div>
      </div>
      <Tabs value={view} onValueChange={(value) => onNavigate(value === 'triggers' ? 'plugin-triggers' : value === 'runs' ? 'plugin-runs' : 'plugins')}>
        <TabsList>
          <TabsTrigger value="library">插件库</TabsTrigger>
          <TabsTrigger value="triggers">触发器</TabsTrigger>
          <TabsTrigger value="runs">执行记录</TabsTrigger>
        </TabsList>
        <div className="plugin-runtime-status">
          <strong>运行环境</strong>
          {runtimeLoading && !runtime ? (
            <p>正在检查运行环境…</p>
          ) : runtimeError && !runtime ? (
            <p>{runtimeError}</p>
          ) : runtime?.runtime_installed ? (
            <><p>执行 {runtime.enabled ? '已启用' : '已关闭'}{!runtime.worker_connected ? ' · 执行服务未连接' : ''}{!runtime.isolation_available ? ' · 隔离环境不可用' : ''}</p><details><summary>运行环境详情</summary><p>执行服务：{runtime.worker_connected ? '已连接' : '未连接'} · 隔离环境：{runtime.isolation_available ? runtime.isolation_mode : (runtime.isolation_reason || '不可用')}</p></details></>
          ) : (
            <p>尚未安装插件运行环境，暂时不能执行插件。</p>
          )}
          {isAdmin && runtime && (
            <div className="mt-3 flex gap-2">
              {!runtime.runtime_installed && (
                <Button size="sm" onClick={() => setInstallOpen(true)}>安装运行环境</Button>
              )}
              {runtime.runtime_installed && (
                <Button size="sm" variant="outline" onClick={async () => {
                  try {
                    await api.updateRuntimeSettings(requestV2, { enabled: !runtime.enabled })
                    notify(runtime.enabled ? '已关闭插件执行' : '已启用插件执行', 'success')
                    void refreshRuntime()
                  } catch (error: any) {
                    notify(error.message || '更新运行环境失败', 'error')
                  }
                }}>{runtime.enabled ? '关闭执行' : '启用执行'}</Button>
              )}
            </div>
          )}
        </div>
        <TabsContent value="library">
          <div className="plugin-record-list">
            {loading && !plugins.length ? <p className="p-6 text-sm text-muted-foreground" role="status">正在加载插件…</p> : plugins.length === 0 ? <p className="p-6 text-sm text-muted-foreground">还没有插件。可从 GitHub 仓库或 ZIP 安装，审核授权后启用。</p> : plugins.map(item => (
              <button key={item.id} className="plugin-library-row" onClick={() => setDetailsPlugin(item)}>
                <div>
                  <strong>{item.name}</strong>
                  <div className="text-xs text-muted-foreground">{item.status === 'enabled' ? '已启用' : item.status === 'draft' ? '草稿' : item.status === 'archived' ? '已归档' : '已停用'} · {item.description || '无说明'}</div>
                </div>
                <span className="text-xs text-muted-foreground">#{item.id}</span>
              </button>
            ))}
          </div>
        </TabsContent>
        <TabsContent value="triggers">
          <div className="plugin-record-list">
            {loading && !triggers.length ? <p className="p-6 text-sm text-muted-foreground" role="status">正在加载触发器…</p> : triggers.length === 0 ? <p className="p-6 text-sm text-muted-foreground">没有触发器。触发器绑定固定版本，不会自动追随最新发布。</p> : triggers.map(item => (
              <div key={item.id} className="flex items-center justify-between border-b border-border/50 px-4 py-3 last:border-0">
                <div>
                  <strong>{item.name}</strong>
                  <div className="text-xs text-muted-foreground">{item.kind} · 插件 #{item.plugin_id} · 版本 #{item.revision_id} · {item.enabled ? '已启用' : '已关闭'}</div>
                </div>
                <Button size="sm" variant="outline" onClick={async () => {
                  try {
                    await api.updateTrigger(requestV2, item.id, { enabled: !item.enabled }, item.binding_revision)
                    notify(item.enabled ? '已暂停触发器' : '已启用触发器', 'success')
                    void refresh()
                  } catch (error: any) {
                    notify(error.message || '更新触发器失败', 'error')
                  }
                }}>{item.enabled ? '暂停' : '启用'}</Button>
              </div>
            ))}
          </div>
        </TabsContent>
        <TabsContent value="runs">
          <div className="plugin-record-list">
            {loading && !runs.length ? <p className="p-6 text-sm text-muted-foreground" role="status">正在加载执行记录…</p> : runs.length === 0 ? <p className="p-6 text-sm text-muted-foreground">还没有执行记录。</p> : runs.map(item => (
              <button key={item.id} className="plugin-library-row" onClick={() => setRunID(item.id)}>
                <span>执行 #{item.id} · 插件 #{item.plugin_id}<span className="block text-xs text-muted-foreground">{item.error_code || item.skip_reason || item.created_at}</span></span>
                <span className="text-xs">{runStatusLabel(item.status)}{item.mode === 'simulate' ? ' · 模拟' : ''}</span>
              </button>
            ))}
          </div>
        </TabsContent>
      </Tabs>

      {triggerOpen && <PluginTriggerDialog plugins={plugins} servers={servers} request={requestV2} onClose={() => setTriggerOpen(false)} onSaved={() => { setTriggerOpen(false); notify('已创建关闭状态的离线触发器', 'success'); void refreshWorkspace() }} />}
      {packageOpen && <PackageInstallDialog request={requestV2} onClose={() => setPackageOpen(false)} onInstalled={() => { setPackageOpen(false); notify('安装完成，请审核配置与授权；更新版本需单独激活', 'success'); void refreshWorkspace() }} />}
      {detailsPlugin && <PluginDetails key={detailsPlugin.id} plugin={detailsPlugin} request={requestV2} isAdmin={isAdmin} servers={servers} onClose={() => setDetailsPlugin(null)} onChanged={() => void refreshWorkspace()} onDevelop={() => { const item = detailsPlugin; setDetailsPlugin(null); void openEditor(item) }} />}
      {runID && <PluginRunDialog id={runID} request={requestV2} onClose={() => setRunID(null)} />}

      <Dialog isOpen={installOpen} onClose={() => setInstallOpen(false)} title="安装插件运行环境" size="lg">
        <div className="space-y-3 text-sm">
          <p className="text-muted-foreground">在主控主机上以 root 执行下面的命令。安装完成后回到本页刷新，再打开「启用执行」。安装不会自动开启插件。</p>
          <pre className="overflow-x-auto rounded-xl bg-secondary/50 p-3 font-mono text-xs whitespace-pre-wrap">{runtime?.install_command || ''}</pre>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setInstallOpen(false)}>关闭</Button>
            <Button onClick={async () => {
              const command = String(runtime?.install_command || '')
              if (!command) {
                notify('暂时没有安装命令，请刷新后重试', 'warning')
                return
              }
              try {
                await navigator.clipboard.writeText(command)
                notify('已复制安装命令', 'success')
              } catch {
                notify('复制失败，请手动选择命令', 'warning')
              }
            }}>复制命令</Button>
          </div>
        </div>
      </Dialog>

      <Dialog isOpen={createOpen} onClose={() => setCreateOpen(false)} title="新建插件" size="lg">
        <div className="space-y-3">
          <label className="block text-sm">名称<Input value={name} onChange={event => setName(event.target.value)} /></label>
          <label className="block text-sm">说明<Input value={description} onChange={event => setDescription(event.target.value)} /></label>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setCreateOpen(false)}>取消</Button>
            <Button onClick={async () => {
              try {
                const created = await api.createPlugin(requestV2, { name, description })
                notify('已创建草稿', 'success')
                setCreateOpen(false)
                await refresh()
                void openEditor(created.plugin)
              } catch (error: any) {
                notify(error.message || '创建失败', 'error')
              }
            }}>创建</Button>
          </div>
        </div>
      </Dialog>

      <Dialog isOpen={editorOpen} onClose={closeEditor} title={selected ? `编辑 ${selected.name}` : '编辑插件'} size="xl">
        <div className="space-y-4">
          <div className="rounded-xl bg-secondary/50 p-3 text-xs">
            草稿状态：{detail?.draft ? '有未发布草稿' : '无草稿'} · 已发布：{detail?.published ? `#${detail.published.id}` : '无'} · 当前能力：{selectedCapabilities}
          </div>
          <div className="grid gap-3 md:grid-cols-2">
            <label className="block text-sm">源码
              <textarea className="mt-1 min-h-56 w-full rounded-xl border border-border bg-background p-3 font-mono text-xs" value={source} onChange={event => setSource(event.target.value)} />
            </label>
            <label className="block text-sm">运行规范
              <textarea className="mt-1 min-h-56 w-full rounded-xl border border-border bg-background p-3 font-mono text-xs" value={manifestText} onChange={event => setManifestText(event.target.value)} />
            </label>
          </div>
          <label className="block text-sm">参数 JSON
            <Input value={paramsText} onChange={event => setParamsText(event.target.value)} />
          </label>
          {editorStatus && <p role="status" className="text-sm">{editorStatus}</p>}
          <div className="flex flex-wrap gap-2">
            <Button size="sm" disabled={editorBusy} onClick={() => void runEditorOperation(async () => { await saveCurrentDraft() })}>保存草稿</Button>
            <Button size="sm" variant="outline" disabled={editorBusy} onClick={() => void runEditorOperation(async () => {
              const session = editorSession.current
              const result = await api.validatePlugin(requestV2, selected!.id, source, parseManifest(), JSON.parse(paramsText || '{}'))
              if (session === editorSession.current) setEditorStatus(result.valid === false ? '校验未通过' : '校验通过')
            })}>校验</Button>
            <Button size="sm" variant="outline" disabled={editorBusy} onClick={() => void runEditorOperation(async () => {
              const draft = await saveCurrentDraft()
              await api.simulatePlugin(requestV2, selected!.id, draft.id, JSON.parse(paramsText || '{}'), {}, newIdempotency('sim'))
              notify('已提交模拟运行', 'success')
            })}>模拟运行</Button>
            <Button size="sm" variant="outline" disabled={editorBusy} onClick={() => void runEditorOperation(async () => {
              const published = detail?.published as PluginRevision | undefined
              if (!published) throw new Error('需要先发布版本')
              await api.runPlugin(requestV2, selected!.id, published.id, JSON.parse(paramsText || '{}'), {}, newIdempotency('run'))
              notify('已提交正式运行', 'success')
            })}>正式运行</Button>
            <Button size="sm" disabled={editorBusy} onClick={() => void runEditorOperation(async () => {
              const session = editorSession.current
              const draft = await saveCurrentDraft()
              const published = await api.publishRevision(requestV2, selected!.id, draft.id)
              notify('已发布新版本，旧授权不会自动继承', 'success')
              if (session === editorSession.current) setDetail((current: typeof detail) => ({ ...current, published: published.revision, draft: undefined }))
            })}>发布版本</Button>
          </div>
          <p className="text-xs text-muted-foreground">版本发布后，请在插件详情的「权限」中由管理员审核授权。</p>
        </div>
      </Dialog>
    </div>
  )
}
