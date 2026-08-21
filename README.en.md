# OBoard

OBoard is a Linux control plane for multi-node proxy networks. It manages servers, subscriptions, certificates, and deployments, and provides a Web panel, REST/WebSocket APIs, subscription rendering, and signed Agent/kernel distribution.

## Features

- Centrally manage servers, users, subscriptions, proxy paths, certificates, and DNS
- Generate and deliver signed per-server configuration with online tasks, health checks, and retries
- Render subscriptions and serve verified Agent/kernel packages, including `OBOARD_BASE_PATH`
- Enforce one-time enrollment tokens, signed tasks, encrypted storage, and audit controls
- Provide an official Linux binary installer with install, update, uninstall, and `VERSION`-based stable or development release selection

## Installation

OBoard Controller is distributed as Linux binary packages for `amd64` and `arm64`. The official installer covers install, update, and uninstall, and selects a stable or development release with `VERSION=latest` or `VERSION=dev`.

The default install root is `/opt/oboard` and the default port is `2787`.

## Related Repositories

- [oboard-agent](https://github.com/OboardProject/oboard-agent): node-side Agent and the built-in proxy kernel `oboard-sb`

## License

Copyright 2026 OBoard contributors.

`oboard` (Controller and Web) is released under the [GNU GPL v3](LICENSE). You may use, modify, and redistribute this software under the terms of that license.
