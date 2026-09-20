import React, { useEffect, useRef, useState } from 'react'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import { Input } from '../../components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../../components/ui/tabs'
import * as api from './api'
import type { PluginRun, PluginUIComponent, PluginUIDocument, PluginsWorkspaceProps } from './types'

export const runTerminal = (status: string) => ['succeeded', 'failed', 'timed_out', 'cancelled', 'skipped'].includes(status)
export const runStatusLabel = (status: string) => ({ pending: '等待中', accepted: '已受理', dispatching: '下发中', executing: '执行中', partial: '部分完成', unknown: '结果待确认', queued: '排队中', running: '执行中', succeeded: '已完成', failed: '失败', timed_out: '超时', cancelled: '已取消', skipped: '已跳过' }[status] || status)
export function displayValue(value: unknown): string {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'object') return '复杂结果'
  return String(value)
}

export function PluginPageRenderer({ document, pluginID, revisionID, request, disabled = false, onViewRun }: {
  document: PluginUIDocument; pluginID: number; revisionID: number; request: PluginsWorkspaceProps['client']['requestV2']; disabled?: boolean; onViewRun?: (id: number) => void
}) {
  const [pageID, setPageID] = useState(document.pages[0]?.id || '')
  const [form, setForm] = useState<PluginUIComponent | null>(null)
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [run, setRun] = useState<PluginRun | null>(null)
  const [componentID, setComponentID] = useState('')
  const [results, setResults] = useState<Record<string, unknown>>({})
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const pending = useRef(false)
  const busy = submitting || !!run && !runTerminal(run.status)
  const unavailable = disabled || !Number.isSafeInteger(revisionID) || revisionID <= 0
  useEffect(() => {
    if (!run || runTerminal(run.status)) return
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout>
    const started = Date.now()
    const poll = async () => {
      try {
        const next = await request(`/plugin-runs/${run.id}`, { signal: controller.signal }) as { run: PluginRun }
        if (controller.signal.aborted) return
        setRun(next.run)
        if (runTerminal(next.run.status)) {
          if (next.run.status === 'succeeded') setResults(current => ({ ...current, [componentID]: next.run.result }))
          return
        }
        if (Date.now() - started > 120_000) { setError('执行仍在进行，可到执行记录查看后续结果'); return }
        timer = setTimeout(poll, 1500)
      } catch (e) { if (!controller.signal.aborted) setError(e instanceof Error ? e.message : '读取执行结果失败') }
    }
    timer = setTimeout(poll, 1000)
    return () => { controller.abort(); clearTimeout(timer) }
  }, [run?.id, componentID, request])
  const execute = async (component: PluginUIComponent, input: Record<string, unknown>) => {
    if (pending.current || busy || unavailable || !component.action) return
    pending.current = true; setSubmitting(true); setError(''); setRun(null); setComponentID(`${pageID}/${component.id}`)
    setResults(current => { const next = { ...current }; delete next[`${pageID}/${component.id}`]; return next })
    try {
      const next = await api.runPlugin(request, pluginID, revisionID, { ...input, ...component.action.params }, {}, `ui-${crypto.randomUUID()}`)
      setRun(next.run); setForm(null)
      if (next.run.status === 'succeeded') setResults(current => ({ ...current, [`${pageID}/${component.id}`]: next.run.result }))
    } catch (e) { setError(e instanceof Error ? e.message : '操作未提交') }
    finally { pending.current = false; setSubmitting(false) }
  }
  if (!document.pages.length) return <p className="text-sm text-muted-foreground">此插件未声明页面。</p>
  return <div className="space-y-4">
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {run && <div className="space-y-2"><p role="status" className="text-sm">{runStatusLabel(run.status)} · 执行 #{run.id}{run.error_code ? ` · ${run.error_code}` : ''}</p>{onViewRun && <Button variant="outline" onClick={() => onViewRun(run.id)}>查看执行记录</Button>}</div>}
    <Tabs value={pageID} onValueChange={setPageID}><TabsList>{document.pages.map(page => <TabsTrigger key={page.id} value={page.id}>{page.title}</TabsTrigger>)}</TabsList>
      {document.pages.map(page => <TabsContent key={page.id} value={page.id}>
        <p className="mb-3 text-sm text-muted-foreground">{page.description}</p>
        <div className="space-y-3">{page.components.map(component => {
          const result = results[`${page.id}/${component.id}`]
          const rows = Array.isArray(result) ? result : result && typeof result === 'object' && 'rows' in result && Array.isArray(result.rows) ? result.rows : []
          return <section key={component.id} className="space-y-2 rounded-xl border border-border p-4">
            {component.title && <h4 className="font-semibold">{component.title}</h4>}
            {component.text && <p className={component.type === 'stat' ? 'text-2xl font-semibold' : 'whitespace-pre-wrap text-sm'}>{component.text}</p>}
            {component.type === 'table' && <div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr>{component.columns?.map(column => <th className="p-2" key={column.key}>{column.label}</th>)}</tr></thead><tbody>{rows.slice(0, 200).map((row, index) => <tr key={index}>{component.columns?.map(column => <td className="p-2" key={column.key}>{displayValue(row && typeof row === 'object' ? row[column.key] : undefined)}</td>)}</tr>)}</tbody></table>{!rows.length && <p className="text-sm text-muted-foreground">暂无结果{component.action ? '，请执行查询' : ''}。</p>}{rows.length > 200 && <p className="text-xs">仅展示前 200 行。</p>}</div>}
            {component.type !== 'table' && result !== undefined && <p className="text-sm">{displayValue(result)}</p>}
            {component.action && <Button disabled={busy || unavailable} onClick={() => {
              if (component.type === 'form') { setError(''); setValues(Object.fromEntries((component.fields || []).filter(field => field.type === 'boolean').map(field => [field.name, false]))); setForm(component) } else void execute(component, {})
            }}>{component.action.label}</Button>}
          </section>
        })}</div>
      </TabsContent>)}
    </Tabs>
    <Dialog isOpen={!!form} onClose={() => { if (!submitting) setForm(null) }} title={form?.title || form?.action?.label || '插件操作'} size="lg">
      {form && <form className="space-y-3" onSubmit={e => { e.preventDefault(); void execute(form, values) }}>
        {form.fields?.map(field => <label key={field.name} className="block text-sm">{field.label}{field.required ? ' *' : ''}
          {field.type === 'boolean' ? <input className="ml-2" type="checkbox" checked={values[field.name] === true} onChange={e => setValues({ ...values, [field.name]: e.target.checked })} />
            : field.type === 'select' ? <select required={field.required} className="block w-full rounded-xl border border-border bg-background p-2" value={String(values[field.name] ?? '')} onChange={e => setValues({ ...values, [field.name]: e.target.value })}><option value="">请选择</option>{field.options?.map(option => <option key={option}>{option}</option>)}</select>
              : <Input required={field.required} type={field.type === 'number' ? 'number' : 'text'} step="any" value={String(values[field.name] ?? '')} onChange={e => setValues({ ...values, [field.name]: e.target.value === '' ? undefined : field.type === 'number' ? Number(e.target.value) : e.target.value })} />}
        </label>)}
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        <p className="text-xs text-muted-foreground">操作由主控验证当前用户、插件版本与授权范围，提交后可追踪执行结果。</p>
        <Button type="submit" disabled={busy || unavailable}>{submitting ? '提交中…' : form.action?.label}</Button>
      </form>}
    </Dialog>
  </div>
}
