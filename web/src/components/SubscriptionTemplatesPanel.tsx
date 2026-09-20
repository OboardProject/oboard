import React, { useEffect, useState } from 'react'
import { FileCode2, RotateCcw } from 'lucide-react'
import { useDialogs } from './ui/dialog-context'
import { Dialog } from './ui/dialog'
import { SettingsDisclosure } from './settings/SettingsLayout'
import { useSettingsEditorClose } from './settings/useSettingsEditorClose'

export type SubscriptionClientTemplate = {
  format: string
  label: string
  content: string
  source: 'builtin' | 'custom'
  revision: number
  builtin_digest: string
  base_builtin_digest?: string
  builtin_updated?: boolean
  markers: string[]
}

export function SubscriptionTemplatesPanel({ client, notify }: { client: any; notify: (message: string, tone?: 'success' | 'warning' | 'danger' | 'error' | 'info') => void }) {
  const dialogs = useDialogs()
  const [items, setItems] = useState<SubscriptionClientTemplate[]>([])
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<SubscriptionClientTemplate | null>(null)
  const [draft, setDraft] = useState('')
  const [preview, setPreview] = useState('')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [loadError, setLoadError] = useState('')
  const closeEditor = useSettingsEditorClose({ busy: Boolean(busy), dirty: Boolean(editing) && draft !== editing?.content, onClose: () => setEditing(null) })

  const refresh = async (isActive = () => true) => {
    const result = await client.request('/subscription-templates')
    if (isActive()) { setItems(result.subscription_templates || []); setLoadError('') }
  }

  const retry = async () => {
    setLoading(true)
    try { await refresh() }
    catch (error: any) { setLoadError(String(error?.message || error || '加载模板失败')) }
    finally { setLoading(false) }
  }

  useEffect(() => {
    let active = true
    setLoading(true)
    refresh(() => active).catch(error => {
      if (active) setLoadError(String(error?.message || error || '加载模板失败'))
    }).finally(() => {
      if (active) setLoading(false)
    })
    return () => { active = false }
  }, [])

  const openEditor = (item: SubscriptionClientTemplate) => {
    setEditing(item)
    setError('')
    setDraft(item.content)
    setPreview('')
  }

  const run = async (key: string, action: () => Promise<void>) => {
    if (busy) return
    setBusy(key)
    setError('')
    try {
      await action()
    } catch (error: any) {
      setError(String(error?.message || error || '操作失败'))
      notify(String(error?.message || error || '操作失败'), 'error')
    } finally {
      setBusy('')
    }
  }

  const validate = () => run('validate', async () => {
    if (!editing) return
    await client.request(`/subscription-templates/${editing.format}/validate`, { method: 'POST', body: JSON.stringify({ content: draft }) })
    notify('模板校验通过', 'success')
  })

  const renderPreview = () => run('preview', async () => {
    if (!editing) return
    const result = await client.request(`/subscription-templates/${editing.format}/preview`, { method: 'POST', body: JSON.stringify({ content: draft }) })
    setPreview(String(result.content || ''))
    notify('已生成合成节点预览', 'success')
  })

  const save = () => run('save', async () => {
    if (!editing) return
    const result = await client.request(`/subscription-templates/${editing.format}`, {
      method: 'PUT',
      body: JSON.stringify({ content: draft, expected_revision: editing.revision || 0 }),
    })
    const saved = result.subscription_template as SubscriptionClientTemplate
    notify('模板已保存，下次订阅拉取立即生效', 'success')
    setEditing(saved)
    setDraft(saved.content)
    setPreview('')
    await refresh()
  })

  const reset = () => run('reset', async () => {
    if (!editing) return
    if (!await dialogs.confirm({
      title: `恢复 ${editing.label} 系统模板？`,
      message: '将删除当前自定义模板并恢复系统默认，下次获取订阅时生效。',
      confirmText: '恢复系统默认',
      tone: 'danger',
    })) return
    const result = await client.request(`/subscription-templates/${editing.format}/reset`, { method: 'POST', body: '{}' })
    const restored = result.subscription_template as SubscriptionClientTemplate
    notify('已恢复系统默认模板', 'success')
    setEditing(restored)
    setDraft(restored.content)
    setPreview('')
    await refresh()
  })

  const customCount = items.filter(item => item.source === 'custom').length
  const summaryText = items.length ? `${items.length} 个模板${customCount > 0 ? ` · ${customCount} 个自定义` : ''}` : undefined

  return (
    <>
      <SettingsDisclosure
        title="客户端模板"
        className="signal-templates"
        defaultOpen
        summary={summaryText}
      >
        {loading ? <div className="settings-loading" role="status"><span>正在加载模板…</span><div /><div /><div /></div> : loadError ? <div className="settings-feedback"><p role="alert">{loadError}</p><button type="button" className="ghost" onClick={() => void retry()}>重新加载</button></div> : !items.length ? <p className="settings-feedback">暂无可用模板</p> : (
          <div className="subscription-template-list">
            {items.map(item => (
              <button key={item.format} type="button" className="subscription-template-row" onClick={() => openEditor(item)}>
                <div>
                  <strong>{item.label}</strong>
                  <span className="muted">{item.format} · {item.source === 'custom' ? '自定义' : '系统'} · 版本 {item.revision || 0}</span>
                </div>
                <span className={`status-pill ${item.builtin_updated ? 'warning' : 'neutral'}`}>{item.source === 'custom' ? (item.builtin_updated ? '基于旧系统模板' : '自定义') : '系统默认'}</span>
              </button>
            ))}
          </div>
        )}
      </SettingsDisclosure>
      {editing && (
        <Dialog isOpen={Boolean(editing)} onClose={() => void closeEditor()} className="signal-settings-dialog" title={`${editing.label} 模板`} size="xl">
          <div className="subscription-template-editor" aria-busy={Boolean(busy)}>
            <p className="muted">来源 {editing.source === 'custom' ? '自定义' : '系统'} · 版本 {editing.revision || 0} · 系统摘要 {editing.builtin_digest.slice(0, 12)}{editing.base_builtin_digest ? ` · 自定义基于 ${editing.base_builtin_digest.slice(0, 12)}` : ''}</p>
            {editing.builtin_updated && <p className="danger-text">系统模板已有新版，当前自定义模板仍基于旧系统模板，不会自动覆盖。</p>}
            <p className="muted">可用标记：{(editing.markers || []).join(' ')}</p>
            <textarea className="monospace-input subscription-template-textarea" disabled={Boolean(busy)} value={draft} onChange={event => { setDraft(event.target.value); setPreview(''); setError('') }} spellCheck={false} aria-label={`${editing.label} 模板内容`} />
            {preview && <section aria-label="合成节点预览"><h4>合成节点预览</h4><pre className="subscription-template-preview">{preview}</pre></section>}
            {error && <p className="settings-feedback" role="alert">{error}</p>}
            <footer className="dialog-actions">
              <button type="button" className="ghost" onClick={validate} disabled={Boolean(busy)}>{busy === 'validate' ? '校验中...' : '校验'}</button>
              <button type="button" className="ghost" onClick={renderPreview} disabled={Boolean(busy)}>{busy === 'preview' ? '预览中...' : '预览'}</button>
              {editing.source === 'custom' && <button type="button" className="danger-ghost" onClick={reset} disabled={Boolean(busy)}><RotateCcw size={14} />{busy === 'reset' ? '恢复中...' : '恢复系统默认'}</button>}
              <button type="button" onClick={save} disabled={Boolean(busy) || draft === editing.content}><FileCode2 size={14} />{busy === 'save' ? '保存中...' : '保存'}</button>
            </footer>
          </div>
        </Dialog>
      )}
    </>
  )
}
