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
  })
})
