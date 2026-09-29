import React, { useState } from 'react'
import { BadgeCheck, Code2, Plus, Trash2 } from 'lucide-react'
import { Badge } from '../../components/ui/badge'
import { Button } from '../../components/ui/button'
import { Dialog } from '../../components/ui/dialog'
import { Input } from '../../components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '../../components/ui/tabs'
import { PermissionList } from './PackageInstallDialog'
import { formatTime, instanceStatusLabels, instanceStatusTone, publisherLabel, shortHash } from './domain'
import type { InstallationDetail } from './types'

export interface PluginDetailProps {
  detail: InstallationDetail
  isAdmin: boolean
  canConfigure: boolean
  busy: boolean
  onClose: () => void
  onOpenInstance: (id: number) => void
  onCreateInstance: (name: string) => void
  onToggleEnabled: () => void
  onUninstall: () => void
  onActivate: (packageID: number) => void
  onEdit: () => void
}

export function PluginDetail({ detail, isAdmin, canConfigure, busy, onClose, onOpenInstance, onCreateInstance, onToggleEnabled, onUninstall, onActivate, onEdit }: PluginDetailProps) {
  const [view, setView] = useState('instances')
  const [newName, setNewName] = useState('')
  const [confirmUninstall, setConfirmUninstall] = useState(false)
  const [activateTarget, setActivateTarget] = useState<number | null>(null)

  const footer = <>
    {detail.can_develop && <Button variant="outline" onClick={onEdit}><Code2 size={15} />{detail.has_draft ? '继续编辑草稿' : '编辑源码'}</Button>}
    {isAdmin && detail.published && <Button variant={detail.enabled ? 'ghost' : 'default'} disabled={busy} onClick={onToggleEnabled}>{detail.enabled ? '停用插件' : '启用插件'}</Button>}
    {isAdmin && <Button variant="ghost" className="danger-text" disabled={busy} onClick={() => setConfirmUninstall(true)}><Trash2 size={15} />卸载</Button>}
  </>

  return <Dialog isOpen onClose={onClose} size="lg" placement="right" drawerSize="wide" footer={footer}
    title={<span className="plugin-dialog-title">{detail.name}{detail.version && <span className="plugin-version">{detail.version}</span>}<Badge variant={detail.enabled ? 'success' : 'secondary'}>{detail.enabled ? '已启用' : detail.published ? '已停用' : '草稿'}</Badge></span>}>
    <div className="plugin-detail">
      <p className="plugin-env-help"><code>{detail.plugin_id}</code>{detail.description ? ` · ${detail.description}` : ''}</p>
      <Tabs value={view} onValueChange={setView}>
        <TabsList className="plugin-tabs">
          <TabsTrigger value="instances">实例</TabsTrigger>
          <TabsTrigger value="overview">权限与来源</TabsTrigger>
          <TabsTrigger value="versions">版本</TabsTrigger>
          {detail.readme && <TabsTrigger value="readme">说明</TabsTrigger>}
        </TabsList>
        <TabsContent value="instances">
          {!detail.published && <p className="plugin-env-help">发布后才能创建可运行的实例。</p>}
          {detail.instance_details.length === 0 ? <p className="text-sm text-muted-foreground">还没有实例。一个插件可以有多个实例，各自配置与授权。</p> : <ul className="plugin-record-list">
            {detail.instance_details.map(instance => <li key={instance.id}><button type="button" className="plugin-record-row plugin-record-button" onClick={() => onOpenInstance(instance.id)}>
              <div className="min-w-0">
                <strong>{instance.name}</strong>
                <p className="plugin-env-help">上次运行 {formatTime(instance.last_run_at)}{instance.schedules?.length ? ` · ${instance.schedules.length} 个计划` : ''}</p>
              </div>
              <Badge variant={instanceStatusTone(instance.status)}>{instanceStatusLabels[instance.status] || instance.status}</Badge>
            </button></li>)}
          </ul>}
          {canConfigure && detail.published && <form className="plugin-rename" onSubmit={event => { event.preventDefault(); if (newName.trim()) { onCreateInstance(newName.trim()); setNewName('') } }}>
            <label className="plugin-inline-field">新实例名称<Input value={newName} maxLength={80} placeholder="例如：香港节点巡检" onChange={event => setNewName(event.target.value)} /></label>
            <Button type="submit" size="sm" variant="outline" disabled={busy || !newName.trim()}><Plus size={14} />新建实例</Button>
          </form>}
        </TabsContent>
        <TabsContent value="overview">
          <dl className="plugin-facts">
            <div><dt>发布者</dt><dd>{detail.signature_state === 'verified' && <BadgeCheck size={13} className="inline mr-1" aria-label="已签名" />}{publisherLabel(detail.publisher_identity, detail.publisher_name)}</dd></div>
            <div><dt>来源</dt><dd>{detail.source_kind === 'github' ? `${detail.source_repository} @ ${shortHash(detail.source_commit)}` : detail.source_kind === 'editor' ? '面板编辑器' : '上传的插件包'}</dd></div>
            {detail.manifest && <div><dt>运行时</dt><dd>{detail.manifest.runtime} · {detail.manifest.entry}</dd></div>}
          </dl>
          <h4 className="plugin-subhead">声明的能力</h4>
          <PermissionList permissions={detail.permissions} hosts={detail.http_hosts} forbidden={detail.forbidden} />
          {detail.license && <details className="plugin-details"><summary>许可证</summary><pre className="plugin-pre">{detail.license}</pre></details>}
        </TabsContent>
        <TabsContent value="versions">
          <ul className="plugin-record-list">
            {detail.versions.map(version => <li key={version.package_id} className="plugin-record-row">
              <div className="min-w-0">
                <strong>{version.version}</strong>{version.active && <Badge variant="success" className="ml-2">当前</Badge>}
                <p className="plugin-env-help">{formatTime(version.created_at)} · <code>{shortHash(version.sha256)}</code> · {version.signature_state === 'verified' ? '已签名' : '未签名'}</p>
              </div>
              {isAdmin && !version.active && <Button size="sm" variant="outline" disabled={busy} onClick={() => setActivateTarget(version.package_id)}>切换到此版本</Button>}
            </li>)}
          </ul>
          {!detail.versions.length && <p className="text-sm text-muted-foreground">还没有发布的版本。</p>}
        </TabsContent>
        {detail.readme && <TabsContent value="readme"><pre className="plugin-pre plugin-readme">{detail.readme}</pre></TabsContent>}
      </Tabs>
    </div>
    <Dialog isOpen={confirmUninstall} onClose={() => setConfirmUninstall(false)} title={`卸载 ${detail.name}`} size="sm"
      footer={<><Button variant="ghost" onClick={() => setConfirmUninstall(false)}>取消</Button><Button variant="destructive" busy={busy} onClick={() => { setConfirmUninstall(false); onUninstall() }}>卸载</Button></>}>
      <p className="text-sm">将删除所有实例、授权、密钥、状态与计划，进行中的执行会被取消。执行记录保留到过期。</p>
    </Dialog>
    <Dialog isOpen={activateTarget !== null} onClose={() => setActivateTarget(null)} title="切换版本" size="sm"
      footer={<><Button variant="ghost" onClick={() => setActivateTarget(null)}>取消</Button><Button busy={busy} onClick={() => { if (activateTarget) onActivate(activateTarget); setActivateTarget(null) }}>切换</Button></>}>
      <p className="text-sm">实例配置会按新版本清单校验。新版本若申请了更多权限，所有实例在管理员审核前暂停运行。</p>
    </Dialog>
  </Dialog>
}
