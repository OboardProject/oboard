package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func relayLifecycleFunctions(t *testing.T) string {
	t.Helper()
	raw, err := subscriptionRelayAssets.ReadFile("assets/install-subscription-relay.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	start := strings.Index(script, "relay_ready() {")
	if start < 0 {
		t.Fatal("missing relay lifecycle functions")
	}
	end := strings.Index(script[start:], "\nif [ \"$manager\" = systemd ]; then")
	if start < 0 || end < 0 {
		t.Fatal("missing relay lifecycle functions")
	}
	return script[start : start+end]
}

func TestRelayInstallerReadinessUsesLocalBasePath(t *testing.T) {
	for _, item := range []struct{ addr, url string }{
		{":2777", "http://127.0.0.1:2777/panel/healthz"},
		{"0.0.0.0:2777", "http://127.0.0.1:2777/panel/healthz"},
		{"[::]:2777", "http://[::1]:2777/panel/healthz"},
		{"127.0.0.1:3000", "http://127.0.0.1:3000/panel/healthz"},
	} {
		t.Run(item.addr, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "curl"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$PROBE_LOG\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", "-c", relayLifecycleFunctions(t)+"\nrelay_ready")
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "RELAY_ADDR="+item.addr, "CONTROLLER_URL=https://controller.example/panel/", "PROBE_LOG="+filepath.Join(dir, "probe"))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("probe: %s, %v", output, err)
			}
			args, err := os.ReadFile(filepath.Join(dir, "probe"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(args), "--noproxy *") || !strings.Contains(string(args), item.url) {
				t.Fatalf("wrong health request: %s", args)
			}
		})
	}
}

func TestRelayInstallerRestoresOldBinaryWhenNewServiceIsNotReady(t *testing.T) {
	for _, manager := range []string{"systemd", "openrc"} {
		t.Run(manager, func(t *testing.T) {
			dir := t.TempDir()
			scripts := map[string]string{
				"curl":       "#!/bin/sh\n[ \"$(cat \"$INSTALL_DIR/oboard-subscription-relay\")\" = old ]\n",
				"sleep":      "#!/bin/sh\nexit 0\n",
				"systemctl":  "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SERVICE_LOG\"\n",
				"rc-service": "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SERVICE_LOG\"\n",
			}
			for name, script := range scripts {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for name, content := range map[string]string{"oboard-subscription-relay": "new", "oboard-subscription-relay.rollback": "old"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("/bin/sh", "-c", relayLifecycleFunctions(t)+"\nfail() { echo \"$*\"; exit 1; }\nif ! restart_relay || ! relay_ready; then rollback_relay; fi")
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "RELAY_ADDR=:2777", "CONTROLLER_URL=https://controller.example/panel", "manager="+manager, "HAD_OLD=1", "INSTALL_DIR="+dir, "ROLLBACK_FILE="+filepath.Join(dir, "oboard-subscription-relay.rollback"), "SERVICE_LOG="+filepath.Join(dir, "services"))
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "已恢复旧版本服务") {
				t.Fatalf("update result: %s, %v", output, err)
			}
			restored, _ := os.ReadFile(filepath.Join(dir, "oboard-subscription-relay"))
			if string(restored) != "old" {
				t.Fatalf("old binary not restored: %s", restored)
			}
			log, _ := os.ReadFile(filepath.Join(dir, "services"))
			if len(strings.Split(strings.TrimSpace(string(log)), "\n")) != 2 {
				t.Fatalf("expected new and rollback restarts: %s", log)
			}
		})
	}
}
