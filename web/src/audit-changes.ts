export type AuditClient = {
  request: (path: string, init?: RequestInit) => Promise<any>
  requestV2?: (path: string, init?: RequestInit) => Promise<any>
}
export async function prepareAuditChange(client: AuditClient, capability: string, input: unknown, reason: string, key: string) {
  if (!client.requestV2) throw new Error('当前客户端不支持受控变更')
  const values = input as { revision?: number; event_id?: number; expected_revision?: number }
  const base_revisions: Record<string, string> = {}
  if (capability === 'audit.collection.update') base_revisions.audit_collection = String(values.revision)
  if (capability === 'audit.policy.update') base_revisions.account_audit_policy = String(values.revision)
  if (capability === 'audit.events.review' || capability === 'audit.events.analyze') base_revisions[`audit_event:${values.event_id}`] = String(values.expected_revision)
  const draft = await client.requestV2('/changesets', { method: 'POST', body: JSON.stringify({ reason, idempotency_key: key, base_revisions, operations: [{ capability, input }] }) })
  return client.requestV2(`/changesets/${encodeURIComponent(draft.id)}/validate`, { method: 'POST', body: '{}' })
}
export async function applyAuditChange(client: AuditClient, id: string, reason: string) {
  if (!client.requestV2) throw new Error('当前客户端不支持受控变更')
  await client.requestV2(`/changesets/${encodeURIComponent(id)}/approve`, { method: 'POST', body: JSON.stringify({ comment: reason }) })
  const result = await client.requestV2(`/changesets/${encodeURIComponent(id)}/apply`, { method: 'POST', body: '{}' })
  if (result.status !== 'succeeded') throw new Error(`变更尚未完成：${result.status || '未知状态'}`)
  return result
}
