import React, { useState } from 'react'
import { AlertTriangle, BadgeCheck, FileArchive, GitBranch } from 'lucide-react'
import { Badge } from '../../components/ui/badge'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import { Input } from '../../components/ui/input'
import * as api from './api'
import { envTypeLabels, errorMessage, publisherLabel, readFileAsBase64, riskLabels, shortHash } from './domain'
import type { PackagePreview, PackageSource, PermissionDiff, PermissionLine, RequestFn } from './types'

export function PermissionList({ permissions, hosts, forbidden }: { permissions: PermissionLine[]; hosts?: string[]; forbidden?: string[] }) {
  return <div className="plugin-permission-review">
    {!permissions.length ? <p className="text-sm text-muted-foreground">不申请任何能力。</p> : <ul className="plugin-permission-list">
      {permissions.map(line => <li key={line.capability} className="plugin-permission-item">
        <div className="plugin-permission-head">
          <Badge variant={line.risk === 'high' ? 'destructive' : line.risk === 'medium' ? 'warning' : 'secondary'}>{riskLabels[line.risk]}</Badge>
          <div className="min-w-0">
            <strong>{line.label}</strong>
            <p className="plugin-env-help">{line.description}</p>
            {line.capability === 'http.request' && hosts?.length ? <p className="plugin-env-help">主机：{hosts.map(host => <code key={host} className="mr-1">{host}</code>)}</p> : null}
          </div>
        </div>
      </li>)}
    </ul>}
    {forbidden?.length ? <div className="plugin-forbidden"><strong>插件永远不能获得</strong><p>{forbidden.join('、')}</p></div> : null}
  </div>
}

function DiffList({ diff }: { diff: PermissionDiff }) {
  const rows: Array<[string, string[]]> = [
    ['新增能力', diff.added_capabilities], ['移除能力', diff.removed_capabilities], ['新增 HTTP 主机', diff.added_hosts],
    ['移除 HTTP 主机', diff.removed_hosts], ['新增 HTTP 方法', diff.added_methods], ['新增事件', diff.added_events],
    ['新增密钥', diff.added_secrets], ['新增变量', diff.added_environment], ['移除变量', diff.removed_environment],
    ['变量类型变化', diff.changed_environment], ['新增必填项', diff.new_required],
    ['新增页面', diff.added_pages], ['新增操作', diff.added_actions],
  ]
  const shown = rows.filter(([, items]) => items?.length)
  if (!shown.length && !diff.resources_expanded) return <p className="text-sm text-muted-foreground">权限与配置项没有变化。</p>
  return <dl className="plugin-diff">
    {shown.map(([label, items]) => <div key={label}><dt>{label}</dt><dd>{items.map(item => <code key={item}>{item}</code>)}</dd></div>)}
    {diff.resources_expanded && <div><dt>资源范围</dt><dd>可选择的服务器数量上限增加</dd></div>}
  </dl>
}

