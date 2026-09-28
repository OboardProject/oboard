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
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
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
	first, err := controller.issueServerStealthLayout(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetServerEnrollmentHash(ctx, server.ID, security.HashSecret("discarded-token"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	retry, err := controller.issueServerStealthLayout(ctx, server.ID)
	if err != nil || retry == first {
		t.Fatalf("retry reused the failed installation layout: %v", err)
	}
	first = retry
	active, err := controller.serverStealthLayout(ctx, server.ID)
	if err != nil || active != "" {
		t.Fatalf("layout became active before enrollment: %v", err)
	}
	if err := db.SetServerEnrollmentHash(ctx, server.ID, security.HashSecret("first-token"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimServerEnrollment(ctx, security.HashSecret("discarded-token"), "discarded-agent", security.HashSecret("discarded-secret")); err == nil {
		t.Fatal("reissued enrollment left old token valid")
	}
	if _, err := db.ClaimServerEnrollment(ctx, security.HashSecret("first-token"), "first-agent", security.HashSecret("first-agent-token")); err != nil {
		t.Fatal(err)
	}
	settings, err := db.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, exposed := controller.publicSettingsValues(ctx, settings)["server_stealth_layout."+fmt.Sprint(server.ID)]; exposed {
		t.Fatal("security-process layout leaked through public settings")
	}
	if _, exposed := controller.publicSettingsValues(ctx, settings)["server_stealth_pending."+fmt.Sprint(server.ID)]; exposed {
		t.Fatal("pending layout leaked through public settings")
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
	third, err := newTestServer(db, "test-secret", "").issueServerStealthLayout(ctx, server.ID)
	if err != nil || third == second {
		t.Fatalf("new enrollment must rotate pending layout: %v", err)
	}
	stillActive, err := newTestServer(db, "test-secret", "").serverStealthLayout(ctx, server.ID)
	if err != nil || stillActive != second {
		t.Fatalf("pending enrollment replaced active layout: %v", err)
	}
	if err := db.SetServerEnrollmentHash(ctx, server.ID, security.HashSecret("second-token"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimServerEnrollment(ctx, security.HashSecret("second-token"), "second-agent", security.HashSecret("second-agent-token")); err != nil {
		t.Fatal(err)
	}
	promoted, err := newTestServer(db, "test-secret", "").serverStealthLayout(ctx, server.ID)
	if err != nil || promoted != third {
		t.Fatalf("successful enrollment did not promote pending layout: %v", err)
	}
	if err := db.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	remaining, err := db.GetSetting(ctx, "server_stealth_layout."+fmt.Sprint(server.ID))
	if err != nil || remaining != "" {
		t.Fatalf("deleted server retained security-process layout: %v", err)
	}
	remaining, err = db.GetSetting(ctx, "server_stealth_pending."+fmt.Sprint(server.ID))
	if err != nil || remaining != "" {
		t.Fatalf("deleted server retained pending layout: %v", err)
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
