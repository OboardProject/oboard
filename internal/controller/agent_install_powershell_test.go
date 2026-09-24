package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestWindowsAgentInstallerResolvesPlaceholders(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	New(db, "test-secret", "", "", nil).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/install/agent.ps1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("Windows installer status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	script := response.Body.String()
	if strings.Contains(script, "__BASE_URL__") || strings.Contains(script, "__RELEASE_PUBLIC_KEY__") {
		t.Fatal("installer placeholders were not replaced")
	}
	for _, want := range []string{
		"$DefaultBaseUrl = 'https://panel.example.com'",
		"[OBoard.ReleaseVerifier]::Verify(",
		"Test-DownloadedRelease $tmp @($agentName, $coreName, $realmName)",
		"'-enroll-only'",
		"Remove-Item Env:\\OBOARD_ENROLL_TOKEN",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("installer missing %q", want)
		}
	}
	// Enrollment runs only after the signed components are installed, and the
	// installer never tells the operator it succeeded before the Agent service
	// has stayed up.
	verify := strings.Index(script, "[void](Test-DownloadedRelease")
	enroll := strings.Index(script, "'-enroll-only'")
	stable := strings.Index(script, "Wait-ServiceStable $AgentService 15")
	success := strings.Index(script, "安装完成：Agent 已注册并在后台运行。")
	if verify < 0 || enroll < verify || stable < enroll || success < stable {
		t.Fatal("installer must verify, enroll, and confirm the running service in order")
	}
}

func TestPowerShellSingleQuoteDoublesQuotes(t *testing.T) {
	if got := powershellSingleQuote("it's"); got != "'it''s'" {
		t.Fatalf("powershellSingleQuote = %s", got)
	}
}

func TestEnrollmentReturnsWindowsCommandOnlyForStandardLayout(t *testing.T) {
	ctx := context.Background()
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	if err := db.SetSetting(ctx, "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	plain := &model.Server{Name: "enrollment-windows"}
	if err := db.CreateServer(ctx, plain); err != nil {
		t.Fatal(err)
	}
	result := request(t, h, http.MethodPost, fmt.Sprintf("/api/v1/ui/servers/%d/enroll-token", plain.ID), token, map[string]any{}, http.StatusOK)
	command, _ := result["windows_install_command"].(string)
	enrollment := result["enrollment_token"].(string)
	for _, want := range []string{
		"$env:OBOARD_ENROLL_TOKEN = " + powershellSingleQuote(enrollment),
		"'https://panel.example.com/install/agent.ps1'",
		"[Text.Encoding]::UTF8",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("Windows command missing %q: %s", want, command)
		}
	}

	stealthServer := &model.Server{Name: "enrollment-windows-stealth", StealthEnabled: true}
	if err := db.CreateServer(ctx, stealthServer); err != nil {
		t.Fatal(err)
	}
	srv.stealthTransport.Store(&stealthTransport{addr: "transport.example.com:443", pin: "test-pin"})
	stealthResult := request(t, h, http.MethodPost, fmt.Sprintf("/api/v1/ui/servers/%d/enroll-token", stealthServer.ID), token, map[string]any{}, http.StatusOK)
	if _, ok := stealthResult["windows_install_command"]; ok {
		t.Fatal("security-process servers must not receive a Windows command")
	}
}
