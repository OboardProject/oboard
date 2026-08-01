---
name: oboard-proxy-chain-designer
description: Design and compare OBoard proxy-path candidates from live topology constraints. Use when selecting entry, relay, exit, transport, region, or deployment scope without allowing an AI to generate or write raw sing-box configuration.
---

# OBoard Proxy Chain Designer

Work only with OBoard's `proxy_paths` and ordered `proxy_path_steps` model. Never generate raw kernel JSON, choose ports independently, or connect directly to a Node Agent.

## Workflow

1. Call `oboard_list_capabilities`, then read `topology.read` through `oboard_query`.
2. Call `oboard_plan_proxy_path` with the entry server, exit region, preferred relay regions, hop limit, UDP requirement, avoided servers, and objective.
3. Compare only candidates returned by Controller. Explain online state, region match, hop count, transport capability, and warnings. Do not invent an unavailable path.
4. Re-read topology before creating a Changeset. Use current resource revisions as `base_revisions`.
5. Create a `topology.write` Changeset only when the capability is listed and the user has selected an exact candidate. Then call `oboard_validate_changeset`.
6. Show the Controller-computed diff, listener conflicts, affected servers and users, reload requirement, risk class, and plan hash. Stop for approval.
7. Apply only an approved Changeset. Follow with `oboard_plan_deployment`; topology approval does not imply deployment approval.

## Safety Rules

Never bypass DAG, port, certificate, tunnel, trusted-forward, MTU, or Agent/kernel capability validation. Stop if state revisions changed, validation reports a loop or conflict, a trusted transparent prefix would be partially deployed, or the requested operation is not executable in the current Controller build.
