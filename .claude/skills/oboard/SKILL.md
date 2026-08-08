---
name: oboard
description: Operate OBoard through its MCP Fast Path for server onboarding and settings, proxy paths, deployments, and other Controller tasks. Use whenever a user asks to inspect, plan, change, deploy, or recover OBoard-managed resources through MCP.
---

# OBoard MCP

Use the Fast Path as the default OBoard interaction model. The MCP tool descriptions are sufficient even when this skill is unavailable; this skill only reinforces the efficient sequence.

## Normal Workflow

1. Call `oboard_task` first with the user's goal. Supply an explicit `intent`, structured `params`, or `target_refs` when already known, but do not discover capabilities first.
2. Follow `status` and `next_action` literally:
   - `ready`: explain the returned summary, risk, approval, and verification. Never reconstruct or expose the internal operations.
   - `needs_input`: obtain only the requested fields and call `oboard_task` again with the same `continuation_id`.
   - `choose_candidate`: choose only from returned refs and resume with the same `continuation_id`.
   - `fallback_required`: use `oboard_discover`, capability schema, plan, validate, and submit as the advanced path.
   - `error`: follow the returned recovery action without widening targets.
3. For `ready`, call `oboard_commit_task` with the returned `prepared_id` and a stable idempotency key only after the user has authorized the change.
4. Follow the returned Workflow with `oboard_get_workflow` until terminal. Changeset creation is not execution success.
5. Redeem an external action only when the Workflow requests it. Present target-server commands to the user for execution in their own terminal.

## Invariants

- Never manually build or carry capability plan JSON unless `oboard_task` returned `fallback_required`.
- Never modify a prepared task or infer its hidden operations from the summary.
- Never claim completion before the canonical Workflow reaches `succeeded`; report partial, failed, cancelled, expired, and approval states exactly.
- Never broaden target scope after ambiguity, denial, or a resource error.
- Never expose or retain tokens, passwords, private keys, enrollment material, or other secrets.
- Never SSH into a target server or run a remote shell on the user's behalf. OBoard does not provide that capability.
- Treat specialized OBoard skills as optional advanced references, not prerequisites for normal Fast Path use.
