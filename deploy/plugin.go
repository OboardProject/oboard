package deploy

import _ "embed"

//go:embed systemd/oboard-plugin-worker.service
var PluginSystemd string

//go:embed openrc/oboard-plugin-worker
var PluginOpenRC string
