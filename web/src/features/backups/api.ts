import type { APIClient } from '../../shared/api-client'
import type { ControllerBackup, ControllerBackupSnapshot } from './types'

type UploadResult = { backup: ControllerBackup; inspection: { manifest: { source_version: string; created_at: string } } }

export function backupAPI(client: APIClient) {
  return {
    list: async () => await client.request('/backups') as ControllerBackupSnapshot,
    upload: async (form: FormData) => await client.upload('/backups/upload', form) as UploadResult,
    download: (id: string) => client.download(`/backups/${id}/download`),
    restore: (id: string, password: string) => client.request(`/backups/${id}/restore`, {
      method: 'POST', body: JSON.stringify({ recovery_password: password }),
    }),
  }
}
