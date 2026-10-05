package controller

import (
	"os"
	"os/exec"
	"testing"
)

func TestStealthReinstallReplacementAndRollback(t *testing.T) {
	script := testAgentInstallScript(t)
	functions := extractShellFunction(t, script, "restore_reused_install") + "\n" + extractShellFunction(t, script, "install_reused_stealth")
	for _, scenario := range []string{"upgrade", "wrong-build", "stage-failure", "partial-replacement"} {
		t.Run(scenario, func(t *testing.T) {
			driver := `set -eu
cd "$TEST_ROOT"
mkdir download live
tmp="$TEST_ROOT/download"
INSTALL_DIR="$TEST_ROOT/live"
agent_name=agent
core_name=core
realm_name=realm
STEALTH_AGENT_BIN="$INSTALL_DIR/agent"
STEALTH_CORE_BIN="$INSTALL_DIR/core"
STEALTH_REALM_BIN="$INSTALL_DIR/realm"
STEALTH_AGENT_SERVICE=agent-service
STEALTH_CORE_SERVICE=core-service
STEALTH_CONFIG_PATH=config
STEALTH_KEY_PATH=key
STEALTH_IDENTITY_JSON="{}"
SERVICE_MANAGER=systemd
INSTALL_LOG="$TEST_ROOT/install.log"
TARGET_BUILD=new-build
OBOARD_ENROLL_TOKEN=test-token
OLD_ACTIVE_SERVICES=
cat > "$tmp/agent" <<'AGENT'
#!/bin/sh
case "$*" in
 *-reuse-existing*|*-cleanup-existing*) exit 0 ;;
 -version) echo "Agent build new-build" ;;
 *-enroll-only*) echo enrolled >> "$TEST_ROOT/events" ;;
 *) exit 1 ;;
esac
AGENT
chmod 755 "$tmp/agent"
echo new-core > "$tmp/core"
echo new-realm > "$tmp/realm"
printf '#!/bin/sh\necho "Agent build old-build"\n' > "$STEALTH_AGENT_BIN"
chmod 755 "$STEALTH_AGENT_BIN"
echo old-core > "$STEALTH_CORE_BIN"
echo old-realm > "$STEALTH_REALM_BIN"
cp "$STEALTH_AGENT_BIN" original-agent
service_active() { return 1; }
stop_previous_services() { OLD_ACTIVE_SERVICES=agent-service; echo stopped >> events; }
restart_managed_service() { echo "restart $1" >> events; }
wait_service_stable() { return 0; }
release_core_lifecycle_lock() { :; }
install() {
 if [ "$SCENARIO" = stage-failure ] && [ "$3" = "$tmp/core" ]; then return 1; fi
 command install "$@"
}
mv() {
 if [ "$SCENARIO" = partial-replacement ] && [ "$2" = "$STEALTH_CORE_BIN.next.$$" ]; then return 1; fi
 command mv "$@"
}
` + functions + `
if [ "$SCENARIO" = wrong-build ]; then TARGET_BUILD=other-build; fi
if install_reused_stealth; then
 [ "$SCENARIO" = upgrade ]
 cmp "$tmp/agent" "$STEALTH_AGENT_BIN"
 cmp "$tmp/core" "$STEALTH_CORE_BIN"
 cmp "$tmp/realm" "$STEALTH_REALM_BIN"
 grep -q '^enrolled$' events
else
 [ "$SCENARIO" != upgrade ]
 cmp original-agent "$STEALTH_AGENT_BIN"
 [ "$(cat "$STEALTH_CORE_BIN")" = old-core ]
 [ "$(cat "$STEALTH_REALM_BIN")" = old-realm ]
 if grep -q '^enrolled$' events; then exit 1; fi
fi
grep -q '^restart agent-service$' events
for asset in "$STEALTH_AGENT_BIN" "$STEALTH_CORE_BIN" "$STEALTH_REALM_BIN"; do
 [ ! -e "$asset.previous.$$" ]
 [ ! -e "$asset.next.$$" ]
done
`
			cmd := exec.Command(testPOSIXShell(t), "-c", driver)
			cmd.Env = append(os.Environ(), "TEST_ROOT="+t.TempDir(), "SCENARIO="+scenario)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("reinstall %s: %v\n%s", scenario, err, output)
			}
		})
	}
}