export function PackageInstallDialog({ request, onClose, onInstalled }: { request: RequestFn; onClose: () => void; onInstalled: (installationID: number, reviewRequired: boolean) => void }) {
  const [mode, setMode] = useState<'upload' | 'github'>('upload')
  const [fileName, setFileName] = useState('')
  const [packageBase64, setPackageBase64] = useState('')
  const [repository, setRepository] = useState('')
  const [ref, setRef] = useState('')
  const [preview, setPreview] = useState<PackagePreview | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const source = (): PackageSource => mode === 'upload'
    ? { kind: 'upload', package_base64: packageBase64 }
    : { kind: 'github', repository_url: repository.trim(), ref: ref.trim() || undefined, commit: preview?.source_commit }

  const runPreview = async () => {
    setBusy(true); setError(''); setPreview(null)
    try { setPreview(await api.previewPackage(request, source())) } catch (e) { setError(errorMessage(e, '无法读取插件包')) } finally { setBusy(false) }
  }
  const install = async () => {
    if (!preview) return
    setBusy(true); setError('')
    try {
      const result = await api.installPackage(request, source(), preview.sha256)
      onInstalled(result.installation_id, result.review_required)
    } catch (e) { setError(errorMessage(e, '安装失败')) } finally { setBusy(false) }
  }

  const blocked = Boolean(preview?.blocked)
  const footer = <>
    <Button variant="ghost" onClick={onClose}>取消</Button>
    {preview ? <Button busy={busy} disabled={blocked} onClick={() => void install()}>{preview.action === 'update' ? '安装新版本' : '确认安装'}</Button>
      : <Button busy={busy} disabled={mode === 'upload' ? !packageBase64 : !repository.trim()} onClick={() => void runPreview()}>检查插件包</Button>}
  </>

  return <Dialog isOpen onClose={onClose} title="安装插件" size="lg" footer={footer}>
    <div className="plugin-install">
      {!preview && <>
        <div className="plugin-segmented" role="radiogroup" aria-label="安装来源">
          <button type="button" role="radio" aria-checked={mode === 'upload'} className={mode === 'upload' ? 'active' : ''} onClick={() => setMode('upload')}><FileArchive size={15} />上传 .obplugin</button>
          <button type="button" role="radio" aria-checked={mode === 'github'} className={mode === 'github' ? 'active' : ''} onClick={() => setMode('github')}><GitBranch size={15} />GitHub 仓库</button>
        </div>
        {mode === 'upload' ? <label className="plugin-file-drop">
          <input type="file" accept=".obplugin,.zip,application/zip" onChange={async event => {
            const file = event.target.files?.[0]
            if (!file) return
            if (file.size > 8 * 1024 * 1024) { setError('插件包不能超过 8 MiB'); return }
            try { setPackageBase64(await readFileAsBase64(file)); setFileName(file.name); setError('') } catch (e) { setError(errorMessage(e, '读取文件失败')) }
          }} />
          <FileArchive size={22} aria-hidden="true" />
          <span>{fileName || '选择插件包文件'}</span>
        </label> : <div className="plugin-form-grid">
          <label className="plugin-inline-field">仓库地址<Input placeholder="https://github.com/owner/repo" value={repository} onChange={event => setRepository(event.target.value)} /></label>
          <label className="plugin-inline-field">分支、标签或提交（可选）<Input placeholder="main" value={ref} onChange={event => setRef(event.target.value)} /></label>
          <p className="plugin-env-help">安装时锁定到预览时解析出的提交，之后仓库变化不会影响已安装版本。</p>
        </div>}
        <p className="plugin-env-help">检查只解析与校验，不会执行任何插件代码。</p>
      </>}
      {preview && <div className="plugin-preview">
        <div className="plugin-preview-head">
          <div className="min-w-0">
            <h4>{preview.name} <span className="plugin-version">{preview.version}</span></h4>
            <p className="plugin-env-help"><code>{preview.plugin_id}</code>{preview.description ? ` · ${preview.description}` : ''}</p>
          </div>
          {preview.signature_state === 'verified'
            ? <Badge variant="success"><BadgeCheck size={13} />已签名</Badge>
            : <Badge variant="warning">未签名</Badge>}
        </div>
        <dl className="plugin-facts">
          <div><dt>发布者</dt><dd>{publisherLabel(preview.publisher_identity, preview.publisher_name)}</dd></div>
          <div><dt>包摘要</dt><dd><code>{shortHash(preview.sha256)}</code></dd></div>
          {preview.source_repository && <div><dt>来源</dt><dd>{preview.source_repository} @ <code>{shortHash(preview.source_commit)}</code></dd></div>}
          {preview.existing && <div><dt>已安装</dt><dd>{preview.existing.version}</dd></div>}
        </dl>
        {preview.blocked && <div className="plugin-callout danger"><AlertTriangle size={16} aria-hidden="true" /><p>{preview.blocked}</p></div>}
        <section>
          <h4 className="plugin-subhead">申请的权限</h4>
          <PermissionList permissions={preview.permissions} hosts={preview.http_hosts} forbidden={preview.forbidden} />
        </section>
        {preview.existing && <section>
          <h4 className="plugin-subhead">与当前版本相比</h4>
          <DiffList diff={preview.diff} />
          {preview.diff.expanded && <div className="plugin-callout warning"><AlertTriangle size={16} aria-hidden="true" /><p>新版本扩大了权限。激活后，管理员需要重新审核每个实例的授权才能运行。</p></div>}
        </section>}
        {(preview.manifest.environment || []).length > 0 && <section>
          <h4 className="plugin-subhead">需要配置</h4>
          <ul className="plugin-env-summary">
            {(preview.manifest.environment || []).map(field => <li key={field.name}><code>{field.name}</code><span>{field.label}</span><span className="plugin-env-help">{envTypeLabels[field.type] || field.type}{field.required ? ' · 必填' : ''}</span></li>)}
          </ul>
        </section>}
        <Button variant="link" onClick={() => setPreview(null)}>更换插件包</Button>
      </div>}
      {error && <p role="alert" className="plugin-field-error">{error}</p>}
    </div>
  </Dialog>
}
