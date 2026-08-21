# OBoard

[中文](README.zh-CN.md) | **English**

OBoard is a Linux control panel for personal-use multi-node proxy networks. It centrally manages servers, subscriptions, certificates, and deployments, and provides a Web panel, MCP, subscription support, and signed Agent/kernel distribution.

## Features

- Centrally manage servers, users, subscriptions, proxy paths, certificates, and DNS
- Edit topology-based proxy paths with support for port forwarding, chained proxies, WARP, and rule-based routing
- Generate and deliver signed per-server configuration with online tasks, health checks, and retries
- Provide one-time token enrollment, statistics, and audit console features
- Designed for personal and small-scale distribution, not commercial use

## Installation

OBoard Controller runs as a binary.

[Install](https://oboardproject.github.io/)

The default install root is `/opt/oboard`, and the default port is `2787`.

## Related Repositories

- [oboard-agent](https://github.com/OboardProject/oboard-agent): node-side Agent and the built-in proxy kernel `oboard-sb`

## License

Copyright 2026 OBoard contributors.

`oboard` is released under the [GNU GPL v3](LICENSE).
