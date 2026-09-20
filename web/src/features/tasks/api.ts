import type { APIClient } from '../../shared/api-client'
import type { Task } from './types'

export type TaskOperationAttempt = { target_type: string; target_id: string; task_id: number | null; attempt: number; config_version?: number; actual_revision?: number; execution_kind: string; state: string }
export type TaskOperationRecord = { id: string; kind?: string; created_at: string; targets: { target_type: string; target_id: string; state: string; cause_code?: string }[]; attempts?: TaskOperationAttempt[] }

export async function fetchTaskOperation(client: APIClient, id: string): Promise<TaskOperationRecord> {
  return await client.request(`/task-operations/${encodeURIComponent(id)}`) as TaskOperationRecord
}

export async function fetchTaskOperations(client: APIClient, before?: TaskOperationRecord): Promise<TaskOperationRecord[]> {
  const query = new URLSearchParams({ limit: '25' })
  if (before) { query.set('before_time', before.created_at); query.set('before_id', before.id) }
  const result = await client.request(`/task-operations?${query}`) as { operations?: TaskOperationRecord[] }
  return result.operations || []
}

export async function fetchTasks(client: APIClient): Promise<Task[]> {
  const result = await client.request('/agent-tasks?limit=300') as { tasks?: Task[] }
  return result.tasks || []
}

export async function fetchTask(client: APIClient, id: number): Promise<Task | undefined> {
  const result = await client.request(`/agent-tasks/${id}`) as { task?: Task }
  return result.task
}
