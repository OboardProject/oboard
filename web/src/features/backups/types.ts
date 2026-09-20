import type { APIClient } from '../../shared/api-client'
import type { DialogApi } from '../../components/ui/dialog-context'
export type BackupDestination = { provider: 's3' | 'webdav' | ''; endpoint: string; bucket?: string; prefix?: string; region?: string; force_path_style?: boolean; enabled: boolean }
export type ControllerBackup = { id: string; name: string; origin: 'manual' | 'automatic' | 'uploaded' | 'pre_restore' | string; local_status: string; remote_status: string; remote_error?: string; remote_retrievable: boolean; size_bytes: number; source_version: string; format_version: number; protected: boolean; created_at: string }
export type ControllerUpdateBackup = { name: string; path: string; size_bytes: number; mod_time: string; created_at: string; is_latest: boolean; target_build?: string }
export type ControllerBackupSettings = { enabled: boolean; schedule: 'daily' | 'weekly'; time: string; weekday: number; local_retention: number; remote_retention: number; update_retention: number; destination: BackupDestination; password_configured: boolean; destination_configured: boolean; last_success_at?: string; last_error?: string }
export type ControllerBackupSnapshot = { settings: ControllerBackupSettings; backups: ControllerBackup[]; update_backups?: ControllerUpdateBackup[]; update_retention?: number }
export type BackupProps = { client: APIClient; dialogs: DialogApi; notify?: (message: string, tone: 'error' | 'success' | 'info') => void; localizeErrorMessage: (message: unknown) => string }
