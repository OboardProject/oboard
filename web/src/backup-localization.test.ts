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
    expect(main).toContain('setSettingsDialogOpen(true)')
    expect(main).toContain('backup-password-warning')
    expect(main).toContain('尚未设置恢复密码，请先设置至少 12 位的恢复密码后再创建备份。')
    expect(main).toContain('备份已开始，正在后台创建')
    expect(main).toContain('备份仍在后台执行，请稍后在备份记录中查看')
    expect(main).toContain('<button onClick={() => void createBackup()} disabled={Boolean(working)}>')
  })
})
