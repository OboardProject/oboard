import React, { useCallback, useEffect, useState } from 'react'
import { Play, RotateCcw } from 'lucide-react'
import { Badge } from '../../components/ui/badge'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import { Input } from '../../components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../../components/ui/tabs'
import * as api from './api'
import { EnvironmentForm } from './EnvironmentForm'
import { GrantEditor } from './GrantEditor'
import { PageView } from './PageView'
import { ScheduleEditor } from './ScheduleEditor'
import {
  describeError, errorIssues, errorMessage, formatBytes, formatTime, instanceStatusLabels, instanceStatusTone,
  issuesByField, newIdempotencyKey, runStatusLabels, runTone, triggerLabels,
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

export function InstanceDialog(props: InstanceDialogProps) {
  const { instanceID, installation, request, servers, channels, users, plans, customTypes, canConfigure, canExecute, canAuthorize, notify, onChanged, onOpenRun, onClose } = props
  const [instance, setInstance] = useState<InstanceDetail | null>(null)
  const [view, setView] = useState<View>('config')
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [custom, setCustom] = useState<CustomVar[]>([])
  const [issues, setIssues] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [runs, setRuns] = useState<Run[]>([])
  const [pages, setPages] = useState<InstancePage[]>([])
  const [pageID, setPageID] = useState('')
  const [state, setState] = useState<StateEntry[]>([])
  const [audit, setAudit] = useState<AuditEvent[]>([])
  const [secretName, setSecretName] = useState('')
  const [secretValue, setSecretValue] = useState('')
  const [nameDraft, setNameDraft] = useState('')

  const applyInstance = useCallback((next: InstanceDetail) => {
    setInstance(next)
    setValues(next.values || {})
    setCustom(next.custom || [])
    setIssues(issuesByField(next.issues))
    setNameDraft(next.name)
  }, [])

  const load = useCallback(async () => {
    try { applyInstance(await api.getInstance(request, instanceID)); setError('') } catch (e) { setError(errorMessage(e, '读取实例失败')) }
  }, [applyInstance, instanceID, request])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
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
      if (fieldIssues.length) setIssues(issuesByField(fieldIssues))
      setError(errorMessage(e, '操作失败'))
    } finally {
      setBusy(false)
    }
  }

  if (!instance) {
    return <Dialog isOpen onClose={onClose} title="插件实例" size="lg" placement="right" drawerSize="wide">
      {error ? <p role="alert" className="plugin-field-error">{error}</p> : <p className="text-sm text-muted-foreground" role="status">正在读取…</p>}
    </Dialog>
  }

  const manifest = installation.manifest
  const runnable = instance.status === 'ready' || instance.status === 'degraded'
  const footer = <>
    {canExecute && <Button variant="outline" disabled={busy || !runnable} title={runnable ? undefined : instanceStatusLabels[instance.status]}
      onClick={() => void act(async () => { const result = await api.runInstance(request, instance.id, newIdempotencyKey('manual')); onOpenRun(result.run.id) }, '已提交运行')}><Play size={15} />立即运行</Button>}
    {canConfigure && instance.auto_paused && <Button variant="outline" disabled={busy} onClick={() => void act(async () => applyInstance(await api.updateInstance(request, instance.id, { resume: true })), '已恢复实例')}><RotateCcw size={15} />恢复</Button>}
    {canConfigure && <Button variant={instance.enabled ? 'ghost' : 'default'} disabled={busy} onClick={() => void act(async () => applyInstance(await api.updateInstance(request, instance.id, { enabled: !instance.enabled })), instance.enabled ? '已停用实例' : '已启用实例')}>{instance.enabled ? '停用' : '启用'}</Button>}
  </>

  return <Dialog isOpen onClose={onClose} size="xl" placement="right" drawerSize="wide" footer={footer}
    title={<span className="plugin-dialog-title">{instance.name}<Badge variant={instanceStatusTone(instance.status)}>{instanceStatusLabels[instance.status] || instance.status}</Badge></span>}>
    <div className="plugin-instance">
      {error && <p role="alert" className="plugin-field-error">{error}</p>}
      {instance.auto_paused && <div className="plugin-callout danger"><p>连续失败 {instance.failure_streak} 次后已自动暂停。修复原因后点击「恢复」。</p></div>}
      {instance.permission_review_required && <div className="plugin-callout warning"><p>新版本申请了新的权限，管理员审核授权前实例不会运行。</p></div>}
      <Tabs value={view} onValueChange={value => setView(value as View)}>
        <TabsList className="plugin-tabs">
          <TabsTrigger value="config">配置</TabsTrigger>
          <TabsTrigger value="grant">权限</TabsTrigger>
          <TabsTrigger value="schedules">计划</TabsTrigger>
          <TabsTrigger value="runs">执行</TabsTrigger>
          {!!manifest?.pages?.length && <TabsTrigger value="pages">界面</TabsTrigger>}
          <TabsTrigger value="state">存储</TabsTrigger>
          <TabsTrigger value="activity">活动</TabsTrigger>
        </TabsList>
        <TabsContent value="config">
          {canConfigure && <div className="plugin-rename">
            <label className="plugin-inline-field">实例名称<Input value={nameDraft} maxLength={80} onChange={event => setNameDraft(event.target.value)} /></label>
            <Button size="sm" variant="outline" disabled={busy || !nameDraft.trim() || nameDraft === instance.name}
              onClick={() => void act(async () => applyInstance(await api.updateInstance(request, instance.id, { name: nameDraft.trim() })), '已重命名')}>保存名称</Button>
          </div>}
          <EnvironmentForm fields={manifest?.environment || []} values={values} onChange={setValues} custom={custom} onCustomChange={canConfigure ? setCustom : undefined}
            customTypes={customTypes} servers={servers} secrets={instance.secrets} onSetSecret={canAuthorize ? name => { setSecretName(name); setSecretValue('') } : undefined}
            issues={issues} disabled={!canConfigure || busy} />
          {canConfigure && <div className="plugin-actions">
            <Button busy={busy} onClick={() => void act(async () => applyInstance(await api.saveEnvironment(request, instance.id, instance.revision, values, custom.filter(item => item.name))), '配置已保存')}>保存配置</Button>
          </div>}
        </TabsContent>
        <TabsContent value="grant">
          {canAuthorize ? <GrantEditor key={`${instance.grant?.revision || 0}-${instance.grant?.package_id || 0}`} detail={installation} grant={instance.grant} servers={servers} channels={channels} users={users} plans={plans}
            envServerIDs={serverIDsInValues(installation, values, custom)} busy={busy}
            onSave={(grant: Grant) => void act(async () => applyInstance(await api.setGrant(request, instance.id, instance.grant?.revision || 0, grant)), '授权已保存')}
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
    <Dialog isOpen={Boolean(secretName)} onClose={() => setSecretName('')} title={`设置密钥 ${secretName}`} size="sm"
      footer={<>
        <Button variant="ghost" onClick={() => setSecretName('')}>取消</Button>
        {instance.secrets?.[secretName]?.configured && <Button variant="outline" className="danger-text" disabled={busy} onClick={() => void act(async () => { await api.setSecret(request, instance.id, secretName, ''); setSecretName(''); await load() }, '已清除密钥')}>清除</Button>}
        <Button busy={busy} disabled={!secretValue} onClick={() => void act(async () => { await api.setSecret(request, instance.id, secretName, secretValue); setSecretName(''); setSecretValue(''); await load() }, '密钥已保存')}>保存</Button>
      </>}>
      <p className="plugin-env-help">密钥加密保存，保存后任何人都无法再查看明文。插件只能在 HTTP 认证或签名中引用它。</p>
      <Input type="password" autoComplete="new-password" aria-label="密钥内容" value={secretValue} onChange={event => setSecretValue(event.target.value)} />
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
