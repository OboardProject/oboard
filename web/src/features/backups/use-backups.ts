import { useEffect, useRef, useState } from 'react'
import { emptySettings, backupStatus } from './domain'
import { backupAPI } from './api'
import { formatBytes } from '../../shared/presentation'
import type { BackupDestination, ControllerBackup, ControllerBackupSettings, ControllerBackupSnapshot, ControllerUpdateBackup, BackupProps } from './types'

export function useBackups({ client, notify, dialogs, localizeErrorMessage }: BackupProps) {
  const api = backupAPI(client)
  const [snapshot, setSnapshot] = useState<ControllerBackupSnapshot>({ settings: emptySettings, backups: [], update_backups: [] })
  const [draft, setDraft] = useState<ControllerBackupSettings>(emptySettings)
  const [updateBackupDetail, setUpdateBackupDetail] = useState<ControllerUpdateBackup | null>(null)
  const [recoveryPassword, setRecoveryPassword] = useState('')
  const [recoveryPasswordConfirm, setRecoveryPasswordConfirm] = useState('')
  const [s3AccessKey, setS3AccessKey] = useState('')
  const [s3SecretKey, setS3SecretKey] = useState('')
  const [webdavUsername, setWebdavUsername] = useState('')
  const [webdavPassword, setWebdavPassword] = useState('')
  const [uploadPassword, setUploadPassword] = useState('')
  const [settingsDialogOpen, setSettingsDialogOpen] = useState(false)
  const [passwordDialogOpen, setPasswordDialogOpen] = useState(false)
  const [uploadDialogOpen, setUploadDialogOpen] = useState(false)
  const [uploadFile, setUploadFile] = useState<File | null>(null)
  const [uploadDragActive, setUploadDragActive] = useState(false)
  const [uploadValidationError, setUploadValidationError] = useState<'file' | 'password' | ''>('')
  const [passwordValidationError, setPasswordValidationError] = useState('')
  const [working, setWorking] = useState('')
  const uploadRef = useRef<HTMLInputElement>(null)
  const uploadDropRef = useRef<HTMLButtonElement>(null)
  const uploadPasswordRef = useRef<HTMLInputElement>(null)
  const refresh = async (quiet = false) => {
    if (!quiet) setWorking('load')
    try {
      const result = await api.list()
      setSnapshot(result)
      setDraft(result.settings || emptySettings)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      if (!quiet) setWorking('')
    }
  }
  useEffect(() => { void refresh() }, [])
  const saveSettings = async () => {
    if (working) return
    if (draft.destination?.enabled && !draft.destination.provider) {
      notify?.('请选择第三方备份的存储类型', 'error')
      return
    }
    if (!snapshot.settings?.password_configured) {
      notify?.('请先在备份密码中设置恢复密码', 'error')
      return
    }
    setWorking('save')
    try {
      const result = await client.request('/backups/settings', {
        method: 'PUT',
        body: JSON.stringify({
          enabled: draft.enabled,
          schedule: draft.schedule,
          time: draft.time,
          weekday: draft.weekday,
          local_retention: draft.local_retention,
          remote_retention: draft.remote_retention,
          update_retention: draft.update_retention,
          destination: draft.destination,
          s3_access_key: s3AccessKey,
          s3_secret_key: s3SecretKey,
          webdav_username: webdavUsername,
          webdav_password: webdavPassword,
        }),
      }) as { settings: ControllerBackupSettings }
      setSnapshot(previous => ({ ...previous, settings: result.settings }))
      setDraft(result.settings)
      // Refresh update backups after retention change may have triggered auto-delete.
      void refresh(true)
      setS3AccessKey('')
      setS3SecretKey('')
      setWebdavUsername('')
      setWebdavPassword('')
      notify?.('备份设置已保存', 'success')
      setSettingsDialogOpen(false)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const saveRecoveryPassword = async () => {
    if (working) return
    if (recoveryPassword.length < 6) {
      setPasswordValidationError('恢复密码至少需要 6 个字符。')
      return
    }
    if (recoveryPassword !== recoveryPasswordConfirm) {
      setPasswordValidationError('两次输入的恢复密码不一致。')
      return
    }
    setPasswordValidationError('')
    setWorking('password')
    try {
      const settings = snapshot.settings || emptySettings
      const result = await client.request('/backups/settings', {
        method: 'PUT',
        body: JSON.stringify({
          enabled: settings.enabled,
          schedule: settings.schedule,
          time: settings.time,
          weekday: settings.weekday,
          local_retention: settings.local_retention,
          remote_retention: settings.remote_retention,
          update_retention: settings.update_retention,
          destination: settings.destination,
          recovery_password: recoveryPassword,
        }),
      }) as { settings: ControllerBackupSettings }
      setSnapshot(previous => ({ ...previous, settings: result.settings }))
      setDraft(current => ({ ...current, password_configured: result.settings.password_configured }))
      setRecoveryPassword('')
      setRecoveryPasswordConfirm('')
      setPasswordDialogOpen(false)
      notify?.(settings.password_configured ? '备份恢复密码已更换' : '备份恢复密码已设置', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const testDestination = async () => {
    setWorking('test')
    try {
      await client.request('/backups/settings/test', {
        method: 'POST',
        body: JSON.stringify({
          destination: draft.destination,
          s3_access_key: s3AccessKey,
          s3_secret_key: s3SecretKey,
          webdav_username: webdavUsername,
          webdav_password: webdavPassword,
        }),
      })
      notify?.('第三方备份目标连接成功', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const createBackup = async () => {
    if (working) return
    if (!snapshot.settings?.password_configured && !draft.password_configured) {
      notify?.('请先设置备份恢复密码', 'error')
      setPasswordValidationError('')
      setPasswordDialogOpen(true)
      return
    }
    setWorking('create')
    try {
      const result = await client.request('/backups', { method: 'POST', body: JSON.stringify({ upload_remote: true }) }) as { backup: ControllerBackup }
      notify?.('备份已开始，正在后台创建', 'info')
      for (let attempt = 0; attempt < 150; attempt++) {
        await new Promise(resolve => window.setTimeout(resolve, 2000))
        const next = await api.list()
        setSnapshot(next)
        setDraft(next.settings || emptySettings)
        const item = next.backups?.find((entry: ControllerBackup) => entry.id === result.backup?.id)
        if (item?.local_status === 'failed') {
          throw new Error(next.settings?.last_error || '备份创建失败，请稍后重试')
        }
        if (item && item.local_status !== 'pending') {
          notify?.(item.remote_status === 'failed' ? '本地备份已创建，但第三方上传失败' : '备份已创建', item.remote_status === 'failed' ? 'error' : 'success')
          return
        }
      }
      throw new Error('备份仍在后台执行，请稍后在备份记录中查看')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const downloadBackup = async (item: ControllerBackup) => {
    setWorking(`download-${item.id}`)
    try {
      const file = await api.download(item.id)
      const url = URL.createObjectURL(file.blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = file.filename
      anchor.click()
      URL.revokeObjectURL(url)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const restoreBackup = async (item: ControllerBackup, password = '', alreadyConfirmed = false) => {
    const recoveryPasswordForRestore = password || await dialogs.prompt({
      title: item.local_status === 'available' ? '恢复备份' : '取回并恢复备份',
      message: '请输入创建这份备份时使用的恢复密码。密码验证通过后才会开始恢复。',
      placeholder: '该备份的恢复密码',
      inputType: 'password',
      confirmText: '继续',
    })
    if (!recoveryPasswordForRestore) return
    if (!alreadyConfirmed) {
      const confirmed = await dialogs.confirm({
        title: '恢复主控数据？',
        message: '恢复前会创建保护备份。主控将重启，当前登录会话失效，并重新下发恢复后的节点配置。',
        confirmText: '备份并恢复',
        tone: 'danger',
      })
      if (!confirmed) return
    }
    setWorking(`restore-${item.id}`)
    try {
      await api.restore(item.id, recoveryPasswordForRestore)
      notify?.('备份已验证，主控正在重启恢复数据', 'success')
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
      setWorking('')
    }
  }
  const removeBackup = async (item: ControllerBackup) => {
    const remoteDeleteMessage = item.remote_status === 'available' && !item.remote_retrievable
      ? '当前第三方目标与这条记录不一致。这里只会删除本地记录，远端文件需要到旧存储中手动删除。'
      : item.remote_status === 'available' ? '本地副本和第三方副本都会删除。' : '本地副本和备份记录都会删除。'
    const deleteMessage = item.protected ? `这是恢复前创建的保护备份。${remoteDeleteMessage}` : remoteDeleteMessage
    const confirmed = await dialogs.confirm({ title: '删除备份？', message: deleteMessage, confirmText: '删除', tone: 'danger' })
    if (!confirmed) return
    setWorking(`delete-${item.id}`)
    try {
      const result = await client.request(`/backups/${item.id}`, { method: 'DELETE' }) as { message?: string }
      notify?.(result.message || '备份已删除', 'success')
      await refresh(true)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const viewUpdateBackup = async (item: ControllerUpdateBackup) => {
    setUpdateBackupDetail(item)
  }
  const downloadUpdateBackup = async (item: ControllerUpdateBackup) => {
    setWorking(`download-update-${item.name}`)
    try {
      const file = await client.download(`/controller-update/backups/${encodeURIComponent(item.name)}/download`)
      const url = URL.createObjectURL(file.blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = file.filename || item.name
      anchor.click()
      URL.revokeObjectURL(url)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const removeUpdateBackup = async (item: ControllerUpdateBackup) => {
    const confirmed = await dialogs.confirm({
      title: '删除更新前备份？',
      message: `将删除文件 ${item.name}（${formatBytes(Number(item.size_bytes || 0))}）。${item.is_latest ? '这是最近一次更新前的备份，删除后将无法通过该文件回滚。' : ''}`,
      confirmText: '删除',
      tone: 'danger',
    })
    if (!confirmed) return
    setWorking(`delete-update-${item.name}`)
    try {
      const result = await client.request(`/controller-update/backups/${encodeURIComponent(item.name)}`, { method: 'DELETE' }) as { message?: string }
      notify?.(result.message || '更新前备份已删除', 'success')
      await refresh(true)
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      setWorking('')
    }
  }
  const uploadBackup = async () => {
    const file = uploadFile
    if (!file) {
      setUploadValidationError('file')
      uploadDropRef.current?.focus()
      return
    }
    if (!uploadPassword) {
      setUploadValidationError('password')
      uploadPasswordRef.current?.focus()
      return
    }
    setUploadValidationError('')
    setWorking('upload')
    let restoreStarted = false
    try {
      const form = new FormData()
      form.set('backup', file)
      form.set('recovery_password', uploadPassword)
      const result = await api.upload(form)
      await refresh(true)
      setUploadDialogOpen(false)
      setUploadFile(null)
      setUploadPassword('')
      const confirmed = await dialogs.confirm({
        title: '立即恢复上传的备份？',
        message: `该备份来自 ${result.inspection?.manifest?.source_version || '未知版本'}。选择稍后恢复会保留文件，之后可从备份列表恢复。`,
        confirmText: '立即恢复',
        cancelText: '只保存',
        tone: 'danger',
      })
      if (confirmed) {
        restoreStarted = true
        await restoreBackup(result.backup, form.get('recovery_password') as string, true)
      } else {
        notify?.('备份已保存，尚未恢复', 'success')
      }
    } catch (error: any) {
      notify?.(localizeErrorMessage(error?.message || error), 'error')
    } finally {
      if (!restoreStarted) setWorking('')
    }
  }
  const clearSettingsSecrets = () => {
    setS3AccessKey('')
    setS3SecretKey('')
    setWebdavUsername('')
    setWebdavPassword('')
  }
  const openSettingsDialog = () => {
    const settings = snapshot.settings || emptySettings
    setDraft({ ...settings, destination: { ...(settings.destination || emptySettings.destination) } })
    clearSettingsSecrets()
    setSettingsDialogOpen(true)
  }
  const closeSettingsDialog = () => {
    if (working) return
    const settings = snapshot.settings || emptySettings
    setDraft({ ...settings, destination: { ...(settings.destination || emptySettings.destination) } })
    clearSettingsSecrets()
    setSettingsDialogOpen(false)
  }
  const openPasswordDialog = () => {
    setRecoveryPassword('')
    setRecoveryPasswordConfirm('')
    setPasswordValidationError('')
    setPasswordDialogOpen(true)
  }
  const closePasswordDialog = () => {
    if (working) return
    setRecoveryPassword('')
    setRecoveryPasswordConfirm('')
    setPasswordValidationError('')
    setPasswordDialogOpen(false)
  }
  const chooseUploadFile = (file?: File) => {
    if (!file) return
    setUploadFile(file)
    setUploadValidationError(current => current === 'file' ? '' : current)
  }
  const openUploadDialog = () => {
    setUploadFile(null)
    setUploadPassword('')
    setUploadDragActive(false)
    setUploadValidationError('')
    setUploadDialogOpen(true)
  }
  const closeUploadDialog = () => {
    if (working) return
    setUploadFile(null)
    setUploadPassword('')
    setUploadDragActive(false)
    setUploadValidationError('')
    setUploadDialogOpen(false)
  }
  const updateDestination = (patch: Partial<BackupDestination>) => setDraft(current => ({ ...current, destination: { ...current.destination, ...patch } }))
  const destination = draft.destination || emptySettings.destination
  const weekdayNames = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']
  const savedSettings = snapshot.settings || emptySettings
  const savedDestination = savedSettings.destination || emptySettings.destination
  const savedDestinationName = savedDestination.provider === 's3' ? 'S3 兼容存储' : savedDestination.provider === 'webdav' ? 'WebDAV' : '第三方存储'
  const scheduleDescription = savedSettings.enabled
    ? `${savedSettings.schedule === 'weekly' ? `每${weekdayNames[savedSettings.weekday] || '周日'}` : '每天'} ${savedSettings.time || '03:00'} 自动创建，本地保留 ${savedSettings.local_retention || 1} 份。`
    : '当前只会在您点击“创建备份”时备份。'
  return { snapshot, draft, setDraft, updateBackupDetail, setUpdateBackupDetail, recoveryPassword, setRecoveryPassword, recoveryPasswordConfirm, setRecoveryPasswordConfirm, s3AccessKey, setS3AccessKey, s3SecretKey, setS3SecretKey, webdavUsername, setWebdavUsername, webdavPassword, setWebdavPassword, uploadPassword, setUploadPassword, settingsDialogOpen, passwordDialogOpen, uploadDialogOpen, uploadFile, uploadDragActive, setUploadDragActive, uploadValidationError, setUploadValidationError, passwordValidationError, setPasswordValidationError, working, uploadRef, uploadDropRef, uploadPasswordRef, refresh, saveSettings, saveRecoveryPassword, testDestination, createBackup, downloadBackup, restoreBackup, removeBackup, viewUpdateBackup, downloadUpdateBackup, removeUpdateBackup, uploadBackup, openSettingsDialog, closeSettingsDialog, openPasswordDialog, closePasswordDialog, chooseUploadFile, openUploadDialog, closeUploadDialog, updateDestination, destination, weekdayNames, savedSettings, savedDestination, savedDestinationName, scheduleDescription, backupStatus }
}
