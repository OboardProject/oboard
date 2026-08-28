package core

import "embed"

//go:embed subscription_templates/*.tmpl
var subscriptionBuiltinTemplates embed.FS
