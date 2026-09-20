import { AnimatePresence } from 'motion/react'
import { ArrowUp, ArrowUpCircle, CalendarSync, Database, Download, Eye, Info, KeyRound, RefreshCw, Settings2, Trash2 } from 'lucide-react'
import { MotionDialogPanel } from '../../components/ui/motion'
import { FormField, FieldHelp } from '../../components/ui/form-field'
import { Switch } from '../../components/ui/switch'
import { Select } from '../../components/ui/select'
import { formatBytes, formatDate } from '../../shared/presentation'
import { useBackups } from './use-backups'
import type { BackupDestination, BackupProps } from './types'

function XIcon() {
  return <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M18 6 6 18" /><path d="m6 6 12 12" /></svg>
}

export function ControllerBackupPanel(props: BackupProps) {
  const { localizeErrorMessage } = props
  const { snapshot, draft, setDraft, updateBackupDetail, setUpdateBackupDetail, recoveryPassword, setRecoveryPassword, recoveryPasswordConfirm, setRecoveryPasswordConfirm, s3AccessKey, setS3AccessKey, s3SecretKey, setS3SecretKey, webdavUsername, setWebdavUsername, webdavPassword, setWebdavPassword, uploadPassword, setUploadPassword, settingsDialogOpen, passwordDialogOpen, uploadDialogOpen, uploadFile, uploadDragActive, setUploadDragActive, uploadValidationError, setUploadValidationError, passwordValidationError, setPasswordValidationError, working, uploadRef, uploadDropRef, uploadPasswordRef, refresh, saveSettings, saveRecoveryPassword, testDestination, createBackup, downloadBackup, restoreBackup, removeBackup, viewUpdateBackup, downloadUpdateBackup, removeUpdateBackup, uploadBackup, openSettingsDialog, closeSettingsDialog, openPasswordDialog, closePasswordDialog, chooseUploadFile, openUploadDialog, closeUploadDialog, updateDestination, destination, weekdayNames, savedSettings, savedDestination, savedDestinationName, scheduleDescription, backupStatus } = useBackups(props)
  return <>
  <section className="settings-card controller-backup-card">
    <div className="settings-card-head">
      <div className="settings-heading"><h3>主控数据备份</h3><FieldHelp label="主控数据备份" hint="备份用户数据、证书和配置，不包含日志和程序文件。" placement="bottom" /></div>
      <div className="backup-card-head-actions"><span className={`status-pill ${snapshot.settings?.last_error ? 'danger' : 'ok'}`}>{working === 'load' ? '正在读取' : snapshot.settings?.last_error ? '需要处理' : '已就绪'}</span><button type="button" className="ghost" onClick={openSettingsDialog} disabled={Boolean(working)}><Settings2 size={15} />自动备份设置</button></div>
    </div>
    {snapshot.settings?.last_error && <div className="controller-update-error" role="alert">{localizeErrorMessage(snapshot.settings.last_error)}</div>}
    <div className="backup-settings-summary">
      <span className={`backup-settings-summary-icon${savedSettings.enabled ? ' active' : ''}`}><CalendarSync size={18} /></span>
      <div><strong>{savedSettings.enabled ? '自动备份已开启' : '自动备份未开启'}</strong><span>{scheduleDescription}</span><small>{savedDestination.enabled ? `新备份会同时上传到${savedDestinationName}，远端保留 ${savedSettings.remote_retention || 1} 份。` : '第三方备份未启用，新备份只保存在本机。'}</small></div>
    </div>
    <section className="backup-password-setting">
      <div className="settings-heading"><h3>备份密码</h3><FieldHelp label="备份密码" hint="用于加密新备份；恢复时须输入创建该备份时的密码。" placement="bottom" /></div>
      <div className="backup-password-setting-actions"><span className={`status-pill ${savedSettings.password_configured ? 'ok' : 'warning'}`}>{savedSettings.password_configured ? '已设置' : '未设置'}</span><button type="button" className="ghost" onClick={openPasswordDialog} disabled={Boolean(working)}><KeyRound size={15} />{savedSettings.password_configured ? '更换密码' : '设置密码'}</button></div>
    </section>
    <div className="backup-actions"><div><strong>立即备份</strong><span>本地备份完成后，会上传到已启用的第三方目标。</span></div><button onClick={() => void createBackup()} disabled={Boolean(working)}><Database size={15} />{working === 'create' ? '备份中...' : '创建备份'}</button></div>
    <section className="backup-import">
      <div className="settings-heading"><h3>导入备份</h3><FieldHelp label="导入备份" hint="上传已有备份，验证密码后可恢复。" placement="bottom" /></div>
      <button type="button" className="ghost" onClick={openUploadDialog} disabled={Boolean(working)}><ArrowUp size={15} />上传备份</button>
    </section>
    <section className="backup-records">
      <div className="settings-card-head"><div className="settings-heading"><h3>备份记录</h3><FieldHelp label="备份记录" hint="恢复前会创建保护备份，保护备份不会被自动清理。" placement="bottom" /></div><button className="ghost icon-button" onClick={() => void refresh()} disabled={Boolean(working)} title="刷新备份记录" aria-label="刷新备份记录"><RefreshCw size={15} className={working === 'load' ? 'spin' : ''} /></button></div>
      {snapshot.backups?.length ? <div className="backup-record-list">{snapshot.backups.map(item => <div className="backup-record" key={item.id}><div className="backup-record-main"><strong>{item.origin === 'automatic' ? '自动备份' : item.origin === 'uploaded' ? '上传备份' : item.origin === 'pre_restore' ? '恢复前保护备份' : '手动备份'}</strong><span>{formatDate(item.created_at)} · {item.local_status === 'pending' ? '等待后台完成' : formatBytes(Number(item.size_bytes || 0)) + ' · 来源 ' + (item.source_version || '-')}</span>{item.remote_error && <small>{localizeErrorMessage(item.remote_error)}</small>}</div><span className={`status-pill ${item.local_status === 'pending' ? 'warning' : item.remote_status === 'failed' || (item.local_status !== 'available' && !item.remote_retrievable) ? 'danger' : item.protected ? 'warning' : 'ok'}`}>{backupStatus(item)}</span><div className="backup-record-actions">{(item.local_status === 'available' || item.remote_retrievable) && <button type="button" className="ghost icon-button" title={item.local_status === 'available' ? '下载备份' : '从第三方取回并下载'} aria-label={item.local_status === 'available' ? '下载备份' : '从第三方取回并下载'} onClick={() => void downloadBackup(item)} disabled={Boolean(working) || item.local_status === 'pending'}><Download size={15} /></button>}{(item.local_status === 'available' || item.remote_retrievable) && <button type="button" className="ghost" onClick={() => void restoreBackup(item)} disabled={Boolean(working) || item.local_status === 'pending'}>{item.local_status === 'available' ? '恢复' : '取回并恢复'}</button>}<button type="button" className="ghost icon-button danger-text" title="删除备份" aria-label="删除备份" onClick={() => void removeBackup(item)} disabled={Boolean(working) || item.local_status === 'pending'}><Trash2 size={15} /></button></div></div>)}</div> : <p className="muted backup-empty">尚未创建备份。</p>}
    </section>
    <section className="backup-records">
      <div className="settings-card-head"><div className="settings-heading"><h3>更新前备份</h3><FieldHelp label="更新前备份" hint="更新前创建的数据副本。保留 0 份时，更新成功后立即清理。" placement="bottom" /></div><span className="status-pill">{`保留 ${snapshot.settings?.update_retention ?? snapshot.update_retention ?? 2} 份`}</span><button className="ghost icon-button" onClick={() => void refresh(true)} disabled={Boolean(working)} title="刷新更新前备份" aria-label="刷新更新前备份"><RefreshCw size={15} className={working === 'load' ? 'spin' : ''} /></button></div>
      {snapshot.update_backups?.length ? <div className="backup-record-list">{snapshot.update_backups.map(item => <div className="backup-record" key={item.name}><div className="backup-record-main"><strong>{item.is_latest ? '最近更新前备份' : '更新前备份'} {item.is_latest && <span className="status-pill ok" style={{marginLeft:6, fontSize:11}}>最新</span>}</strong><span>{formatDate(item.created_at)} · {formatBytes(Number(item.size_bytes || 0))}{item.target_build ? ` · 目标构建 ${item.target_build}` : ''}</span><small title={item.path} style={{overflowWrap:'anywhere'}}>{item.name}</small></div><span className={`status-pill ${item.is_latest ? 'warning' : 'ok'}`}>{item.is_latest ? '已关联' : '已保留'}</span><div className="backup-record-actions"><button type="button" className="ghost icon-button" title="查看详情" aria-label="查看详情" onClick={() => void viewUpdateBackup(item)} disabled={Boolean(working)}><Eye size={15} /></button><button type="button" className="ghost icon-button" title="下载快照" aria-label="下载快照" onClick={() => void downloadUpdateBackup(item)} disabled={Boolean(working)}><Download size={15} /></button><button type="button" className="ghost icon-button danger-text" title="删除更新前备份" aria-label="删除更新前备份" onClick={() => void removeUpdateBackup(item)} disabled={Boolean(working)}><Trash2 size={15} /></button></div></div>)}</div> : <p className="muted backup-empty">暂无更新前备份，更新时选择“备份”后会自动创建。</p>}
    </section>
  </section>
  <AnimatePresence>{passwordDialogOpen && <MotionDialogPanel onCancel={closePasswordDialog} className="backup-password-dialog" ariaLabel={savedSettings.password_configured ? '更换备份恢复密码' : '设置备份恢复密码'}>
    <header className="dialog-head"><div><h2>{savedSettings.password_configured ? '更换备份密码' : '设置备份密码'}</h2><p className="muted">密码不会显示或找回，请妥善保存。</p></div><button type="button" className="ghost dialog-close icon-button" onClick={closePasswordDialog} disabled={Boolean(working)} aria-label="关闭" title="关闭"><XIcon /></button></header>
    <form id="backup-password-form" onSubmit={event => { event.preventDefault(); void saveRecoveryPassword() }}>
      <div className="dialog-body backup-password-dialog-body">
        {savedSettings.password_configured && <div className="backup-password-change-note"><Info size={16} aria-hidden="true" /><span>更换密码只影响之后创建的备份；已有备份仍需使用原密码恢复。</span></div>}
        <div className="backup-password-fields">
          <div className="form-field"><div className="form-field-meta"><label className="form-field-label" htmlFor="backup-recovery-password">{savedSettings.password_configured ? '新密码' : '恢复密码'}<em aria-label="必填">*</em></label></div><div className="form-field-control"><input id="backup-recovery-password" type="password" minLength={6} required autoComplete="new-password" value={recoveryPassword} onChange={event => { setRecoveryPassword(event.target.value); setPasswordValidationError('') }} aria-describedby="backup-password-help backup-password-error" /></div><small id="backup-password-help" className="backup-field-help">至少 6 个字符，恢复到其他主控时也需要使用。</small></div>
          <div className="form-field"><div className="form-field-meta"><label className="form-field-label" htmlFor="backup-recovery-password-confirm">确认密码<em aria-label="必填">*</em></label></div><div className="form-field-control"><input id="backup-recovery-password-confirm" type="password" minLength={6} required autoComplete="new-password" value={recoveryPasswordConfirm} onChange={event => { setRecoveryPasswordConfirm(event.target.value); setPasswordValidationError('') }} aria-describedby="backup-password-error" /></div></div>
        </div>
        <p id="backup-password-error" className="backup-form-error" role="alert" aria-live="polite">{passwordValidationError}</p>
      </div>
      <footer className="dialog-actions"><button type="button" className="ghost" onClick={closePasswordDialog} disabled={Boolean(working)}>取消</button><button type="submit" disabled={Boolean(working)}>{working === 'password' ? '保存中…' : savedSettings.password_configured ? '更换密码' : '设置密码'}</button></footer>
    </form>
  </MotionDialogPanel>}</AnimatePresence>
  <AnimatePresence>{uploadDialogOpen && <MotionDialogPanel onCancel={closeUploadDialog} className="backup-upload-dialog" ariaLabel="上传备份">
    <header className="dialog-head"><div><h2>上传备份</h2><p className="muted">选择备份文件并输入创建该备份时使用的恢复密码。</p></div><button type="button" className="ghost dialog-close icon-button" onClick={closeUploadDialog} disabled={Boolean(working)} aria-label="关闭" title="关闭"><XIcon /></button></header>
    <form id="backup-upload-form" onSubmit={event => { event.preventDefault(); void uploadBackup() }}>
      <div className="dialog-body backup-upload-dialog-body">
        <input ref={uploadRef} id="backup-upload-file" className="backup-upload-file-input" type="file" accept=".obk,application/octet-stream" onChange={event => { chooseUploadFile(event.target.files?.[0]); event.currentTarget.value = '' }} />
        <button ref={uploadDropRef} type="button" className={`backup-upload-dropzone${uploadDragActive ? ' is-dragging' : ''}${uploadFile ? ' has-file' : ''}`} onClick={() => uploadRef.current?.click()} onDragEnter={event => { event.preventDefault(); setUploadDragActive(true) }} onDragOver={event => { event.preventDefault(); event.dataTransfer.dropEffect = 'copy'; setUploadDragActive(true) }} onDragLeave={event => { event.preventDefault(); setUploadDragActive(false) }} onDrop={event => { event.preventDefault(); setUploadDragActive(false); chooseUploadFile(event.dataTransfer.files?.[0]) }} aria-describedby={`backup-upload-file-help${uploadValidationError === 'file' ? ' backup-upload-error' : ''}`} aria-invalid={uploadValidationError === 'file'} disabled={Boolean(working)}>
          <ArrowUpCircle size={28} aria-hidden="true" /><strong>{uploadFile ? uploadFile.name : uploadDragActive ? '松开即可选择文件' : '选择备份文件'}</strong><span id="backup-upload-file-help">{uploadFile ? `${formatBytes(uploadFile.size)} · 点击可更换文件` : '点击选择，或将 .obk 文件拖到此处'}</span>
        </button>
        <div className="form-field backup-upload-password-field"><div className="form-field-meta"><label className="form-field-label" htmlFor="backup-upload-password">恢复密码<em aria-label="必填">*</em></label></div><div className="form-field-control"><input ref={uploadPasswordRef} id="backup-upload-password" type="password" autoComplete="current-password" value={uploadPassword} onChange={event => { setUploadPassword(event.target.value); setUploadValidationError(current => current === 'password' ? '' : current) }} placeholder="该备份的恢复密码" aria-describedby={`backup-upload-password-help${uploadValidationError === 'password' ? ' backup-upload-error' : ''}`} aria-invalid={uploadValidationError === 'password'} /></div><small id="backup-upload-password-help" className="backup-field-help">上传时会先验证密码和文件完整性，不会立即覆盖当前数据。</small></div>
        <p id="backup-upload-error" className="backup-form-error" role="alert" aria-live="polite">{uploadValidationError === 'file' ? '请选择要上传的备份文件。' : uploadValidationError === 'password' ? '请输入该备份的恢复密码。' : ''}</p>
      </div>
      <footer className="dialog-actions"><button type="button" className="ghost" onClick={closeUploadDialog} disabled={Boolean(working)}>取消</button><button type="submit" disabled={Boolean(working)}><ArrowUp size={15} aria-hidden="true" />{working === 'upload' ? '上传并验证中…' : '上传并验证'}</button></footer>
    </form>
  </MotionDialogPanel>}</AnimatePresence>
  <AnimatePresence>{settingsDialogOpen && <MotionDialogPanel onCancel={closeSettingsDialog} className="backup-settings-dialog">
    <header className="dialog-head"><div><h2>自动备份设置</h2><p className="muted">设置自动创建时间、备份保留数量和第三方存储位置。</p></div><button type="button" className="ghost dialog-close icon-button" onClick={closeSettingsDialog} disabled={Boolean(working)} aria-label="关闭" title="关闭"><XIcon /></button></header>
    <div className="dialog-body backup-settings-dialog-body">
      <form id="backup-settings-form" className="backup-settings-form" onSubmit={event => { event.preventDefault(); void saveSettings() }}>
        <section className="backup-form-section">
          <div className="backup-form-section-head"><div><strong>自动创建</strong><span>开启后，系统会按您选择的时间创建加密备份。</span></div><Switch checked={draft.enabled} onChange={checked => setDraft(current => ({ ...current, enabled: checked }))} ariaLabel="启用自动备份" /></div>
          <div className="backup-dialog-grid">
            <FormField label="备份频率"><Select value={draft.schedule} disabled={!draft.enabled} onChange={event => setDraft(current => ({ ...current, schedule: event.target.value as 'daily' | 'weekly' }))}><option value="daily">每天</option><option value="weekly">每周</option></Select></FormField>
            {draft.schedule === 'weekly' && <FormField label="每周日期"><Select value={draft.weekday} disabled={!draft.enabled} onChange={event => setDraft(current => ({ ...current, weekday: Number(event.target.value) }))}>{weekdayNames.map((label, index) => <option key={label} value={index}>{label}</option>)}</Select></FormField>}
            <FormField label="执行时间" hint="使用“流量控制”中的统计时区"><input type="time" value={draft.time} disabled={!draft.enabled} onChange={event => setDraft(current => ({ ...current, time: event.target.value || '03:00' }))} /></FormField>
            <FormField label="本地保留数量" hint="手动、自动和上传的备份共用此数量">
              <input
                type="number"
                min={1}
                max={100}
                placeholder="1"
                value={(draft.local_retention as any) === '' ? '' : draft.local_retention}
                onChange={event => setDraft(current => ({ ...current, local_retention: event.target.value === '' ? ('' as any) : Number(event.target.value) }))}
                onBlur={event => {
                  const n = Number(event.target.value)
                  if (!event.target.value || isNaN(n) || n < 1) setDraft(c => ({ ...c, local_retention: 1 }))
                  else if (n > 100) setDraft(c => ({ ...c, local_retention: 100 }))
                }}
              />
            </FormField>
            <FormField label="更新前备份保留数量" hint="更新成功后保留的更新前快照数量，0 表示成功后立即删除">
              <input
                type="number"
                min={0}
                max={10}
                placeholder="2"
                value={(draft.update_retention as any) === '' ? '' : draft.update_retention}
                onChange={event => setDraft(current => ({ ...current, update_retention: event.target.value === '' ? ('' as any) : Number(event.target.value) }))}
                onBlur={event => {
                  const n = Number(event.target.value)
                  if (event.target.value === '' || isNaN(n) || n < 0) setDraft(c => ({ ...c, update_retention: 0 }))
                  else if (n > 10) setDraft(c => ({ ...c, update_retention: 10 }))
                }}
              />
            </FormField>
          </div>
        </section>
        <section className="backup-form-section">
          <div className="backup-form-section-head"><div><strong>第三方备份</strong><span>启用后，新备份会同时上传一份到您自己的存储中。</span></div><Switch checked={destination.enabled} onChange={checked => updateDestination({ enabled: checked })} ariaLabel="启用第三方备份" /></div>
          {destination.enabled && <div className="backup-dialog-grid">
            <FormField label="存储类型"><Select value={destination.provider} onChange={event => updateDestination({ provider: event.target.value as BackupDestination['provider'] })}><option value="">请选择</option><option value="s3">S3 兼容存储</option><option value="webdav">WebDAV</option></Select></FormField>
            <FormField label="第三方存储保留数量" hint="达到数量后，只清理当前存储位置中的旧备份">
              <input
                type="number"
                min={1}
                max={365}
                placeholder="1"
                value={(draft.remote_retention as any) === '' ? '' : draft.remote_retention}
                onChange={event => setDraft(current => ({ ...current, remote_retention: event.target.value === '' ? ('' as any) : Number(event.target.value) }))}
                onBlur={event => {
                  const n = Number(event.target.value)
                  if (!event.target.value || isNaN(n) || n < 1) setDraft(c => ({ ...c, remote_retention: 1 }))
                  else if (n > 365) setDraft(c => ({ ...c, remote_retention: 365 }))
                }}
              />
            </FormField>
            {destination.provider && <><FormField label={destination.provider === 'webdav' ? 'WebDAV 地址' : '服务地址'} hint="建议使用 HTTPS 地址"><input required value={destination.endpoint || ''} onChange={event => updateDestination({ endpoint: event.target.value })} placeholder={destination.provider === 'webdav' ? 'https://dav.example.com/oboard' : 'https://s3.example.com'} /></FormField>
            <FormField label="目录前缀" hint="系统只会管理此目录下由 OBoard 创建的备份"><input value={destination.prefix || ''} onChange={event => updateDestination({ prefix: event.target.value })} placeholder="oboard-backups" /></FormField></>}
            {destination.provider === 's3' && <><FormField label="存储桶"><input required value={destination.bucket || ''} onChange={event => updateDestination({ bucket: event.target.value })} /></FormField><FormField label="区域"><input value={destination.region || ''} onChange={event => updateDestination({ region: event.target.value })} placeholder="us-east-1" /></FormField><FormField label="访问密钥"><input type="password" autoComplete="new-password" value={s3AccessKey} onChange={event => setS3AccessKey(event.target.value)} placeholder={draft.destination_configured ? '留空保持当前值' : ''} /></FormField><FormField label="访问密钥密码"><input type="password" autoComplete="new-password" value={s3SecretKey} onChange={event => setS3SecretKey(event.target.value)} placeholder={draft.destination_configured ? '留空保持当前值' : ''} /></FormField><div className="switch-form-row backup-path-style"><span><strong>使用路径风格地址</strong><small>存储服务要求存储桶名称出现在地址路径中时开启。</small></span><Switch checked={Boolean(destination.force_path_style)} onChange={checked => updateDestination({ force_path_style: checked })} ariaLabel="使用路径风格地址" /></div></>}
            {destination.provider === 'webdav' && <><FormField label="用户名"><input value={webdavUsername} autoComplete="username" onChange={event => setWebdavUsername(event.target.value)} placeholder={draft.destination_configured ? '留空保持当前值' : ''} /></FormField><FormField label="密码"><input type="password" autoComplete="new-password" value={webdavPassword} onChange={event => setWebdavPassword(event.target.value)} placeholder={draft.destination_configured ? '留空保持当前值' : ''} /></FormField></>}
          </div>}
          {destination.enabled && <p className="backup-destination-note">更换存储位置后，旧位置中的备份不会被自动删除。</p>}
        </section>
      </form>
    </div>
    <footer className="dialog-actions backup-settings-dialog-actions"><button type="button" className="ghost" onClick={() => void testDestination()} disabled={Boolean(working) || !destination.enabled || !destination.provider}>{working === 'test' ? '测试中…' : '测试连接'}</button><span /><button type="button" className="ghost" onClick={closeSettingsDialog} disabled={Boolean(working)}>取消</button><button type="submit" form="backup-settings-form" disabled={Boolean(working)}>{working === 'save' ? '保存中…' : '保存设置'}</button></footer>
  </MotionDialogPanel>}</AnimatePresence>
  <AnimatePresence>{updateBackupDetail && <MotionDialogPanel onCancel={() => setUpdateBackupDetail(null)} className="backup-password-dialog" ariaLabel="更新前备份详情">
    <header className="dialog-head"><div><h2>更新前备份详情</h2><p className="muted">查看快照文件信息，必要时可下载或删除。</p></div><button type="button" className="ghost dialog-close icon-button" onClick={() => setUpdateBackupDetail(null)} aria-label="关闭" title="关闭"><XIcon /></button></header>
    <div className="dialog-body backup-password-dialog-body" style={{gap:14}}>
      <div style={{display:'grid', gap:10}}>
        <div><strong>文件名</strong><div style={{marginTop:4, color:'var(--text-secondary)', fontSize:13, overflowWrap:'anywhere'}}>{updateBackupDetail.name}</div></div>
        <div><strong>路径</strong><div title={updateBackupDetail.path} style={{marginTop:4, color:'var(--muted)', fontSize:12, overflowWrap:'anywhere'}}>{updateBackupDetail.path}</div></div>
        <div style={{display:'grid', gridTemplateColumns:'1fr 1fr', gap:12}}>
          <div><strong>大小</strong><div style={{marginTop:4, color:'var(--text-secondary)'}}>{formatBytes(Number(updateBackupDetail.size_bytes || 0))}</div></div>
          <div><strong>状态</strong><div style={{marginTop:4}}><span className={`status-pill ${updateBackupDetail.is_latest ? 'warning' : 'ok'}`}>{updateBackupDetail.is_latest ? '最新关联' : '已保留'}</span></div></div>
        </div>
        <div style={{display:'grid', gridTemplateColumns:'1fr 1fr', gap:12}}>
          <div><strong>创建时间</strong><div style={{marginTop:4, color:'var(--text-secondary)', fontSize:13}}>{formatDate(updateBackupDetail.created_at)}</div></div>
          <div><strong>修改时间</strong><div style={{marginTop:4, color:'var(--text-secondary)', fontSize:13}}>{formatDate(updateBackupDetail.mod_time)}</div></div>
        </div>
        {updateBackupDetail.target_build && <div><strong>目标构建</strong><div style={{marginTop:4, color:'var(--text-secondary)', fontFamily:'monospace', fontSize:13}}>{updateBackupDetail.target_build}</div></div>}
        <div style={{padding:'10px 12px', background:'var(--surface-2)', border:'1px solid var(--border)', borderRadius:'var(--radius-md)', fontSize:12, color:'var(--muted)', lineHeight:1.6}}>更新前备份为原始 SQLite 快照，保留用于更新失败时手动恢复。删除后无法找回。</div>
      </div>
    </div>
    <footer className="dialog-actions"><button type="button" className="ghost" onClick={() => setUpdateBackupDetail(null)}>关闭</button><button type="button" className="ghost" onClick={() => { const item = updateBackupDetail; setUpdateBackupDetail(null); if (item) void downloadUpdateBackup(item) }}>下载</button><button type="button" className="ghost danger-text" onClick={() => { const item = updateBackupDetail; setUpdateBackupDetail(null); if (item) void removeUpdateBackup(item) }}>删除</button></footer>
  </MotionDialogPanel>}</AnimatePresence>
  </>
}
