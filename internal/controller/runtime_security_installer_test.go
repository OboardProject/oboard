package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeSecurityInstallerUsesAgentPreflight(t *testing.T) {
	for _, script := range []string{testAgentInstallScript(t), testAgentSelfUpdateScript(t)} {
		dir := t.TempDir()
		config := filepath.Join(dir, "config")
		if err := os.WriteFile(config, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		writeExecutable(t, filepath.Join(dir, "agent"), `#!/bin/sh
[ "$1" = -preflight-core-update ] || exit 99
exit 17
`)
		driver := "set -eu\n" + extractShellFunction(t, script, "preflight_staged_core") + "\npreflight_staged_core /staged/core\necho continued\n"
		cmd := exec.Command(testPOSIXShell(t), "-c", driver)
		cmd.Env = append(os.Environ(), "CONFIG_PATH="+config, "STATE_DIR="+dir, "tmp="+dir, "agent_name=agent")
		output, err := cmd.CombinedOutput()
		if err == nil || strings.Contains(string(output), "continued") || !strings.Contains(string(output), "预检失败") {
			t.Fatalf("failed preflight did not stop installer: %v %s", err, output)
		}
	}
}
