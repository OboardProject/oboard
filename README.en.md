# oboard

[中文](README.md) | **English**

OBoard control-plane Controller for the panel, REST/WebSocket APIs, subscription rendering, and signed Agent/kernel distribution. Agent and kernel code live in the separate `OboardProject/oboard-agent` repository.

## Features

- **Panel and API**: Web UI, REST `/api/v1`, Agent WebSocket/callbacks, subscriptions, install scripts, and release downloads
- **Config generation**: controller-side validation, proxy-path topology, and subscription rendering without linking sing-box
- **Binary install**: default port `2787`; optional hidden path via `OBOARD_BASE_PATH`
- **Certificates**: panel-managed or manual DNS-01 (including wildcards) and Agent-side HTTP-01 through `acme.sh`
- **Signed distribution**: release script packages only signed Agent/kernel artifacts from the sibling `oboard-agent` repo

## Repository layout

| Path | Description |
|------|-------------|
| `cmd/controller` | Controller entrypoint |
| `internal/controller` | REST API, WebSocket, install/update scripts, static and download serving |
| `internal/core` | Config generation, validation, and subscription rendering |
| `web` | Web panel |
| `deploy` | Service units and deployment assets |

## Build

```bash
go test ./...
cd web && npm run build
cd .. && go build -o ../dist/controller/oboard-controller ./cmd/controller
```

> For local development, write build outputs under the parent workspace `dist/` directory so this repository tree stays clean.

The Controller release script locates the sibling `../oboard-agent` repository, invokes its release build, and packages only signed Agent/kernel artifacts for panel downloads.

## Install and manage

Default Controller port is `2787`.

### Binary install

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo bash
```

### First administrator

When `OBOARD_ADMIN_PASSWORD` is unset, the installer creates the first administrator with a random one-time password and prints it once in the controller service log on first boot. Change it immediately after login.

## License

Copyright 2026 OBoard contributors.

`oboard` (Controller and Web) is released under the
[GNU GPL v3](LICENSE).  
You may use, modify, and redistribute this software under the terms of that license.
