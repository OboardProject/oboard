import './plugins.css'
import React, { useCallback, useEffect, useState } from 'react'
import { Code2, Cpu, PackagePlus, RefreshCw, Settings2 } from 'lucide-react'
import { Badge } from '../../components/ui/badge'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import { Input } from '../../components/ui/input'
import { SignalEmptyState } from '../../components/ui/SignalEmptyState'
import { Switch } from '../../components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../../components/ui/tabs'
import { useRegisterPageRefresh } from '../../page-refresh-context'
import { canAuthorizePlugins, hasManagementAccess } from '../../permissions'
import * as api from './api'
import { InstanceDialog } from './InstanceDialog'
import { PackageInstallDialog } from './PackageInstallDialog'
import { PluginDetail } from './PluginDetail'
import { PluginEditor } from './PluginEditor'
import { RunDialog } from './RunDialog'
import {
  describeError, errorMessage, formatTime, instanceStatusLabels, instanceStatusTone, publisherLabel, runStatusLabels, runTone, triggerLabels,
} from './domain'
import type { Catalog, Installation, InstallationDetail, NotificationChannelOption, PluginsWorkspaceProps, Run, RuntimeStatus, ServerOption } from './types'

type EditorState = { pluginID?: number; manifest?: string; source?: string } | null

