import { useErrorToast } from "../../components/ui/use-error-toast"
import React, { useCallback, useEffect, useRef, useState } from 'react'
import { Play, RotateCcw } from 'lucide-react'
import { Badge } from '../../components/ui/badge'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import { Input } from '../../components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../../components/ui/tabs'
import { useSettingsEditorClose } from '../../components/settings/useSettingsEditorClose'
import * as api from './api'
import { EnvironmentForm } from './EnvironmentForm'
import { GrantEditor, grantMissingScopes, initialGrant } from './GrantEditor'
import { PageView } from './PageView'
import { ScheduleEditor } from './ScheduleEditor'
import {
  describeError, errorIssues, errorMessage, formatBytes, formatTime, instanceStatusLabels, instanceStatusTone,
  issuesByField, issuesForDraft, newIdempotencyKey, runStatusLabels, runTone, triggerLabels,
} from './domain'
import type {
  AuditEvent, CustomVar, EnvType, Grant, InstallationDetail, InstanceDetail, InstancePage,   NotificationChannelOption, PlanOption, RequestFn, Run, ServerOption, StateEntry, ToastTone, UserOption,
} from './types'

type View = 'config' | 'grant' | 'schedules' | 'runs' | 'state' | 'activity' | 'pages'

export interface InstanceDialogProps {
  instanceID: number
  installation: InstallationDetail
  request: RequestFn
  servers: ServerOption[]
  channels: NotificationChannelOption[]
  users: UserOption[]
  plans: PlanOption[]
  customTypes: EnvType[]
  canConfigure: boolean
  canExecute: boolean
  canAuthorize: boolean
  notify: (message: string, tone?: ToastTone) => void
  onChanged: () => void
  onOpenRun: (id: number) => void
  onClose: () => void
}

function serverIDsInValues(installation: InstallationDetail, values: Record<string, unknown>, custom: CustomVar[]) {
  const ids = new Set<string>()
  const add = (type: string, value: unknown) => {
    if (type === 'server' && typeof value === 'string') ids.add(value)
    if (type === 'servers' && Array.isArray(value)) value.forEach(item => ids.add(String(item)))
  }
  for (const field of installation.manifest?.environment || []) add(field.type, values[field.name])
  for (const item of custom) add(item.type, item.value)
  return [...ids]
}

function stableJSON(value: unknown): string {
  return JSON.stringify(value, (_key, item) => item && typeof item === 'object' && !Array.isArray(item)
    ? Object.fromEntries(Object.entries(item as Record<string, unknown>).filter(([, v]) => v !== undefined).sort(([a], [b]) => a.localeCompare(b)))
    : item)
}

