import React, { useEffect, useMemo, useRef, useState } from 'react'
import { AlertTriangle, CheckCircle2, PanelRight, Upload } from 'lucide-react'
import { Badge } from '../../components/ui/badge'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import * as api from './api'
import { EnvironmentForm } from './EnvironmentForm'
import { PermissionList } from './PackageInstallDialog'
import { defaultDraftManifest, defaultDraftSource, errorMessage } from './domain'
import type { EditorDiagnostics, EnvType, RequestFn, ServerOption, ToastTone } from './types'

function useWideLayout(query = '(min-width: 1024px)') {
  const get = () => typeof window === 'undefined' || typeof window.matchMedia !== 'function' || window.matchMedia(query).matches
  const [wide, setWide] = useState(get)
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const media = window.matchMedia(query)
    const update = () => setWide(media.matches)
    media.addEventListener?.('change', update)
    return () => media.removeEventListener?.('change', update)
  }, [query])
  return wide
}

type FileName = 'main.js' | 'manifest.json'

export interface PluginEditorProps {
  pluginID?: number
  initialManifest?: string
  initialSource?: string
  request: RequestFn
  servers: ServerOption[]
  customTypes: EnvType[]
  canPublish: boolean
  notify: (message: string, tone?: ToastTone) => void
  onSaved: (pluginID: number) => void
  onPublished: (pluginID: number, reviewRequired: boolean) => void
  onClose: () => void
}

