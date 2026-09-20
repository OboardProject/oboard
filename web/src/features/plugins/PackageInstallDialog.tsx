import React, { useRef, useState } from 'react'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import { Input } from '../../components/ui/input'
import * as api from './api'
import type { PluginPackageInput, PluginPackageInstallInput, PluginPackagePreview, PluginsWorkspaceProps } from './types'

export const MAX_PACKAGE_BYTES = 8 * 1024 * 1024
export async function encodePackage(file: File): Promise<string> {
  if (file.size > MAX_PACKAGE_BYTES) throw new Error('ZIP 文件不能超过 8 MiB')
  const bytes = new Uint8Array(await file.arrayBuffer())
  let binary = ''
  for (let offset = 0; offset < bytes.length; offset += 8192) binary += String.fromCharCode(...bytes.subarray(offset, offset + 8192))
  return btoa(binary)
}

export function PackageInstallDialog({ request, onClose, onInstalled }: {
  request: PluginsWorkspaceProps['client']['requestV2']; onClose: () => void; onInstalled: () => void
}) {
  const [mode, setMode] = useState<'github' | 'zip'>('github')
  const [repository, setRepository] = useState('')
  const [ref, setRef] = useState('')
  const [file, setFile] = useState<File | null>(null)
  const [preview, setPreview] = useState<PluginPackagePreview | null>(null)
  const [snapshot, setSnapshot] = useState<PluginPackageInstallInput | null>(null)
  const [busy, setBusy] = useState(false)
  const pending = useRef(false)
  const [error, setError] = useState('')
  const invalidate = () => { setPreview(null); setSnapshot(null); setError('') }
  const inspect = async () => {
    if (pending.current) return
    pending.current = true; setBusy(true); invalidate()
    try {
      let input: PluginPackageInput
      if (mode === 'zip') {
        if (!file) throw new Error('请选择 ZIP 安装包')
        input = { package_zip: await encodePackage(file) }
      } else {
        const url = new URL(repository.trim())
        if (url.protocol !== 'https:' || url.hostname !== 'github.com' || url.username || url.password || url.port || url.search || url.hash || !/^\/[^/]+\/[^/]+\/?$/.test(url.pathname)) throw new Error('请输入完整的 HTTPS GitHub 仓库地址，不含分支路径')
        input = { source: { repository_url: url.toString(), ref: ref.trim() } }
      }
      const result = await api.previewPackage(request, input)
      if (!/^[a-f0-9]{64}$/.test(result.sha256)) throw new Error('预览未返回有效包摘要，无法确认安装')
      if (!result.metadata?.plugin_id || !result.metadata.name || !result.metadata.version || !result.capabilities) throw new Error('预览缺少版本或权限信息，无法确认安装')
      if (input.source) {
        if (result.source?.kind !== 'github' || !/^[a-f0-9]{40}$/.test(result.source.commit || '')) throw new Error('预览未返回固定提交，无法确认安装')
        setSnapshot({ source: { repository_url: input.source.repository_url, commit: result.source.commit! } })
      } else {
        setSnapshot(input)
      }
      setPreview(result)
    } catch (e) { setError(e instanceof Error ? e.message : '预览失败') }
    finally { pending.current = false; setBusy(false) }
  }
  return <Dialog isOpen onClose={() => { if (!busy) onClose() }} title="安装或更新插件" size="lg">
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">先检查版本与权限，再确认安装。新插件默认停用；更新不会继承旧版本授权。</p>
      <div className="flex gap-2"><Button variant={mode === 'github' ? 'default' : 'outline'} disabled={busy} onClick={() => { setMode('github'); invalidate() }}>GitHub 仓库</Button><Button variant={mode === 'zip' ? 'default' : 'outline'} disabled={busy} onClick={() => { setMode('zip'); invalidate() }}>上传 ZIP</Button></div>
      {mode === 'github' ? <>
        <label className="block text-sm">仓库地址<Input type="url" value={repository} disabled={busy} placeholder="https://github.com/owner/plugin" onChange={e => { setRepository(e.target.value); invalidate() }} /></label>
        <label className="block text-sm">分支、标签或提交（可留空使用默认分支）<Input value={ref} disabled={busy} onChange={e => { setRef(e.target.value); invalidate() }} /></label>
      </> : <label className="block text-sm">ZIP 安装包（最多 8 MiB）<Input type="file" accept=".zip,application/zip" disabled={busy} onChange={e => { setFile(e.target.files?.[0] || null); invalidate() }} /></label>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      {preview && <section className="space-y-2 rounded-xl border border-border p-4 text-sm" aria-label="安装预览">
        <h4 className="font-semibold">{preview.metadata.name} · {preview.metadata.version}</h4><p>{preview.metadata.description}</p>
        <p>{preview.existing_plugin_id ? '更新已安装插件' : '首次安装'} · {preview.has_ui ? '含插件页面' : '自动化插件'}</p>
        <p className="break-all">包摘要 SHA-256：{preview.sha256}</p>
        {preview.source?.repository && <p className="break-all">来源：{preview.source.repository}</p>}
        {preview.source?.commit && <p className="break-all">固定提交：{preview.source.commit}</p>}
        <p>新增权限：{preview.capabilities.added?.join('、') || '无'}</p><p>移除权限：{preview.capabilities.removed?.join('、') || '无'}</p><p>保持权限：{preview.capabilities.unchanged?.join('、') || '无'}</p>
        <p className="text-muted-foreground">安装不代表授权。更新版本需在详情中单独激活；请管理员审核能力与资源范围后再启用。</p>
      </section>}
      <div className="flex justify-end gap-2"><Button variant="ghost" disabled={busy} onClick={onClose}>取消</Button><Button variant="outline" disabled={busy} onClick={() => void inspect()}>{busy ? '处理中…' : '检查安装包'}</Button>
        {preview && snapshot && <Button disabled={busy} onClick={async () => {
          if (pending.current) return
          pending.current = true; setBusy(true); setError('')
          try { await api.installPackage(request, snapshot, preview.sha256); onInstalled() }
          catch (e) { setError(e instanceof Error ? e.message : '安装失败，请重新检查安装包') }
          finally { pending.current = false; setBusy(false) }
        }}>确认安装</Button>}
      </div>
    </div>
  </Dialog>
}
