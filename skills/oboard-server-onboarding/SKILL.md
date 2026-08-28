---
name: oboard-server-onboarding
description: Safely plan and enroll a new OBoard Node Agent. Use when adding a server from a cloud API, SSH workflow, or existing inventory and when the work must preserve OBoard Changeset validation and approval boundaries.
---

# OBoard Server Onboarding

Use OBoard as the source of truth for enrollment state. External cloud and SSH tools may create or prepare the host, but they must not write OBoard state directly.

## Workflow

1. Call `oboard_task` first with intent `server.onboard`. Pass **only** the properties the user specified (`name` is required). Do not send `false` or `0` for unspecified switches.
2. Controller fills the same defaults as the panel 添加服务器 dialog. Read `oboard://forms/server-create` when you need the current map. Fast Path already applies it.
3. If a same-name server already exists, do not create another record. Follow `needs_input` or `choose_candidate` and reissue enrollment with `servers.enrollment.issue` / `confirm_reissue`. Use `servers.delete` with `confirm=true` only to remove unused duplicates.
4. If Fast Path returns `fallback_required`, call `oboard_validate_form` with `capability: "servers.onboard"` and the same sparse input. Submit `normalized_input` (keep `applied_defaults`). Do not reconstruct omitted booleans as false.
5. `ready` → explain summary/risk/approval, then `oboard_commit_task` with the `prepared_id` after confirmation. Follow the Workflow and redeem its external action only when requested.
6. Present the generated install command for the user to run in their own terminal. Issued install commands set `OBOARD_INSTALL_BBR` to `0` or `1`; they must not pass the literal `${OBOARD_INSTALL_BBR:-0}` as the value. Treat enrollment material as a one-time secret.
7. Wait for Node Agent registration, then query the server and verify connectivity, detected addresses, region, architecture, Agent build, kernel build, and capabilities.
8. Plan topology and deployment separately. Enrollment success is not approval to create listeners or deploy a proxy path.

`servers.update` is a patch. Omitted fields stay unchanged and must not be filled with create defaults.

## Stop Conditions

Stop on an expired enrollment token, changed plan hash, unavailable server capability, failed Controller validation, or any request to expose Agent tokens, proxy credentials, private keys, or signing material. Never SSH into target servers.
