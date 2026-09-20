import { expect, it, vi } from 'vitest'
import { applyAuditChange, prepareAuditChange } from './audit-changes'
it('prepares without applying and requires explicit confirmation for approved application', async () => {
 const requestV2 = vi.fn().mockResolvedValueOnce({id:'change-1'}).mockResolvedValueOnce({id:'change-1',status:'validated'}).mockResolvedValueOnce({status:'approved'}).mockResolvedValueOnce({status:'succeeded'})
 const client = {request:vi.fn(),requestV2}
 const plan = await prepareAuditChange(client,'audit.events.review',{event_id:7,expected_revision:3},'人工核实','fixed-key')
 expect(plan.status).toBe('validated');expect(requestV2).toHaveBeenCalledTimes(2)
 expect(requestV2.mock.calls[0][0]).toBe('/changesets')
 expect(JSON.parse(requestV2.mock.calls[0][1].body).idempotency_key).toBe('fixed-key')
 expect(JSON.parse(requestV2.mock.calls[0][1].body).base_revisions).toEqual({'audit_event:7':'3'})
 await applyAuditChange(client,plan.id,'确认')
 expect(requestV2.mock.calls[2][0]).toBe('/changesets/change-1/approve')
 expect(requestV2.mock.calls[3][0]).toBe('/changesets/change-1/apply')
})
it('does not claim a queued or failed change has applied', async () => {
 const client = {request:vi.fn(),requestV2:vi.fn().mockResolvedValueOnce({status:'approved'}).mockResolvedValueOnce({status:'failed'})}
 await expect(applyAuditChange(client,'old','reason')).rejects.toThrow('failed')
})
