# OBoard

[中文](README.md) | **English**

OBoard is a Linux control plane for multi-node proxy networks. It manages servers, configuration, subscriptions, certificates, and deployments, with a Web panel, REST/WebSocket APIs, subscription rendering, and signed Agent/kernel distribution. The node-side Agent and the built-in proxy kernel `oboard-sb` live in the separate `OboardProject/oboard-agent` repository.

## Overview

- **Central management**: manage servers, users, subscriptions, proxy paths, certificates, and DNS from the panel and API
- **Deployment scheduling**: generate and deliver signed per-server configuration with online tasks, health checks, retries, and visible task states
- **Subscriptions and distribution**: render subscriptions, serve verified Agent/kernel packages, and support `OBOARD_BASE_PATH`
- **Security boundaries**: one-time enrollment tokens, signed tasks, encrypted storage, audit controls, and restricted operations
- **Simple installation**: one script covers install, update, uninstall, and the install command can select stable or development releases

## Installation

OBoard Controller supports Linux only. Official packages are available for `amd64` and `arm64`. The default install root is `/opt/oboard`, the default port is `2787`, and the installer automatically detects systemd or OpenRC.

### Release Selection

Install and update commands use `VERSION` to select the target release:

- Stable: `VERSION=latest`
- Development: `VERSION=dev`

### Install

Install the stable release (the installer falls back to the development release when no stable release exists yet):

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env VERSION=latest bash
```

Install the development release (the development release is a mutable build that follows `main` and is intended for testing):

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env VERSION=dev bash
```

To use a custom install root, pass `INSTALL_DIR`:

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env INSTALL_DIR=/data/oboard bash
```

### Update

Run the install script again. When an existing Controller is detected, it updates in place and preserves the existing configuration, accounts, and data. The update target is selected with `VERSION` as well.

Update to the stable release:

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env VERSION=latest bash
```

Update to the development release:

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env VERSION=dev bash
```

### Uninstall

Keep the configuration, database, and certificates (with the default install root they are reused on the next install; a custom root requires passing the same `INSTALL_DIR` again):

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env OBOARD_ACTION=uninstall bash
```

Remove the entire install root:

```bash
curl -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/scripts/install.sh | sudo env OBOARD_ACTION=uninstall OBOARD_PURGE_DATA=1 bash
```

### First Administrator

On first install, the installer prompts for the super administrator account. If no password is provided, it generates a random one-time password and prints it once in the install result. Save it before closing the terminal and change it immediately after logging in.

## License

Copyright 2026 OBoard contributors.

`oboard` (Controller and Web) is released under the [GNU GPL v3](LICENSE). You may use, modify, and redistribute this software under the terms of that license.
