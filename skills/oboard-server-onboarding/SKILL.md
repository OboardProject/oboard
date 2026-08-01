---
name: oboard-server-onboarding
description: Safely plan and enroll a new OBoard Node Agent. Use when adding a server from a cloud API, SSH workflow, or existing inventory and when the work must preserve OBoard Changeset validation and approval boundaries.
---

# OBoard Server Onboarding

Use OBoard as the source of truth for enrollment state. External cloud and SSH tools may create or prepare the host, but they must not write OBoard state directly.

## Workflow

1. Call `oboard_list_capabilities`. Stop if inventory read, server planning, or onboarding capabilities are absent.
2. Call `oboard_query` with `inventory.read`, then `oboard_plan_server_onboarding` with the intended name, region, and IP stack.
3. Present name conflicts, defaults, and the exact external installation action. Obtain user approval before using cloud APIs, SSH, or cloud-init.
4. Call `oboard_create_changeset` with `servers.onboard`, a unique idempotency key, and the planner's server input.
5. Call `oboard_validate_changeset`. Show the plan hash, warnings, risk class, and blast radius. Stop while status is `awaiting_approval`.
6. Call `oboard_apply_changeset` only after Controller approval. Treat an enrollment token as a one-time secret: do not log it, retain it in memory, or place it in chat history beyond the immediate install action.
7. Wait for Node Agent registration, then query the server and verify connectivity, detected addresses, region, architecture, Agent build, kernel build, and capabilities.
8. Plan topology and deployment separately. Enrollment success is not approval to create listeners or deploy a proxy path.

## Stop Conditions

Stop on an expired enrollment token, changed plan hash, unavailable server capability, failed Controller validation, or any request to expose Agent tokens, proxy credentials, private keys, or signing material.
