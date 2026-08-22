package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestSubscriptionRelayEndToEnd(t *testing.T) {
	for _, basePath := range []string{"", "/oboard"} {
		t.Run("basePath="+fallbackRoot(basePath), func(t *testing.T) {
			runSubscriptionRelayEndToEnd(t, basePath)
		})
	}
}

func runSubscriptionRelayEndToEnd(t *testing.T, basePath string) {
	relayBinary := os.Getenv("OBOARD_RELAY_BINARY")
	if relayBinary == "" {
		built := filepath.Join(t.TempDir(), "oboard-subscription-relay")
		command := exec.Command("go", "build", "-o", built, "github.com/OboardProject/oboard/cmd/subscription-relay")
		command.Dir = "../.."
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build relay binary: %v\n%s", err, output)
		}
		relayBinary = built
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := New(db, "test-secret-0123456789abcdef", "", basePath, nil)
	handler := srv.Handler()
	controllerServer := httptest.NewServer(handler)
	defer controllerServer.Close()

	server := &model.Server{Name: "tokyo", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 20100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "alice-id", ProxyPassword: "alice-pass", SubscriptionToken: "alice-token", LegacyProxyEnabled: true}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, EntryIPMode: model.EntryIPModeIPv4, ConfigJSON: `{"tls":{"enabled":true,"server_name":"a.example.com"},"reality":{"enabled":true,"public_key":"pubkey","short_id":"abcd"},"flow":"xtls-rprx-vision"}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{Kind: model.ProxyPathKindDirect, Name: "direct", InboundID: inbound.ID, Secret: "direct-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	grantTestPlanNode(t, db, user.ID, model.AssignableNodeProxyPath, path.ID)

	relayAddress := "127.0.0.1:12777"
	signingSecret := "0123456789abcdef0123456789abcdef"
	encrypted, err := security.EncryptSecret("test-secret-0123456789abcdef", subscriptionRelaySecretPurpose, signingSecret)
	if err != nil {
		t.Fatal(err)
	}
	publicURL := "http://" + relayAddress + basePath
	expiresAt := time.Now().UTC().Add(time.Hour)
	relay := &model.SubscriptionRelay{Name: "relay", PublicURL: publicURL, Status: "pending", EnrollmentHash: security.HashSecret("enroll-token"), EnrollmentExpiresAt: &expiresAt}
	if err := db.CreateSubscriptionRelay(ctx, relay); err != nil {
		t.Fatal(err)
	}
	relay, err = db.ClaimSubscriptionRelayEnrollment(ctx, security.HashSecret("enroll-token"), security.HashSecret("relay-token"), encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, settingSubscriptionRelayURL, relay.PublicURL); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, settingSubscriptionControllerDirectEnabled, "false"); err != nil {
		t.Fatal(err)
	}

	upstream := controllerServer.URL + basePath
	command := exec.Command(relayBinary,
		"-addr", relayAddress,
		"-upstream", upstream,
		"-relay-id", strconv.FormatInt(relay.ID, 10),
		"-secret", signingSecret,
		"-allow-http-upstream",
		"-trusted-proxy-cidrs", "127.0.0.0/8,::1/128",
	)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	}()

	check := func(name, url string, want int) string {
		t.Helper()
		client := &http.Client{Timeout: 15 * time.Second}
		var lastStatus int
		var lastBody string
		for attempt := 0; attempt < 20; attempt++ {
			response, err := client.Get(url)
			if err != nil {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
			response.Body.Close()
			lastStatus, lastBody = response.StatusCode, string(body)
			if lastStatus == want {
				return lastBody
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("%s: status = %d want %d body = %s", name, lastStatus, want, truncate(lastBody, 300))
		return ""
	}

	// Direct path on the Controller must be 404 while the relay is active.
	check("direct controller", controllerServer.URL+basePath+"/api/v1/subscriptions/alice-token?format=sing-box", http.StatusNotFound)

	// Subscription served through the real relay binary.
	body := check("relay subscription", "http://"+relayAddress+basePath+"/api/v1/subscriptions/alice-token?format=sing-box", http.StatusOK)
	var document struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(document.Outbounds) == 0 {
		t.Fatalf("relay subscription has no outbounds")
	}
}

func fallbackRoot(value string) string {
	if value == "" {
		return "/"
	}
	return value
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + fmt.Sprintf("…(%d bytes)", len(value))
}