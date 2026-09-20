// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { useBackups } from './use-backups'
import { emptySettings } from './domain'
import type { BackupProps } from './types'

it('keeps uploaded-password reuse behind confirmation and clears dialog secrets', async () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  const request = vi.fn().mockResolvedValue({ settings: emptySettings, backups: [] })
  const upload = vi.fn().mockResolvedValue({ backup: { id: 'uploaded', local_status: 'available' }, inspection: { manifest: { source_version: 'test' } } })
  const confirm = vi.fn().mockResolvedValue(true)
  const prompt = vi.fn()
  const props = { client: { request, upload }, dialogs: { confirm, prompt }, localizeErrorMessage: String } as unknown as BackupProps
  let model!: ReturnType<typeof useBackups>
  function Harness() { model = useBackups(props); return null }
  const root = createRoot(document.createElement('div'))
  try {
    await act(async () => root.render(<Harness />))
    await act(async () => { model.openUploadDialog() })
    await act(async () => { model.chooseUploadFile(new File(['archive'], 'test.obk')); model.setUploadPassword('synthetic-secret') })
    await act(async () => { await model.uploadBackup() })
    expect(upload).toHaveBeenCalledOnce()
    const form = upload.mock.calls[0][1] as FormData
    expect(form.get('recovery_password')).toBe('synthetic-secret')
    expect(confirm).toHaveBeenCalledOnce()
    expect(prompt).not.toHaveBeenCalled()
    expect(request).toHaveBeenCalledWith('/backups/uploaded/restore', { method: 'POST', body: JSON.stringify({ recovery_password: 'synthetic-secret' }) })
    expect(model.uploadPassword).toBe('')
    expect(model.uploadFile).toBeNull()
    expect(model.uploadDialogOpen).toBe(false)
    expect(model.working).toBe('restore-uploaded')
  } finally {
    await act(async () => root.unmount())
    vi.unstubAllGlobals()
  }
})
