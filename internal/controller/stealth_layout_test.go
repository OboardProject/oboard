package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestControllerStealthLayoutSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "controller.sqlite")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "layout-persist", StealthEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	controller := newTestServer(db, "test-secret", "")
	first, err := controller.serverStealthLayout(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := db.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, exposed := controller.publicSettingsValues(ctx, settings)["server_stealth_layout."+fmt.Sprint(server.ID)]; exposed {
		t.Fatal("security-process layout leaked through public settings")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	second, err := newTestServer(db, "test-secret", "").serverStealthLayout(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("Controller changed the recorded layout after restart")
	}
	if err := db.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	remaining, err := db.GetSetting(ctx, "server_stealth_layout."+fmt.Sprint(server.ID))
	if err != nil || remaining != "" {
		t.Fatalf("deleted server retained security-process layout: %v", err)
	}
}

func TestStealthInstallerReadsControllerLayout(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	identity, err := newStealthIdentity()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	script := "set -eu\n" + extractShellFunction(t, testAgentInstallScript(t), "read_stealth_layout") + "\nread_stealth_layout\nprintf '%s\n' \"$STEALTH_INSTALL_DIR\" \"$STEALTH_AGENT_BIN\" \"$STEALTH_CONFIG_PATH\"\n"
	cmd := exec.Command(testPOSIXShell(t), "-c", script)
	cmd.Env = append(os.Environ(), "OBOARD_STEALTH_LAYOUT="+base64.StdEncoding.EncodeToString(raw))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("layout decoder failed: %v\n%s", err, output)
	}
	for _, want := range []string{"/opt/" + identity.InstallDirName, "/opt/" + identity.InstallDirName + "/" + identity.AgentName, "/etc/" + identity.ConfigDirName + "/" + identity.ConfigFileName} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("missing %s in %s", want, output)
		}
	}
}

func TestStealthInstallerBootstrapsMissingPython(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	identity, err := newStealthIdentity()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	script := testAgentInstallScript(t)
	start := strings.Index(script, "normalize_install_dir() {")
	end := strings.Index(script, "\nresolve_agent_install_dir\n")
	if start < 0 || end <= start {
		t.Fatal("installer bootstrap block is missing")
	}
	for _, manager := range []string{"apk", "pacman"} {
		t.Run(manager, func(t *testing.T) {
			root := t.TempDir()
			driver := `set -eu
ACTION=install
STEALTH_MODE=1
INSTALL_LOG=
command() {
  if [ "$1" = -v ]; then
    case "$2" in
      python3) [ -f "$PACKAGE_LOG" ]; return ;;
      apk|pacman) [ "$2" = "$PACKAGE_MANAGER" ]; return ;;
    esac
  fi
  return 1
}
apk() { printf '%s\n' "$*" > "$PACKAGE_LOG"; }
pacman() { printf '%s\n' "$*" > "$PACKAGE_LOG"; }
python3() {
  [ -f "$PACKAGE_LOG" ] || return 127
  "$REAL_PYTHON" "$@"
}
` + script[start:end] + "\nprintf '%s\\n' \"$STEALTH_AGENT_BIN\"\n"
			cmd := exec.Command(testPOSIXShell(t), "-c", driver)
			packageLog := filepath.Join(root, "packages")
			cmd.Env = append(os.Environ(), "REAL_PYTHON="+python, "PACKAGE_LOG="+packageLog,
				"PACKAGE_MANAGER="+manager, "OBOARD_STEALTH_LAYOUT="+base64.StdEncoding.EncodeToString(raw))
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("installer bootstrap failed: %v\n%s", err, output)
			}
			wantPackage := "add --no-cache python3\n"
			if manager == "pacman" {
				wantPackage = "-Sy --noconfirm python\n"
			}
			assertTestFile(t, packageLog, wantPackage)
			wantPath := "/opt/" + identity.InstallDirName + "/" + identity.AgentName
			if !strings.Contains(string(output), wantPath) {
				t.Fatalf("layout was not decoded after installing Python: %s", output)
			}
		})
	}
}