export function InstanceDialog(props: InstanceDialogProps) {
  const { instanceID, installation, request, servers, channels, users, plans, customTypes, canConfigure, canExecute, canAuthorize, notify, onChanged, onOpenRun, onClose } = props
  const [instance, setInstance] = useState<InstanceDetail | null>(null)
  const [view, setView] = useState<View>('config')
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [custom, setCustom] = useState<CustomVar[]>([])
  const [issues, setIssues] = useState<Record<string, string>>({})
  const [validatedDraft, setValidatedDraft] = useState<{ values: Record<string, unknown>; custom: CustomVar[] }>({ values: {}, custom: [] })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useErrorToast(notify)
  const [runs, setRuns] = useState<Run[]>([])
  const [pages, setPages] = useState<InstancePage[]>([])
  const [pageID, setPageID] = useState('')
  const [state, setState] = useState<StateEntry[]>([])
  const [audit, setAudit] = useState<AuditEvent[]>([])
  const [secretName, setSecretName] = useState('')
  const [secretValue, setSecretValue] = useState('')
  const [nameDraft, setNameDraft] = useState('')
  const [grantDraft, setGrantDraft] = useState<Grant>({ capabilities: {} })
  const grantKey = useRef('')

  const applyInstance = useCallback((next: InstanceDetail, preserveDraft = false) => {
    setInstance(next)
    const key = `${next.grant?.revision || 0}-${next.grant?.package_id || 0}`
    if (key !== grantKey.current) {
      grantKey.current = key
      setGrantDraft(initialGrant(installation.permissions, next.grant))
    }
    setValidatedDraft({ values: next.values || {}, custom: next.custom || [] })
    if (preserveDraft) {
      setIssues(issuesByField(next.issues))
      return
    }
    setValues(next.values || {})
    setCustom(next.custom || [])
    setIssues(issuesByField(next.issues))
    setNameDraft(next.name)
  }, [installation.permissions])

  const load = useCallback(async () => {
    setError('')
    try { applyInstance(await api.getInstance(request, instanceID)); setError('') } catch (e) { setError(errorMessage(e, '读取实例失败')) }
  }, [applyInstance, instanceID, request])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    setError('')
    if (view === 'runs') void api.listInstanceRuns(request, instanceID).then(page => setRuns(page.runs || []), e => setError(errorMessage(e, '读取执行记录失败')))
    if (view === 'pages') void api.listPages(request, instanceID).then(page => {
      const items = page.pages || []
      setPages(items)
      setPageID(current => items.some(item => item.id === current) ? current : (items[0]?.id || ''))
      setError('')
    }, e => setError(errorMessage(e, '读取界面失败')))
    if (view === 'state') void api.listState(request, instanceID).then(page => setState(page.entries || []), e => setError(errorMessage(e, '读取状态失败')))
    if (view === 'activity') void api.listAudit(request, instanceID).then(page => setAudit(page.events || []), e => setError(errorMessage(e, '读取活动失败')))
  }, [view, instanceID, request])

  const reloadPages = async () => {
    const listed = await api.listPages(request, instanceID)
    const items = listed.pages || []
    setPages(items)
    setPageID(current => items.some(item => item.id === current) ? current : (items[0]?.id || ''))
  }

  const submitPageRun = async (queue: () => Promise<{ run: Run }>, success: string) => {
    setBusy(true)
    setError('')
    try {
      const result = await queue()
      notify(success, 'success')
      onOpenRun(result.run.id)
      setBusy(false)
      for (let attempt = 0; attempt < 8; attempt++) {
        await new Promise(resolve => window.setTimeout(resolve, 1000))
        const current = await api.getRun(request, result.run.id)
        if (current.run.status !== 'queued' && current.run.status !== 'running') {
          await reloadPages()
          return
        }
      }
    } catch (e) {
      setError(errorMessage(e, '操作失败'))
      setBusy(false)
    }
  }

  const act = async (action: () => Promise<void>, success?: string) => {
    setBusy(true)
    setError('')
    try {
      await action()
      if (success) notify(success, 'success')
      onChanged()
    } catch (e) {
      const fieldIssues = errorIssues(e)
      if (fieldIssues.length) {
        setIssues(issuesByField(fieldIssues))
        setValidatedDraft({ values, custom })
      }
      setError(errorMessage(e, '操作失败'))
    } finally {
      setBusy(false)
    }
  }

  const nameDirty = Boolean(instance) && canConfigure && nameDraft.trim() !== '' && nameDraft.trim() !== instance?.name
  const configDirty = Boolean(instance) && canConfigure && (
    stableJSON(values) !== stableJSON(instance?.values || {})
    || stableJSON(custom.filter(item => item.name)) !== stableJSON(instance?.custom || []))
  const grantDirty = Boolean(instance) && canAuthorize && stableJSON(grantDraft) !== stableJSON(initialGrant(installation.permissions, instance?.grant))
  const dirty = nameDirty || configDirty || grantDirty
  const requestClose = useSettingsEditorClose({ busy, dirty, onClose })

  if (!instance) {
    return <Dialog isOpen onClose={onClose} title="插件实例" size="lg" placement="right" drawerSize="wide">
      {error ? <Button variant="outline" onClick={() => void load()}>重新加载</Button> : <p className="text-sm text-muted-foreground" role="status">正在读取…</p>}
    </Dialog>
  }

  const manifest = installation.manifest
  const runnable = instance.status === 'ready' || instance.status === 'degraded'
  const grantMissing = grantDirty ? grantMissingScopes(installation.permissions, grantDraft) : []
  const saveAll = () => void act(async () => {
    let current = instance
    if (nameDirty) applyInstance(current = await api.updateInstance(request, current.id, { name: nameDraft.trim() }), true)
    if (configDirty) applyInstance(current = await api.saveEnvironment(request, current.id, current.revision, values, custom.filter(item => item.name)), true)
    if (grantDirty) applyInstance(current = await api.setGrant(request, current.id, current.grant?.revision || 0, grantDraft), true)
    applyInstance(current)
  }, '已保存')
  const dirtyTab = (on: boolean) => on ? <span className="plugin-tab-dirty" aria-label="有未保存的更改" /> : null
  const footer = <>
    {(canConfigure || canAuthorize) && <span className={`plugin-save-state${dirty ? ' dirty' : ''}`} role="status">
      {!dirty ? '所有更改已保存' : grantMissing.length ? '权限尚未选择资源范围' : '有未保存的更改'}
    </span>}
    {canExecute && <Button variant="outline" disabled={busy || !runnable} title={runnable ? undefined : instanceStatusLabels[instance.status]}
      onClick={() => void act(async () => { const result = await api.runInstance(request, instance.id, newIdempotencyKey('manual')); onOpenRun(result.run.id) }, '已提交运行')}><Play size={15} />立即运行</Button>}
    {canConfigure && instance.auto_paused && <Button variant="outline" disabled={busy} onClick={() => void act(async () => applyInstance(await api.updateInstance(request, instance.id, { resume: true }), true), '已恢复实例')}><RotateCcw size={15} />恢复</Button>}
    {canConfigure && <Button variant="outline" disabled={busy} onClick={() => void act(async () => applyInstance(await api.updateInstance(request, instance.id, { enabled: !instance.enabled }), true), instance.enabled ? '已停用实例' : '已启用实例')}>{instance.enabled ? '停用' : '启用'}</Button>}
    {(canConfigure || canAuthorize) && <Button busy={busy} disabled={!dirty || grantMissing.length > 0} onClick={saveAll}>保存</Button>}
  </>

  return <Dialog isOpen onClose={() => void requestClose()} size="xl" placement="right" drawerSize="wide" footer={footer}
    title={<span className="plugin-dialog-title">{instance.name}<Badge variant={instanceStatusTone(instance.status)}>{instanceStatusLabels[instance.status] || instance.status}</Badge></span>}>
    <div className="plugin-instance">
      {instance.auto_paused && <div className="plugin-callout danger"><p>连续失败 {instance.failure_streak} 次后已自动暂停。修复原因后点击「恢复」。</p></div>}
      {instance.permission_review_required && <div className="plugin-callout warning"><p>新版本申请了新的权限，管理员审核授权前实例不会运行。</p></div>}
      <Tabs value={view} onValueChange={value => setView(value as View)}>
        <TabsList className="plugin-tabs">
          <TabsTrigger value="config">配置{dirtyTab(nameDirty || configDirty)}</TabsTrigger>
          <TabsTrigger value="grant">权限{dirtyTab(grantDirty)}</TabsTrigger>
          <TabsTrigger value="schedules">计划</TabsTrigger>
          <TabsTrigger value="runs">执行</TabsTrigger>
          {!!manifest?.pages?.length && <TabsTrigger value="pages">界面</TabsTrigger>}
          <TabsTrigger value="state">存储</TabsTrigger>
          <TabsTrigger value="activity">活动</TabsTrigger>
        </TabsList>
        <TabsContent value="config">
          {canConfigure && <div className="plugin-rename">
            <label className="plugin-inline-field">实例名称<Input value={nameDraft} maxLength={80} disabled={busy} onChange={event => setNameDraft(event.target.value)} /></label>
          </div>}
          <EnvironmentForm fields={manifest?.environment || []} values={values} onChange={setValues} custom={custom} onCustomChange={canConfigure ? setCustom : undefined}
            customTypes={customTypes} servers={servers} secrets={instance.secrets} onSetSecret={canAuthorize ? name => { setSecretName(name); setSecretValue('') } : undefined}
            issues={issuesForDraft(issues, validatedDraft, { values, custom })} disabled={!canConfigure || busy} />
        </TabsContent>
        <TabsContent value="grant">
          {canAuthorize ? <GrantEditor detail={installation} grant={instance.grant} draft={grantDraft} servers={servers} channels={channels} users={users} plans={plans}
            envServerIDs={serverIDsInValues(installation, values, custom)} busy={busy} onChange={setGrantDraft}
            onRevoke={() => void act(async () => { await api.revokeGrant(request, instance.id); await load() }, '授权已撤销')} />
            : <GrantSummary instance={instance} installation={installation} />}
        </TabsContent>
        <TabsContent value="schedules">
          <ScheduleEditor manifest={manifest} schedules={instance.schedules || []} canEdit={canConfigure} busy={busy}
            onCreate={input => void act(async () => { await api.createSchedule(request, instance.id, input); await load() }, '已添加计划')}
            onToggle={(schedule, enabled) => void act(async () => { await api.updateSchedule(request, schedule.id, { ...schedule, enabled }); await load() })}
            onDelete={schedule => void act(async () => { await api.deleteSchedule(request, schedule.id); await load() }, '已删除计划')} />
        </TabsContent>
        <TabsContent value="runs">
          {!runs.length ? <p className="text-sm text-muted-foreground">还没有执行记录。</p> : <ul className="plugin-record-list">
            {runs.map(run => <li key={run.id}><button type="button" className="plugin-record-row plugin-record-button" onClick={() => onOpenRun(run.id)}>
              <div className="min-w-0">
                <strong>#{run.id} · {triggerLabels[run.trigger] || run.trigger}</strong>
                <p className="plugin-env-help">{formatTime(run.queued_at)}{run.error_code ? ` · ${describeError(run.error_code)}` : ''}</p>
              </div>
              <Badge variant={runTone(run.status)}>{runStatusLabels[run.status] || run.status}</Badge>
            </button></li>)}
          </ul>}
        </TabsContent>
        <TabsContent value="pages">
          {pages.length > 0 ? <PageView pages={pages} pageID={pageID} onSelect={setPageID} canExecute={canExecute && runnable} busy={busy}
            onRefresh={page => void submitPageRun(() => api.refreshPage(request, instance.id, page.id, newIdempotencyKey('ui')), '已提交刷新')}
            onAction={(page, action) => void submitPageRun(() => api.runPageAction(request, instance.id, page.id, action, newIdempotencyKey('action')), '已提交操作')} />
            : !error && <p className="text-sm text-muted-foreground">还没有可展示的界面。</p>}
        </TabsContent>
        <TabsContent value="state">
          <p className="plugin-env-help">已用 {instance.state.keys}/{instance.state.max_keys} 个键 · {formatBytes(instance.state.bytes)}/{formatBytes(instance.state.max_bytes)}</p>
          {!state.length ? <p className="text-sm text-muted-foreground">没有保存的状态。</p> : <ul className="plugin-record-list">
            {state.map(entry => <li key={entry.key} className="plugin-record-row">
              <div className="min-w-0">
                <code>{entry.key}</code>
                <p className="plugin-env-help plugin-truncate">v{entry.version} · {formatTime(entry.updated_at)} · {JSON.stringify(entry.value)}</p>
              </div>
              {canConfigure && <Button size="sm" variant="ghost" disabled={busy} onClick={() => void act(async () => { await api.clearState(request, instance.id, entry.key); setState(current => current.filter(item => item.key !== entry.key)); await load() })}>删除</Button>}
            </li>)}
          </ul>}
          {canConfigure && state.length > 0 && <div className="plugin-actions"><Button variant="outline" className="danger-text" disabled={busy} onClick={() => void act(async () => { await api.clearState(request, instance.id); setState([]); await load() }, '已清空状态')}>清空全部状态</Button></div>}
        </TabsContent>
        <TabsContent value="activity">
          {!audit.length ? <p className="text-sm text-muted-foreground">还没有能力调用记录。</p> : <ul className="plugin-record-list">
            {audit.map(event => <li key={event.id} className="plugin-record-row">
              <div className="min-w-0">
                <strong><code>{event.capability}</code> {event.resource && <span className="plugin-env-help">· {event.resource}</span>}</strong>
                <p className="plugin-env-help">{formatTime(event.created_at)} · {event.duration_ms} ms{event.error_code ? ` · ${describeError(event.error_code)}` : ''}</p>
              </div>
              <Badge variant={event.result === 'succeeded' ? 'success' : event.result === 'denied' ? 'destructive' : 'warning'}>{event.result === 'succeeded' ? '成功' : event.result === 'denied' ? '拒绝' : '失败'}</Badge>
            </li>)}
          </ul>}
        </TabsContent>
      </Tabs>
    </div>
    <Dialog isOpen={Boolean(secretName)} onClose={() => setSecretName('')} title={`设置密钥 ${secretName}`} size="sm" className="plugin-secret-dialog"
      footer={<>
        <Button variant="ghost" onClick={() => setSecretName('')}>取消</Button>
        {instance.secrets?.[secretName]?.configured && <Button variant="outline" className="danger-text" disabled={busy} onClick={() => void act(async () => { await api.setSecret(request, instance.id, secretName, ''); const next = await api.getInstance(request, instance.id); applyInstance(next, true); setSecretName('') }, '已清除密钥')}>清除</Button>}
        <Button busy={busy} disabled={!secretValue} onClick={() => void act(async () => { await api.setSecret(request, instance.id, secretName, secretValue); const next = await api.getInstance(request, instance.id); applyInstance(next, true); setSecretName(''); setSecretValue('') }, '密钥已保存')}>保存</Button>
      </>}>
      <p className="plugin-env-help">密钥加密保存，保存后任何人都无法再查看明文。插件只能在 HTTP 认证或签名中引用它。</p>
      <Input className="plugin-secret-input" type="password" autoComplete="new-password" aria-label="密钥内容" value={secretValue} onChange={event => setSecretValue(event.target.value)} />
    </Dialog>
  </Dialog>
}

function GrantSummary({ instance, installation }: { instance: InstanceDetail; installation: InstallationDetail }) {
  const granted = instance.grant?.capabilities || {}
  return <ul className="plugin-permission-list">
    {installation.permissions.map(line => <li key={line.capability} className="plugin-permission-item">
      <div className="plugin-permission-head">
        <Badge variant={granted[line.capability] ? 'success' : 'secondary'}>{granted[line.capability] ? '已授权' : '未授权'}</Badge>
        <div className="min-w-0"><strong>{line.label}</strong><p className="plugin-env-help">{line.description}</p></div>
      </div>
    </li>)}
    <p className="plugin-env-help">只有管理员可以修改授权。</p>
  </ul>
}
