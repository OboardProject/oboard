import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const main = readFileSync(new URL('./main.tsx', import.meta.url), 'utf8')

describe('backup and update localization', () => {
  it('maps common filesystem errors to Chinese before rendering', () => {
    expect(main).toContain("['no space left on device', '磁盘空间不足']")
    expect(main).toContain("['read-only file system', '磁盘为只读状态，无法写入']")
    expect(main).toContain("['disk quota exceeded', '磁盘配额已用完']")
    expect(main).toContain("['permission denied', '没有操作权限']")
  })

  it('applies the mapping to update and backup error surfaces', () => {
    expect(main).toContain('localizeErrorMessage(snapshot.last_error)')
    expect(main).toContain('localizeErrorMessage(snapshot.settings.last_error)')
    expect(main).toContain('localizeErrorMessage(item.remote_error)')
    expect(main).toContain('setFailure(localizeErrorMessage(result.last_error')
    expect(main).toContain('setInstallFailure(localizeErrorMessage(result.last_error')
    expect(main).toContain('{localizeErrorMessage(failure ||')
  })

  it('prompts for a recovery password before creating a manual backup', () => {
    expect(main).toContain("if (!snapshot.settings?.password_configured && !draft.password_configured) {")
    expect(main).toContain("notify?.('请先设置备份恢复密码', 'error')")
    expect(main).toContain('setPasswordDialogOpen(true)')
    expect(main).toContain('<h3>备份密码</h3>')
    expect(main).toContain("savedSettings.password_configured ? '更换密码' : '设置密码'")
    expect(main).toContain('备份已开始，正在后台创建')
    expect(main).toContain('备份仍在后台执行，请稍后在备份记录中查看')
    expect(main).toContain('<button onClick={() => void createBackup()} disabled={Boolean(working)}>')
  })

  it('keeps password setup separate from automatic backup settings', () => {
    const settingsDialogStart = main.indexOf('className="backup-settings-dialog"')
    const settingsDialogEnd = main.indexOf('</MotionDialogPanel>', settingsDialogStart)
    const settingsDialog = main.slice(settingsDialogStart, settingsDialogEnd)
    expect(settingsDialog).not.toContain('恢复密码')
    expect(settingsDialog).not.toContain('recoveryPassword')
    expect(main).toContain('className="backup-password-dialog"')
    expect(main).toContain('更换密码只影响之后创建的备份')
  })

  it('uploads from a password dialog with click and drag file selection', () => {
    expect(main).toContain('className="backup-upload-dialog"')
    expect(main).toContain('id="backup-upload-password"')
    expect(main).toContain('onClick={() => uploadRef.current?.click()}')
    expect(main).toContain('onDragOver={event =>')
    expect(main).toContain('onDrop={event =>')
    expect(main).toContain('点击选择，或将 .obk 文件拖到此处')
    expect(main).toContain("inputType: 'password'")
  })

  it('defaults controller update confirm to install without backup', () => {
    expect(main).toContain('onClick={() => onInstall(true)}>安装更新')
    expect(main).toContain('onClick={() => onInstall(false)}>备份并更新')
    expect(main).toContain('body: JSON.stringify({ skip_backup: Boolean(skipBackup) })')
    expect(main).toContain('没有其他可用备份时，建议选择备份并更新。')
    expect(main).toContain('默认不备份数据库')
  })
})