export function PluginsWorkspace({ tab, data, client, notify, onNavigate }: PluginsWorkspaceProps) {
  const request = client.requestV2
  const role = data?.session?.role || data?.current_user?.role
  const isAdmin = canAuthorizePlugins(role)
  const canOperate = hasManagementAccess(role)
  const channels: NotificationChannelOption[] = data?.notification_channels || []
  const [plugins, setPlugins] = useState<Installation[]>([])
  const [runs, setRuns] = useState<Run[]>([])
  const [runtime, setRuntime] = useState<RuntimeStatus | null>(null)
  const [catalog, setCatalog] = useState<Catalog | null>(null)
  const [servers, setServers] = useState<ServerOption[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState('')
  const [busy, setBusy] = useState(false)
  const [detail, setDetail] = useState<InstallationDetail | null>(null)
  const [instanceID, setInstanceID] = useState<number | null>(null)
  const [runID, setRunID] = useState<number | null>(null)
  const [installOpen, setInstallOpen] = useState(false)
  const [runtimeOpen, setRuntimeOpen] = useState(false)
  const [editor, setEditor] = useState<EditorState>(null)

  const refresh = useCallback(async () => {
    setLoading(true)
    try {
      const [list, status, runPage] = await Promise.all([
        api.listInstallations(request),
        api.runtimeStatus(request),
        tab === 'plugin-runs' ? api.listRuns(request, { limit: 100 }) : Promise.resolve({ runs: [] as Run[] }),
      ])
      setPlugins(list.plugins || [])
      setRuntime(status)
      setRuns(runPage.runs || [])
      setLoadError('')
    } catch (e) {
      setLoadError(errorMessage(e, '加载插件失败'))
    } finally {
      setLoading(false)
    }
  }, [request, tab])

  useEffect(() => { void refresh() }, [refresh])
  useEffect(() => {
    void api.catalog(request).then(setCatalog, () => undefined)
    void api.serverOptions(request).then(page => setServers(page.servers || []), () => undefined)
  }, [request])
  useRegisterPageRefresh(() => refresh())

  const openDetail = async (id: number) => {
    try { setDetail(await api.getInstallation(request, id)) } catch (e) { notify(errorMessage(e, '读取插件失败'), 'error') }
  }
  const reloadDetail = async () => {
    if (detail) await openDetail(detail.id)
    void refresh()
  }
  const act = async (action: () => Promise<void>, success?: string) => {
    setBusy(true)
    try { await action(); if (success) notify(success, 'success') } catch (e) { notify(errorMessage(e, '操作失败'), 'error') } finally { setBusy(false) }
  }

  const view = tab === 'plugin-runs' ? 'runs' : 'library'
  const pluginName = (installationID: number) => plugins.find(item => item.id === installationID)?.name || `插件 #${installationID}`

  return <div className="plugins-workspace">
    <div className="plugins-head">
      <div>
        <h2>插件</h2>
        <p>插件只能使用授权的结构化能力，由 OBoard 代为执行，不能运行命令。</p>
      </div>
      <div className="plugins-head-actions">
        <Button variant="ghost" size="icon" aria-label="刷新" disabled={loading} onClick={() => void refresh()}><RefreshCw size={16} /></Button>
        {canOperate && <Button variant="outline" onClick={() => setEditor({})}><Code2 size={15} />新建插件</Button>}
        {isAdmin && <Button onClick={() => setInstallOpen(true)}><PackagePlus size={15} />安装插件</Button>}
      </div>
    </div>

    <RuntimeCard runtime={runtime} isAdmin={isAdmin} busy={busy} notify={notify}
      onToggle={enabled => void act(async () => setRuntime(await api.updateRuntime(request, { enabled })), enabled ? '已启用插件执行' : '已关闭插件执行')}
      onSettings={() => setRuntimeOpen(true)} />

    <Tabs value={view} onValueChange={value => onNavigate(value === 'runs' ? 'plugin-runs' : 'plugins')}>
      <TabsList>
        <TabsTrigger value="library">已安装</TabsTrigger>
        <TabsTrigger value="runs">执行记录</TabsTrigger>
      </TabsList>
      {loadError && <p role="alert" className="plugin-field-error">{loadError}</p>}
      <TabsContent value="library">
        {loading && !plugins.length ? <p className="plugin-env-help p-4" role="status">正在加载插件…</p>
          : plugins.length === 0 ? <SignalEmptyState title="还没有插件" description="从 .obplugin 包或 GitHub 仓库安装，或在编辑器中新建。安装后需要配置并由管理员授权才能运行。" />
          : <div className="plugins-grid">
            {plugins.map(item => <button key={item.id} type="button" className="plugin-card" onClick={() => void openDetail(item.id)}>
              <div className="plugin-card-head">
                {item.icon ? <img src={item.icon} alt="" className="plugin-icon" /> : <span className="plugin-icon plugin-icon-fallback" aria-hidden="true"><Cpu size={18} /></span>}
                <div className="min-w-0">
                  <strong>{item.name}</strong>
                  <p className="plugin-env-help">{item.version || '未发布'} · {publisherLabel(item.publisher_identity, item.publisher_name)}</p>
                </div>
                <Badge variant={item.enabled ? 'success' : 'secondary'}>{item.enabled ? '已启用' : item.published ? '已停用' : '草稿'}</Badge>
              </div>
              {item.description && <p className="plugin-card-desc">{item.description}</p>}
              <div className="plugin-card-instances">
                {item.instances.length === 0 ? <span className="plugin-env-help">暂无实例</span> : item.instances.slice(0, 4).map(instance =>
                  <span key={instance.id} className="plugin-instance-chip"><span>{instance.name}</span><Badge variant={instanceStatusTone(instance.status)}>{instanceStatusLabels[instance.status] || instance.status}</Badge></span>)}
                {item.instances.length > 4 && <span className="plugin-env-help">等 {item.instances.length} 个实例</span>}
              </div>
            </button>)}
          </div>}
      </TabsContent>
      <TabsContent value="runs">
        {loading && !runs.length ? <p className="plugin-env-help p-4" role="status">正在加载执行记录…</p>
          : runs.length === 0 ? <SignalEmptyState title="还没有执行记录" description="手动运行或计划触发后，执行结果与日志会出现在这里。" />
          : <ul className="plugin-record-list">
            {runs.map(run => <li key={run.id}><button type="button" className="plugin-record-row plugin-record-button" onClick={() => setRunID(run.id)}>
              <div className="min-w-0">
                <strong>{pluginName(run.installation_id)} · #{run.id}</strong>
                <p className="plugin-env-help">{triggerLabels[run.trigger] || run.trigger} · {formatTime(run.queued_at)}{run.error_code ? ` · ${describeError(run.error_code)}` : ''}</p>
              </div>
              <Badge variant={runTone(run.status)}>{runStatusLabels[run.status] || run.status}</Badge>
            </button></li>)}
          </ul>}
      </TabsContent>
    </Tabs>

    {detail && !instanceID && !editor && <PluginDetail detail={detail} isAdmin={isAdmin} canConfigure={canOperate} busy={busy} onClose={() => setDetail(null)}
      onOpenInstance={setInstanceID}
      onCreateInstance={name => void act(async () => { const created = await api.createInstance(request, detail.id, name); await reloadDetail(); setInstanceID(created.id) }, '已创建实例，请完成配置与授权')}
      onToggleEnabled={() => void act(async () => { setDetail(await api.setInstallationEnabled(request, detail.id, !detail.enabled)); void refresh() }, detail.enabled ? '已停用插件' : '已启用插件')}
      onUninstall={() => void act(async () => { await api.uninstall(request, detail.id); setDetail(null); void refresh() }, '已卸载插件')}
      onActivate={packageID => void act(async () => { await api.activateVersion(request, detail.id, packageID); await reloadDetail() }, '已切换版本')}
      onEdit={() => setEditor({ pluginID: detail.id, manifest: detail.draft?.manifest || (detail.manifest ? JSON.stringify(detail.manifest, null, 2) : undefined), source: detail.draft?.source ?? detail.source })} />}

    {detail && instanceID && <InstanceDialog key={instanceID} instanceID={instanceID} installation={detail} request={request} servers={servers} channels={channels}
      customTypes={catalog?.custom_types || []} canConfigure={canOperate} canExecute={canOperate} canAuthorize={isAdmin} notify={notify}
      onChanged={() => void reloadDetail()} onOpenRun={setRunID} onClose={() => setInstanceID(null)} />}

    {runID && <RunDialog key={runID} id={runID} request={request} canCancel={canOperate} onClose={() => setRunID(null)} />}

    {installOpen && <PackageInstallDialog request={request} onClose={() => setInstallOpen(false)} onInstalled={(id, reviewRequired) => {
      setInstallOpen(false)
      notify(reviewRequired ? '已安装新版本，新增权限需要重新审核授权' : '安装完成，请创建实例并完成配置与授权', 'success')
      void refresh()
      void openDetail(id)
    }} />}

    {editor && <PluginEditor pluginID={editor.pluginID} initialManifest={editor.manifest} initialSource={editor.source} request={request} servers={servers}
      customTypes={catalog?.custom_types || []} canPublish={isAdmin} notify={notify}
      onSaved={() => void refresh()}
      onPublished={(id, reviewRequired) => { setEditor(null); if (reviewRequired) notify('新版本申请了新权限，实例需重新审核授权', 'warning'); void refresh(); void openDetail(id) }}
      onClose={() => { setEditor(null); if (detail) void reloadDetail(); else void refresh() }} />}

    {runtimeOpen && runtime && <RuntimeSettingsDialog runtime={runtime} busy={busy} onClose={() => setRuntimeOpen(false)}
      onSave={input => void act(async () => { setRuntime(await api.updateRuntime(request, input)); setRuntimeOpen(false) }, '运行策略已保存')} />}
  </div>
}

function RuntimeCard({ runtime, isAdmin, busy, notify, onToggle, onSettings }: {
  runtime: RuntimeStatus | null; isAdmin: boolean; busy: boolean
  notify: PluginsWorkspaceProps['notify']; onToggle: (enabled: boolean) => void; onSettings: () => void
}) {
  const [commandOpen, setCommandOpen] = useState(false)
  if (!runtime) return null
  const problems = [
    !runtime.runtime_installed && '未安装运行环境',
    runtime.runtime_installed && !runtime.worker_connected && '执行服务未连接',
    runtime.runtime_installed && !runtime.isolation_available && `隔离不可用${runtime.isolation_reason ? `：${runtime.isolation_reason}` : ''}`,
  ].filter(Boolean) as string[]
  return <div className="plugin-runtime" data-state={problems.length ? 'warning' : runtime.enabled ? 'ok' : 'off'}>
    <div className="plugin-runtime-main">
      <strong>运行环境</strong>
      <span>{runtime.enabled ? '执行已启用' : '执行已关闭'}{runtime.scheduler_paused ? ' · 计划已暂停' : ''}</span>
      <span className="plugin-env-help">运行中 {runtime.active_runs} · 排队 {runtime.queued_runs} · 并发上限 {runtime.max_concurrency} · 单次最长 {runtime.max_timeout_seconds}s</span>
      {problems.length > 0 && <span className="plugin-field-error">{problems.join(' · ')}</span>}
    </div>
    {isAdmin && <div className="plugin-runtime-actions">
      {!runtime.runtime_installed && <Button size="sm" variant="outline" onClick={() => setCommandOpen(true)}>查看安装命令</Button>}
      <Button size="sm" variant="ghost" aria-label="运行策略" onClick={onSettings}><Settings2 size={15} /></Button>
      <Switch checked={runtime.enabled} disabled={busy || !runtime.runtime_installed} ariaLabel="启用插件执行" onChange={onToggle} />
    </div>}
    <Dialog isOpen={commandOpen} onClose={() => setCommandOpen(false)} title="安装插件运行环境" size="lg"
      footer={<><Button variant="ghost" onClick={() => setCommandOpen(false)}>关闭</Button><Button onClick={async () => {
        try { await navigator.clipboard.writeText(runtime.install_command || ''); notify('已复制安装命令', 'success') } catch { notify('复制失败，请手动选择命令', 'warning') }
      }}>复制命令</Button></>}>
      <p className="plugin-env-help">在主控主机上以 root 执行。安装不会自动开启插件执行。</p>
      <pre className="plugin-pre">{runtime.install_command || '暂无安装命令，请刷新后重试。'}</pre>
    </Dialog>
  </div>
}

function RuntimeSettingsDialog({ runtime, busy, onClose, onSave }: {
  runtime: RuntimeStatus; busy: boolean; onClose: () => void
  onSave: (input: { scheduler_paused: boolean; max_concurrency: number; max_timeout_seconds: number; log_retention_days: number; run_retention_days: number }) => void
}) {
  const [draft, setDraft] = useState({
    scheduler_paused: runtime.scheduler_paused, max_concurrency: runtime.max_concurrency, max_timeout_seconds: runtime.max_timeout_seconds,
    log_retention_days: runtime.log_retention_days, run_retention_days: runtime.run_retention_days,
  })
  const number = (key: keyof typeof draft, label: string, min: number, max: number) =>
    <label className="plugin-inline-field">{label}<Input type="number" min={min} max={max} value={String(draft[key])} onChange={event => setDraft({ ...draft, [key]: Number(event.target.value) })} /></label>
  return <Dialog isOpen onClose={onClose} title="插件运行策略" size="default"
    footer={<><Button variant="ghost" onClick={onClose}>取消</Button><Button busy={busy} onClick={() => onSave(draft)}>保存</Button></>}>
    <div className="plugin-form-grid">
      <label className="plugin-switch-row"><span>暂停所有计划触发</span><Switch checked={draft.scheduler_paused} onChange={value => setDraft({ ...draft, scheduler_paused: value })} ariaLabel="暂停所有计划触发" /></label>
      {number('max_concurrency', '全局并发上限', 1, 8)}
      {number('max_timeout_seconds', '单次执行最长秒数', 5, 120)}
      {number('log_retention_days', '日志保留天数', 1, 365)}
      {number('run_retention_days', '执行记录保留天数', 1, 365)}
    </div>
  </Dialog>
}
