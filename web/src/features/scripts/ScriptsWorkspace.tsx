import React, { useEffect, useMemo, useState } from 'react'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import { Input } from '../../components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../../components/ui/tabs'
import { useRegisterPageRefresh } from '../../page-refresh-context'
import { canManageAdministratorAccounts } from '../../permissions'
import * as api from './api'
import type { Script, ScriptRevision, ScriptRun, ScriptTrigger, ScriptsWorkspaceProps } from './types'

function newIdempotency(prefix: string) {
  return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2, 8)}`
}

export function ScriptsWorkspace({ tab, data, client, notify, onNavigate }: ScriptsWorkspaceProps) {
  const requestV2 = client.requestV2
  const role = data?.session?.role || data?.current_user?.role
  const isAdmin = canManageAdministratorAccounts(role)
  const servers: Array<{ id: number; name: string }> = data?.servers || []
  const [scripts, setScripts] = useState<Script[]>([])
  const [triggers, setTriggers] = useState<ScriptTrigger[]>([])
  const [runs, setRuns] = useState<ScriptRun[]>([])
  const [runtime, setRuntime] = useState<any>(null)
  const [selected, setSelected] = useState<Script | null>(null)
  const [detail, setDetail] = useState<any>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [editorOpen, setEditorOpen] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [source, setSource] = useState(api.defaultSource)
  const [manifestText, setManifestText] = useState(JSON.stringify(api.defaultManifest, null, 2))
  const [paramsText, setParamsText] = useState('{}')
  const [grantCaps, setGrantCaps] = useState('servers.status')
  const [grantServers, setGrantServers] = useState('')
  const [loading, setLoading] = useState(false)
  const [runtimeLoading, setRuntimeLoading] = useState(true)
  const [runtimeError, setRuntimeError] = useState('')
  const [installOpen, setInstallOpen] = useState(false)

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
    setLoading(true)
    try {
      const [listed, trig] = await Promise.all([
        api.listScripts(requestV2),
        api.listTriggers(requestV2),
      ])
      const scriptItems = listed.scripts || []
      setScripts(scriptItems)
      setTriggers(trig.triggers || [])
      const pages = await Promise.all(scriptItems.slice(0, 8).map(item => requestV2(`/scripts/${item.id}/runs?limit=8`)))
      const recent = pages.flatMap(page => page.runs || [])
      setRuns(recent.sort((a, b) => String(b.created_at).localeCompare(String(a.created_at))))
    } catch (error: any) {
      notify(error.message || '加载脚本失败', 'error')
    } finally {
      setLoading(false)
    }
  }

  const refresh = async () => {
    await Promise.all([refreshRuntime(), refreshWorkspace()])
  }

  useEffect(() => { void refreshRuntime() }, [])
  useEffect(() => { void refreshWorkspace() }, [tab])
  useRegisterPageRefresh(() => refresh())

  const openEditor = async (item: Script) => {
    try {
      const next = await api.getScript(requestV2, item.id)
      setSelected(item)
      setDetail(next)
      setSource(next.draft?.source || next.published?.source || api.defaultSource)
      setManifestText(JSON.stringify(next.draft?.manifest || next.published?.manifest || api.defaultManifest, null, 2))
      setEditorOpen(true)
    } catch (error: any) {
      notify(error.message || '读取脚本失败', 'error')
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
    if (!selected) return
    const revision = await api.saveDraft(requestV2, selected.id, source, parseManifest())
    notify('草稿已保存', 'success')
    const next = await api.getScript(requestV2, selected.id)
    setDetail(next)
    return revision.revision
  }

  const view = tab === 'script-triggers' ? 'triggers' : tab === 'script-runs' ? 'runs' : 'library'
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
          <h2 className="text-lg font-semibold">脚本</h2>
          <p className="text-sm text-muted-foreground">受限 JavaScript 自动化，不是 Node.js，也不是远程 Shell。</p>
        </div>
        <div className="flex gap-2">
          <Button variant="ghost" onClick={() => void refresh()} disabled={loading || runtimeLoading}>刷新</Button>
          {view === 'library' && <Button onClick={() => { setName(''); setDescription(''); setCreateOpen(true) }}>新建脚本</Button>}
          {view === 'triggers' && <Button onClick={async () => {
            const published = scripts[0]
            if (!published) { notify('先创建并发布一个脚本', 'warning'); return }
            try {
              const detail = await api.getScript(requestV2, published.id)
              if (!detail.published) { notify('先发布脚本版本', 'warning'); return }
              await api.createTrigger(requestV2, {
                script_id: published.id,
                revision_id: detail.published.id,
                name: `${published.name}-offline`,
                kind: 'event',
                spec: { event: 'server.offline', timezone: 'UTC', sustain_seconds: 0, subject_server_ids: [] },
                enabled: false,
              })
              notify('已创建关闭状态的离线触发器', 'success')
              void refresh()
            } catch (error: any) { notify(error.message, 'error') }
          }}>新建离线触发器</Button>}
        </div>
      </div>
      <Tabs value={view} onValueChange={(value) => onNavigate(value === 'triggers' ? 'script-triggers' : value === 'runs' ? 'script-runs' : 'scripts')}>
        <TabsList>
          <TabsTrigger value="library">脚本库</TabsTrigger>
          <TabsTrigger value="triggers">触发器</TabsTrigger>
          <TabsTrigger value="runs">执行记录</TabsTrigger>
        </TabsList>
        <div className="script-runtime-status">
          <strong>运行环境</strong>
          {runtimeLoading && !runtime ? (
            <p>正在检查运行环境…</p>
          ) : runtimeError && !runtime ? (
            <p>{runtimeError}</p>
          ) : runtime?.runtime_installed ? (
            <p>Worker {runtime.worker_connected ? '已连接' : '未连接'} · 沙箱 {runtime.isolation_available ? runtime.isolation_mode : (runtime.isolation_reason || '不可用')} · 执行 {runtime.enabled ? '已启用' : '已关闭'}</p>
          ) : (
            <p>未安装。默认安装主控时不带脚本运行环境；在主控主机上执行安装命令后再启用执行。</p>
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
                    notify(runtime.enabled ? '已关闭脚本执行' : '已启用脚本执行', 'success')
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
          <div className="rounded-2xl border border-border/70 bg-card/70">
            {scripts.length === 0 ? <p className="p-6 text-sm text-muted-foreground">还没有脚本。先创建草稿，再发布并授权。</p> : scripts.map(item => (
              <button key={item.id} className="script-library-row" onClick={() => void openEditor(item)}>
                <div>
                  <strong>{item.name}</strong>
                  <div className="text-xs text-muted-foreground">{item.status} · {item.description || '无说明'}</div>
                </div>
                <span className="text-xs text-muted-foreground">#{item.id}</span>
              </button>
            ))}
          </div>
        </TabsContent>
        <TabsContent value="triggers">
          <div className="rounded-2xl border border-border/70 bg-card/70">
            {triggers.length === 0 ? <p className="p-6 text-sm text-muted-foreground">没有触发器。触发器绑定固定版本，不会自动追随最新发布。</p> : triggers.map(item => (
              <div key={item.id} className="flex items-center justify-between border-b border-border/50 px-4 py-3 last:border-0">
                <div>
                  <strong>{item.name}</strong>
                  <div className="text-xs text-muted-foreground">{item.kind} · 脚本 #{item.script_id} · 版本 #{item.revision_id} · {item.enabled ? '已启用' : '已关闭'}</div>
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
          <div className="rounded-2xl border border-border/70 bg-card/70">
            {runs.length === 0 ? <p className="p-6 text-sm text-muted-foreground">还没有执行记录。</p> : runs.map(item => (
              <div key={item.id} className="border-b border-border/50 px-4 py-3 last:border-0">
                <div className="flex items-center justify-between">
                  <strong>{item.uuid}</strong>
                  <span className="text-xs">{item.status}{item.mode === 'simulate' ? ' · 模拟' : ''}</span>
                </div>
                <div className="text-xs text-muted-foreground">{item.trigger_kind || 'manual'} · 脚本 #{item.script_id} · {item.error_code || item.skip_reason || '无错误'}</div>
              </div>
            ))}
          </div>
        </TabsContent>
      </Tabs>

      <Dialog isOpen={installOpen} onClose={() => setInstallOpen(false)} title="安装脚本运行环境" size="lg">
        <div className="space-y-3 text-sm">
          <p className="text-muted-foreground">在主控主机上以 root 执行下面的命令。安装完成后回到本页刷新，再打开「启用执行」。安装不会自动开启脚本。</p>
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

      <Dialog isOpen={createOpen} onClose={() => setCreateOpen(false)} title="新建脚本" size="lg">
        <div className="space-y-3">
          <label className="block text-sm">名称<Input value={name} onChange={event => setName(event.target.value)} /></label>
          <label className="block text-sm">说明<Input value={description} onChange={event => setDescription(event.target.value)} /></label>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setCreateOpen(false)}>取消</Button>
            <Button onClick={async () => {
              try {
                const created = await api.createScript(requestV2, { name, description })
                notify('已创建草稿', 'success')
                setCreateOpen(false)
                await refresh()
                void openEditor(created.script)
              } catch (error: any) {
                notify(error.message || '创建失败', 'error')
              }
            }}>创建</Button>
          </div>
        </div>
      </Dialog>

      <Dialog isOpen={editorOpen} onClose={() => setEditorOpen(false)} title={selected ? `编辑 ${selected.name}` : '编辑脚本'} size="xl">
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
          <div className="flex flex-wrap gap-2">
            <Button size="sm" onClick={async () => { try { await saveCurrentDraft() } catch (error: any) { notify(error.message, 'error') } }}>保存草稿</Button>
            <Button size="sm" variant="outline" onClick={async () => {
              try {
                const result = await api.validateScript(requestV2, selected!.id, source, parseManifest(), JSON.parse(paramsText || '{}'))
                notify(result.valid === false ? '校验未通过' : '校验通过', result.valid === false ? 'warning' : 'success')
              } catch (error: any) { notify(error.message, 'error') }
            }}>校验</Button>
            <Button size="sm" variant="outline" onClick={async () => {
              try {
                const draft = await saveCurrentDraft()
                await api.simulateScript(requestV2, selected!.id, draft!.id, JSON.parse(paramsText || '{}'), {}, newIdempotency('sim'))
                notify('已提交模拟运行', 'success')
              } catch (error: any) { notify(error.message, 'error') }
            }}>模拟运行</Button>
            <Button size="sm" variant="outline" onClick={async () => {
              try {
                const published = detail?.published as ScriptRevision | undefined
                if (!published) throw new Error('需要先发布版本')
                await api.runScript(requestV2, selected!.id, published.id, JSON.parse(paramsText || '{}'), {}, newIdempotency('run'))
                notify('已提交正式运行', 'success')
              } catch (error: any) { notify(error.message, 'error') }
            }}>正式运行</Button>
            <Button size="sm" onClick={async () => {
              try {
                const draft = await saveCurrentDraft()
                await api.publishRevision(requestV2, selected!.id, draft!.id)
                notify('已发布新版本，旧授权不会自动继承', 'success')
                setDetail(await api.getScript(requestV2, selected!.id))
              } catch (error: any) { notify(error.message, 'error') }
            }}>发布版本</Button>
          </div>
          {isAdmin && selected && (
            <div className="rounded-xl border border-border/60 p-3 space-y-2">
              <strong className="text-sm">管理员授权</strong>
              <Input value={grantCaps} onChange={event => setGrantCaps(event.target.value)} placeholder="能力，逗号分隔" />
              <Input value={grantServers} onChange={event => setGrantServers(event.target.value)} placeholder="服务器 ID，逗号分隔" />
              <div className="text-xs text-muted-foreground">可选服务器：{servers.map(item => `${item.name}#${item.id}`).join('、') || '无'}</div>
              <Button size="sm" onClick={async () => {
                try {
                  const published = detail?.published
                  if (!published) throw new Error('先发布再授权')
                  const ids = grantServers.split(',').map(item => Number(item.trim())).filter(id => id > 0)
                  await api.createGrant(requestV2, {
                    script_id: selected.id,
                    revision_id: published.id,
                    capabilities: grantCaps.split(',').map(item => item.trim()).filter(Boolean),
                    resource_scope: { servers: { mode: 'selected', ids } },
                    constraints: {},
                  })
                  notify('已批准该版本授权', 'success')
                } catch (error: any) { notify(error.message, 'error') }
              }}>批准当前发布版本</Button>
            </div>
          )}
        </div>
      </Dialog>
    </div>
  )
}