export function PluginEditor({ pluginID, initialManifest, initialSource, request, servers, customTypes, canPublish, notify, onSaved, onPublished, onClose }: PluginEditorProps) {
  const [id, setID] = useState<number | undefined>(pluginID)
  const [manifest, setManifest] = useState(initialManifest || JSON.stringify(defaultDraftManifest, null, 2))
  const [source, setSource] = useState(initialSource ?? defaultDraftSource)
  const [file, setFile] = useState<FileName>('main.js')
  const [diagnostics, setDiagnostics] = useState<EditorDiagnostics | null>(null)
  const [diagnoseError, setDiagnoseError] = useState('')
  const [previewValues, setPreviewValues] = useState<Record<string, unknown>>({})
  const [busy, setBusy] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [panelOpen, setPanelOpen] = useState(false)
  const [confirmPublish, setConfirmPublish] = useState(false)
  const wide = useWideLayout()
  const diagnoseSeq = useRef(0)

  useEffect(() => {
    const seq = ++diagnoseSeq.current
    const timer = setTimeout(async () => {
      try {
        const result = await api.diagnoseDraft(request, manifest, source)
        if (seq === diagnoseSeq.current) { setDiagnostics(result); setDiagnoseError('') }
      } catch (e) {
        if (seq === diagnoseSeq.current) setDiagnoseError(errorMessage(e, '诊断失败'))
      }
    }, 600)
    return () => clearTimeout(timer)
  }, [manifest, source, request])

  // save returns the plugin ID so a first publish does not read stale state.
  const save = async (): Promise<number | undefined> => {
    setBusy(true)
    try {
      let target = id
      if (target) await api.saveDraft(request, target, manifest, source)
      else {
        target = (await api.createDraftPlugin(request, manifest, source)).plugin_id
        setID(target)
        onSaved(target)
      }
      setDirty(false)
      notify('草稿已保存', 'success')
      return target
    } catch (e) {
      notify(errorMessage(e, '保存失败'), 'error')
      return undefined
    } finally {
      setBusy(false)
    }
  }

  const publish = async () => {
    setConfirmPublish(false)
    const target = await save()
    if (!target) return
    setBusy(true)
    try {
      const result = await api.publishDraft(request, target) as { review_required?: boolean }
      notify('已发布新版本', 'success')
      onPublished(target, Boolean(result?.review_required))
    } catch (e) {
      notify(errorMessage(e, '发布失败'), 'error')
    } finally {
      setBusy(false)
    }
  }

  const issues = diagnostics?.issues || []
  const fields = diagnostics?.manifest?.environment || []
  const lineCount = useMemo(() => (file === 'main.js' ? source : manifest).split('\n').length, [file, source, manifest])

  const panel = <div className="plugin-editor-panel-body">
    <section>
      <div className="plugin-section-head">
        <h4>诊断</h4>
        {diagnostics && (diagnostics.valid ? <Badge variant="success"><CheckCircle2 size={13} />可发布</Badge> : <Badge variant="destructive"><AlertTriangle size={13} />{issues.length} 个问题</Badge>)}
      </div>
      {diagnoseError && <p className="plugin-field-error">{diagnoseError}</p>}
      {!diagnostics && !diagnoseError && <p className="plugin-env-help" role="status">正在检查…</p>}
      {issues.length > 0 && <ul className="plugin-issue-list">
        {issues.map((issue, index) => <li key={index}><code>{issue.field || '清单'}</code>{issue.message}</li>)}
      </ul>}
      {diagnostics?.undeclared_capabilities?.length ? <p className="plugin-field-error">代码调用了未声明的能力：{diagnostics.undeclared_capabilities.map(cap => <code key={cap} className="mr-1">{cap}</code>)}</p> : null}
      {diagnostics?.unused_environment?.length ? <p className="plugin-env-help">未在代码中使用的变量：{diagnostics.unused_environment.join('、')}</p> : null}
      <p className="plugin-env-help">诊断只解析与编译，不会执行代码。</p>
    </section>
    <section>
      <h4 className="plugin-subhead">配置面板预览</h4>
      <p className="plugin-env-help">根据清单中的 environment 自动生成，与实例配置页一致。</p>
      <EnvironmentForm fields={fields} values={previewValues} onChange={setPreviewValues} custom={[]} customTypes={customTypes} servers={servers} />
    </section>
    <section>
      <h4 className="plugin-subhead">申请的权限</h4>
      <PermissionList permissions={diagnostics?.permissions || []} hosts={diagnostics?.manifest?.http?.hosts} />
    </section>
  </div>

  const footer = <>
    {!wide && <Button variant="outline" onClick={() => setPanelOpen(true)}><PanelRight size={15} />配置面板</Button>}
    <Button variant="ghost" onClick={onClose}>关闭</Button>
    <Button variant="outline" busy={busy} disabled={!dirty && Boolean(id)} onClick={() => void save()}>保存草稿</Button>
    {canPublish && <Button busy={busy} disabled={!diagnostics?.valid} onClick={() => setConfirmPublish(true)}><Upload size={15} />发布</Button>}
  </>

  return <Dialog isOpen onClose={onClose} size="xl" className="plugin-editor-dialog" footer={footer}
    title={<span className="plugin-dialog-title">{diagnostics?.manifest?.name || '插件编辑器'}{dirty && <Badge variant="warning">未保存</Badge>}</span>}>
    <div className="plugin-editor">
      <div className="plugin-editor-main">
        <div className="plugin-file-tabs" role="tablist" aria-label="文件">
          {(['main.js', 'manifest.json'] as FileName[]).map(name => <button key={name} type="button" role="tab" aria-selected={file === name} className={file === name ? 'active' : ''} onClick={() => setFile(name)}>{name}</button>)}
          <span className="plugin-env-help">{lineCount} 行</span>
        </div>
        <textarea
          className="plugin-code-editor"
          aria-label={file}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          value={file === 'main.js' ? source : manifest}
          onKeyDown={event => {
            if (event.key !== 'Tab') return
            event.preventDefault()
            const target = event.currentTarget
            const start = target.selectionStart
            const next = target.value.slice(0, start) + '  ' + target.value.slice(target.selectionEnd)
            if (file === 'main.js') setSource(next); else setManifest(next)
            setDirty(true)
            requestAnimationFrame(() => { target.selectionStart = target.selectionEnd = start + 2 })
          }}
          onChange={event => { if (file === 'main.js') setSource(event.target.value); else setManifest(event.target.value); setDirty(true) }}
        />
      </div>
      {wide && <aside className="plugin-editor-panel" aria-label="配置面板">{panel}</aside>}
    </div>
    {!wide && <Dialog isOpen={panelOpen} onClose={() => setPanelOpen(false)} title="配置面板" size="lg" placement="right">{panel}</Dialog>}
    <Dialog isOpen={confirmPublish} onClose={() => setConfirmPublish(false)} title="发布新版本" size="sm"
      footer={<><Button variant="ghost" onClick={() => setConfirmPublish(false)}>取消</Button><Button busy={busy} onClick={() => void publish()}>发布</Button></>}>
      <p className="text-sm">发布后成为本地插件的新版本（发布者为本地、未签名）。若申请了新权限，现有实例在管理员审核授权前不会运行。</p>
    </Dialog>
  </Dialog>
}
