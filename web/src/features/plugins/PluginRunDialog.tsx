import React, { useEffect, useState } from 'react'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import * as api from './api'
import { displayValue, runStatusLabel, runTerminal } from './PluginPageRenderer'
import type { PluginRun, PluginsWorkspaceProps } from './types'

export function PluginRunDialog({ id, request, onClose }: { id: number; request: PluginsWorkspaceProps['client']['requestV2']; onClose: () => void }) {
  const [run, setRun] = useState<PluginRun | null>(null)
  const [logs, setLogs] = useState<Array<{ id?: number; level?: string; message?: string }>>([])
  const [actions, setActions] = useState<Array<{ id: number; kind?: string; status: string; error_code?: string }>>([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    const load = async () => {
      try {
        const [next, logPage, actionPage] = await Promise.all([
          request(`/plugin-runs/${id}`, { signal: controller.signal }),
          request(`/plugin-runs/${id}/logs`, { signal: controller.signal }),
          request(`/plugin-runs/${id}/actions`, { signal: controller.signal }),
        ])
        if (controller.signal.aborted) return
        setRun(next.run); setLogs(logPage.logs || []); setActions(actionPage.actions || []); setError('')
        if (!runTerminal(next.run.status) || actionPage.actions?.some((action: { status: string }) => ['pending', 'accepted', 'dispatching', 'executing'].includes(action.status))) timer = setTimeout(load, 2000)
      } catch (e) { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : '读取执行记录失败') }
    }
    void load()
    return () => { controller.abort(); clearTimeout(timer) }
  }, [id, request])
  return <Dialog isOpen title={`执行 #${id}`} onClose={onClose} size="lg"><div className="space-y-4">
    {error && <p role="alert" className="text-destructive text-sm">{error}</p>}
    {run ? <>
      <p>{runStatusLabel(run.status)} · {run.mode === 'simulate' ? '模拟执行' : '正式执行'} · 版本 #{run.revision_id}</p>
      <p className="text-sm text-muted-foreground">开始：{run.created_at}{run.finished_at ? ` · 结束：${run.finished_at}` : ''}</p>
      {(run.error_code || run.skip_reason) && <p className="text-sm">原因：{run.error_code || run.skip_reason}</p>}
      {run.result !== undefined && <section><h4 className="font-semibold">执行结果</h4>{run.result && typeof run.result === 'object' && !Array.isArray(run.result) ? <dl className="text-sm">{Object.entries(run.result).slice(0, 30).map(([key, value]) => <div key={key} className="flex flex-wrap gap-2"><dt>{key}</dt><dd className="break-all">{displayValue(value)}</dd></div>)}</dl> : <p className="text-sm">{displayValue(run.result)}</p>}</section>}
      {!runTerminal(run.status) && <Button disabled={busy} variant="outline" onClick={async () => { setBusy(true); try { const next = await api.cancelRun(request, id); setRun(next.run) } catch (e) { setError(e instanceof Error ? e.message : '取消失败') } finally { setBusy(false) } }}>请求取消</Button>}
    </> : <p>正在读取…</p>}
    <section className="space-y-2"><h4 className="font-semibold">节点操作</h4>{!actions.length && <p className="text-sm text-muted-foreground">无节点操作。</p>}{actions.map(action => <p key={action.id} className="text-sm">#{action.id} · {action.kind || '操作'} · {runStatusLabel(action.status)}{action.error_code ? ` · ${action.error_code}` : ''}</p>)}</section>
    <section className="space-y-2"><h4 className="font-semibold">日志</h4>{!logs.length && <p className="text-sm text-muted-foreground">暂无日志。</p>}{logs.map((log, index) => <p key={log.id || index} className="whitespace-pre-wrap break-all rounded-xl bg-secondary/50 p-2 text-xs">{log.level ? `[${log.level}] ` : ''}{log.message || ''}</p>)}</section>
    <p className="text-xs text-muted-foreground">取消不会撤回已下发的节点动作；以节点操作最终状态为准。</p>
  </div></Dialog>
}
