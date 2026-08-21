package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

func TestSettingsCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	updateInput, _ := json.Marshal(map[string]any{"changes": map[string]any{"audit_enabled": false, "traffic_timezone": "Asia/Tokyo", "traffic_enforcement_mode": "reject_new", "subscription_relay_url": "https://subscriptions.example.com", "subscription_controller_direct_enabled": true}})
	applyAutomationChangeset(t, server, principal, "settings-update", automation.OperationRequest{Capability: "settings.update", Input: updateInput})
	settings, err := db.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings["audit_enabled"] != "false" || settings["traffic_timezone"] != "Asia/Tokyo" || settings["traffic_enforcement_mode"] != "reject_new" || settings["subscription_relay_url"] != "https://subscriptions.example.com" || settings[settingSubscriptionControllerDirectEnabled] != "true" {
		t.Fatalf("settings not applied: %#v", settings)
	}
	invalidInput, _ := json.Marshal(map[string]any{"changes": map[string]any{"subscription_relay_url": "http://subscriptions.example.com"}})
	if _, err := server.settingsUpdateCandidate(ctx, invalidInput, false); err == nil {
		t.Fatal("settings.update validation accepted an insecure relay URL")
	}
	payload, err := server.readSystemResource(ctx, principal, "settings")
	if err != nil {
		t.Fatalf("settings.get: %v", err)
	}
	encoded, _ := json.Marshal(payload)
	if len(encoded) == 0 || string(encoded) == "{}" {
		t.Fatalf("empty settings payload")
	}
}

