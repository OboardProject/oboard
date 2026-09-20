import type { APIClient } from '../../shared/api-client'

export type Task = { id: number; server_id?: number; type: string; status: string; config_version?: number; created_at?: string; updated_at?: string; completed_at?: string; payload_json?: string; result_json?: string; nonce?: string }
export type TaskServer = { id: number; name: string }
export type TasksProps = { tasks?: Task[]; servers?: TaskServer[]; client: APIClient; loading?: boolean }
