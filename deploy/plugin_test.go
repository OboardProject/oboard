package deploy

import (
	"strings"
	"testing"
)

// The worker only talks to the Controller over a Unix socket; all plugin
// HTTP goes through the Controller gateway, so the unit denies IP entirely.
func TestPluginWorkerUnitHasNoNetwork(t *testing.T) {
	for _, directive := range []string{"IPAddressDeny=any", "RestrictAddressFamilies=AF_UNIX AF_NETLINK", "NoNewPrivileges=true", "User=oboard-plugins", "ProtectSystem=strict"} {
		if !strings.Contains(PluginSystemd, "\n"+directive+"\n") {
			t.Fatalf("plugin worker unit is missing %q", directive)
		}
	}
	if strings.Contains(PluginOpenRC, "command_user=\"root") {
		t.Fatal("plugin worker must not run as root")
	}
}