func TestBackupCapabilityRunsInBackground(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	root := t.TempDir()
	t.Setenv("OBOARD_BACKUP_DIR", filepath.Join(root, "backups"))
	t.Setenv("OBOARD_ACME_HOME", filepath.Join(root, "acme"))
	server := newTestServer(db, "test-secret", "")
	server.ConfigureControllerBackups(filepath.Join(root, "oboard.sqlite"))
	ctx := context.Background()
	admin := &model.User{Username: "backup-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111113", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	plain, err := json.Marshal(controllerBackupSecrets{RecoveryPassword: "backup-recovery-password"})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := security.EncryptSecret(server.sessionSecret, controllerBackupSecretsSetting, string(plain))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSettings(ctx, map[string]string{controllerBackupSecretsSetting: wrapped}); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	applied := applyAutomationChangesetResult(t, server, principal, "backup-create", automation.OperationRequest{Capability: "backups.create", Input: json.RawMessage(`{"upload_remote":false}`)})
	var result struct {
		Operations []struct {
			Backup struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"backup"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(applied.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Operations) != 1 || result.Operations[0].Backup.ID == "" || result.Operations[0].Backup.Status != "pending" {
		t.Fatalf("async backup result = %#v", result)
	}
	backupID := result.Operations[0].Backup.ID
	deadline := time.Now().Add(15 * time.Second)
	for {
		items, err := db.ListControllerBackups(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if item.ID == backupID && item.LocalStatus == "available" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("async backup did not complete: %#v", items)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestSubscriptionRelayCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "relay-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111112", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	applyAutomationChangeset(t, server, principal, "relay-create", automation.OperationRequest{Capability: "subscription_relays.create", Input: json.RawMessage(`{"name":"国内入口","public_url":"https://relay.example.com"}`)})
	relays, err := db.ListSubscriptionRelays(ctx)
	if err != nil || len(relays) != 1 {
		t.Fatalf("relays=%#v err=%v", relays, err)
	}
	relayID := relays[0].ID
	encrypted, err := security.EncryptSecret("test-secret", subscriptionRelaySecretPurpose, "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimSubscriptionRelayEnrollment(ctx, relays[0].EnrollmentHash, security.HashSecret("relay-token"), encrypted); err != nil {
		t.Fatal(err)
	}
	listed, err := server.application.Query(ctx, principal, "subscription_relays.list", json.RawMessage(`{}`))
	if err != nil || len(listed.([]map[string]any)) != 1 {
		t.Fatalf("relay query=%#v err=%v", listed, err)
	}
	updateInput, _ := json.Marshal(map[string]any{"relay_id": relayID, "name": "华东入口", "public_url": "https://sub.example.com"})
	applyAutomationChangeset(t, server, principal, "relay-update", automation.OperationRequest{Capability: "subscription_relays.update", Input: updateInput})
	updated, err := db.GetSubscriptionRelay(ctx, relayID)
	if err != nil || updated.Name != "华东入口" || updated.PublicURL != "https://sub.example.com" {
		t.Fatalf("updated relay=%#v err=%v", updated, err)
	}
	issueInput, _ := json.Marshal(map[string]any{"relay_id": relayID})
	applyAutomationChangeset(t, server, principal, "relay-issue", automation.OperationRequest{Capability: "subscription_relays.issue_enrollment", Input: issueInput})
	activateInput, _ := json.Marshal(map[string]any{"relay_id": relayID})
	applyAutomationChangeset(t, server, principal, "relay-activate", automation.OperationRequest{Capability: "subscription_relays.activate", Input: activateInput})
	settings, _ := db.ListSettings(ctx)
	if settings[settingSubscriptionRelayURL] != "https://sub.example.com" || settings[settingSubscriptionControllerDirectEnabled] != "false" {
		t.Fatalf("active relay settings=%#v", settings)
	}
	deleteInput, _ := json.Marshal(map[string]any{"relay_id": relayID, "confirm": true})
	applyAutomationChangeset(t, server, principal, "relay-delete", automation.OperationRequest{Capability: "subscription_relays.delete", Input: deleteInput})
	if _, err := db.GetSubscriptionRelay(ctx, relayID); err == nil {
		t.Fatal("deleted relay still exists")
	}
	settings, _ = db.ListSettings(ctx)
	if settings[settingSubscriptionRelayURL] != "" || settings[settingSubscriptionControllerDirectEnabled] != "true" {
		t.Fatalf("deleted relay access settings=%#v", settings)
	}
}

func TestSubscriptionRelayEnrollmentProducesRedeemableExternalAction(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	if err := db.SetSetting(ctx, "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	admin := &model.User{Username: "relay-ext-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	principal.GrantID = "grt_relay_external"

	applied := applyAutomationChangesetResult(t, server, principal, "relay-external-create", automation.OperationRequest{Capability: "subscription_relays.create", Input: json.RawMessage(`{"name":"阿里云","public_url":"https://relay.example.com"}`)})
	workflow := &model.AutomationWorkflow{ID: "wf_relay_external_create", PrincipalID: principal.ID, GrantID: principal.GrantID, Status: model.WorkflowExternalActionRequired, ChangesetID: applied.ID}
	actionID, err := server.storeOneTimeExternalAction(ctx, principal, workflow, applied)
	if err != nil || actionID == "" {
		t.Fatalf("relay create external action id=%q err=%v", actionID, err)
	}
	redeemed, err := db.ConsumeExternalAction(ctx, actionID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := security.DecryptSecret(server.sessionSecret, "external-action", redeemed.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(plaintext), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["type"] != "execute_on_target" {
		t.Fatalf("relay action type=%#v", payload["type"])
	}
	environment, _ := payload["environment"].(map[string]any)
	if token, _ := environment["OBOARD_SUBSCRIPTION_RELAY_ENROLLMENT_TOKEN"].(string); token == "" {
		t.Fatalf("relay action missing enrollment token: %s", plaintext)
	}
	if command, _ := payload["command"].(string); !strings.Contains(command, "/install/subscription-relay.sh") {
		t.Fatalf("relay action missing install command: %s", plaintext)
	}

	relays, err := db.ListSubscriptionRelays(ctx)
	if err != nil || len(relays) != 1 {
		t.Fatalf("relays=%#v err=%v", relays, err)
	}
	issueInput, _ := json.Marshal(map[string]any{"relay_id": relays[0].ID})
	issued := applyAutomationChangesetResult(t, server, principal, "relay-external-issue", automation.OperationRequest{Capability: "subscription_relays.issue_enrollment", Input: issueInput})
	workflow.ID = "wf_relay_external_issue"
	actionID, err = server.storeOneTimeExternalAction(ctx, principal, workflow, issued)
	if err != nil || actionID == "" {
		t.Fatalf("relay issue external action id=%q err=%v", actionID, err)
	}
	if _, err := db.GetExternalAction(ctx, actionID); err != nil {
		t.Fatalf("relay issue action not stored: %v", err)
	}
}

func TestApprovalPolicyAndNotificationCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	serviceAccount, err := server.newServicePrincipal(*admin, "MCP 自动化", []string{"servers:*"}, json.RawMessage(`{}`), nil, 60, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAPIPrincipal(ctx, serviceAccount); err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	policyInput, _ := json.Marshal(map[string]any{"principal_id": serviceAccount.ID, "capability": "servers.update", "mode": "automatic", "expires_at": expiresAt})
	applyAutomationChangeset(t, server, principal, "policy-set", automation.OperationRequest{Capability: "approval_policies.set", Input: policyInput})
	policies, err := db.ListApprovalPolicies(ctx, serviceAccount.ID)
	if err != nil || len(policies) != 1 {
		t.Fatalf("policies=%#v err=%v", policies, err)
	}
	if policies[0].Mode != model.ApprovalAutomatic || policies[0].Capability != "servers.update" {
		t.Fatalf("unexpected policy: %#v", policies[0])
	}
	if policies[0].ExpiresAt == nil || !policies[0].ExpiresAt.Equal(expiresAt) {
		t.Fatalf("approval policy expiry = %v, want %v", policies[0].ExpiresAt, expiresAt)
	}
	channelInput := json.RawMessage(`{"notification_channel":{"name":"测试频道","type":"bark","enabled":true,"events":"server_offline","config_json":"{\"device_key\":\"test-device-key\",\"group\":\"测试\"}","user_ids":[]}}`)
	applyAutomationChangeset(t, server, principal, "channel-create", automation.OperationRequest{Capability: "notification_channels.create", Input: channelInput})
	channels, err := db.ListNotificationChannelsByOwner(ctx, admin.ID)
	if err != nil || len(channels) != 1 {
		t.Fatalf("channels=%#v err=%v", channels, err)
	}
	channel := channels[0]
	if channel.Type != "bark" || !channel.Enabled {
		t.Fatalf("unexpected channel: %#v", channel)
	}
	updateInput, _ := json.Marshal(map[string]any{"channel_id": channel.ID, "changes": map[string]any{"enabled": false}})
	applyAutomationChangeset(t, server, principal, "channel-update", automation.OperationRequest{Capability: "notification_channels.update", Input: updateInput})
	updated, err := db.GetNotificationChannel(ctx, channel.ID)
	if err != nil || updated.Enabled {
		t.Fatalf("channel not updated: %#v err=%v", updated, err)
	}
	deleteInput, _ := json.Marshal(map[string]any{"channel_id": channel.ID, "confirm": true})
	applyAutomationChangeset(t, server, principal, "channel-delete", automation.OperationRequest{Capability: "notification_channels.delete", Input: deleteInput})
	if _, err := db.GetNotificationChannel(ctx, channel.ID); err == nil {
		t.Fatal("deleted channel still exists")
	}
}

func TestServiceAccountAndTokenCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "service-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111118", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	create := json.RawMessage(`{"service_account":{"name":"automation","scopes":["settings:read"],"resource_filter":{},"allowed_cidrs":["192.0.2.0/24"],"rate_limit_per_minute":30,"max_concurrency":2}}`)
	applyAutomationChangeset(t, server, principal, "service-create", automation.OperationRequest{Capability: "api_principals.create", Input: create})
	items, err := db.ListAPIPrincipals(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("service accounts=%#v err=%v", items, err)
	}
	serviceAccount := items[0]
	if serviceAccount.Name != "automation" || serviceAccount.RateLimitPerMinute != 30 || serviceAccount.MaxConcurrency != 2 {
		t.Fatalf("unexpected service account: %#v", serviceAccount)
	}
	listedPrincipals, err := server.queryManagementCapability(ctx, principal, "api_principals.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	assertCapabilityOutputSchema(t, server, "api_principals.list", listedPrincipals)
	update, _ := json.Marshal(map[string]any{"principal_id": serviceAccount.ID, "changes": map[string]any{"name": "automation-v2", "enabled": false}})
	applyAutomationChangeset(t, server, principal, "service-update", automation.OperationRequest{Capability: "api_principals.update", Input: update})
	updated, err := db.GetAPIPrincipal(ctx, serviceAccount.ID)
	if err != nil || updated.Name != "automation-v2" || updated.Enabled {
		t.Fatalf("updated service account=%#v err=%v", updated, err)
	}

	issue, _ := json.Marshal(map[string]any{"principal_id": serviceAccount.ID})
	applied := applyAutomationChangesetResult(t, server, principal, "token-issue", automation.OperationRequest{Capability: "api_tokens.issue", Input: issue})
	var oneTimeResult struct {
		Operations []struct {
			APIToken string `json:"api_token"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(applied.Result, &oneTimeResult); err != nil || len(oneTimeResult.Operations) != 1 || !strings.HasPrefix(oneTimeResult.Operations[0].APIToken, "obk_") {
		t.Fatalf("one-time token missing from immediate result: %s", applied.Result)
	}
	plainToken := oneTimeResult.Operations[0].APIToken
	persisted, err := server.automation.Get(ctx, applied.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted.Result), plainToken) || strings.Contains(string(persisted.Result), `"api_token":`) {
		t.Fatalf("one-time token leaked into persisted changeset: %s", persisted.Result)
	}
	tokens, err := db.ListAPITokens(ctx, serviceAccount.ID)
	encodedTokens, _ := json.Marshal(tokens)
	if err != nil || len(tokens) != 1 || strings.Contains(string(encodedTokens), plainToken) {
		t.Fatalf("token metadata=%#v err=%v", tokens, err)
	}
	listTokensInput, _ := json.Marshal(map[string]any{"principal_id": serviceAccount.ID})
	listedTokens, err := server.queryManagementCapability(ctx, principal, "api_tokens.list", listTokensInput)
	if err != nil {
		t.Fatal(err)
	}
	assertCapabilityOutputSchema(t, server, "api_tokens.list", listedTokens)
	listedTokensJSON, _ := json.Marshal(listedTokens)
	if strings.Contains(string(listedTokensJSON), plainToken) || strings.Contains(string(listedTokensJSON), "token_hash") {
		t.Fatalf("api_tokens.list leaked secret material: %s", listedTokensJSON)
	}
	revoke, _ := json.Marshal(map[string]any{"principal_id": serviceAccount.ID, "token_id": tokens[0].ID, "confirm": true})
	applyAutomationChangeset(t, server, principal, "token-revoke", automation.OperationRequest{Capability: "api_tokens.revoke", Input: revoke})
	tokens, err = db.ListAPITokens(ctx, serviceAccount.ID)
	if err != nil || tokens[0].RevokedAt == nil {
		t.Fatalf("token was not revoked: %#v err=%v", tokens, err)
	}
	deleteInput, _ := json.Marshal(map[string]any{"principal_id": serviceAccount.ID, "confirm": true})
	applyAutomationChangeset(t, server, principal, "service-delete", automation.OperationRequest{Capability: "api_principals.delete", Input: deleteInput})
	if _, err := db.GetAPIPrincipal(ctx, serviceAccount.ID); err == nil {
		t.Fatal("deleted service account still exists")
	}
}

func applyAutomationChangesetResult(t *testing.T, server *Server, principal application.Principal, idempotencyKey string, operation automation.OperationRequest) *model.AutomationChangeset {
	t.Helper()
	ctx := context.Background()
	draft, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{operation}})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := json.Marshal(draft.ExpectedRevisions)
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: idempotencyKey, BaseRevisions: base, Operations: []automation.OperationRequest{operation}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Approve(ctx, principal, changeset.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	applied, err := server.automation.Apply(ctx, principal, changeset.ID)
	if err != nil {
		t.Fatal(err)
	}
	return applied
}

func (s *Server) readSystemResource(ctx context.Context, principal application.Principal, name string) (any, error) {
	switch name {
	case "settings":
		items, err := s.store.ListSettings(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"settings": s.publicSettings(ctx, items)}, nil
	default:
		return nil, nil
	}
}
