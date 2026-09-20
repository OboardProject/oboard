package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestDeviceRetirementLeaseAndConfigTransition(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retirement.sqlite")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	srv := newTestServer(db, "retirement-secret", "")
	node := &model.Server{Name: "retirement", PublicIPv4: "203.0.113.41"}
	must(db.CreateServer(ctx, node))
	user := &model.User{Username: "account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid", ProxyPassword: "pass"}
	must(db.CreateUser(ctx, user))
	in := &model.Inbound{ServerID: node.ID, Name: "socks", Protocol: model.ProtocolSocks, Port: 1443, Enabled: true, ConfigJSON: "{}"}
	must(db.CreateInbound(ctx, in))
	grantTestPlanInboundNode(t, db, user.ID, in.ID)
	must(db.ReconcileProxyCredentials(ctx, srv.sessionSecret, []model.ProxyCredential{{UserID: user.ID, InboundID: in.ID, Protocol: in.Protocol}}))
	sqlDB, err := sql.Open("sqlite", path)
	must(err)
	defer sqlDB.Close()
	_, err = sqlDB.Exec(`update proxy_credentials set device_id_hash='existing',credential_epoch=1`)
	must(err)
	_, err = sqlDB.Exec(`insert into user_devices(id,device_id_hash,user_id,name,token_hash,token_prefix,credential_epoch,status,subscription_suspended,proxy_access_state,created_at,updated_at) values('d','existing',?,'old','hash','prefix',1,'active',0,'active','2030-01-01T00:00:00Z','2030-01-01T00:00:00Z')`, user.ID)
	must(err)
	at := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	deadline := at.Add(2 * time.Minute)
	batch, err := db.StartDeviceRetirement(ctx, "admin", "review", deadline, at)
	must(err)
	must(db.ReviewDeviceRetirement(ctx, batch.ID, user.ID, "account_authorized", "account verified"))
	must(db.BeginDeviceRetirementTransition(ctx, batch.ID, at))
	data, err := db.FullRoutingConfigData(ctx)
	must(err)
	data, err = srv.loadProxyCredentialData(ctx, data)
	must(err)
	identities := core.DataPlaneIdentities(data.Users)
	if len(identities) != 2 {
		t.Fatalf("transition identities=%d", len(identities))
	}
	legacy := core.UserCredentialForRoute(identities[1], in.ID, 0, in.Protocol)
	if legacy.AuthorizationKey == "" {
		t.Fatal("old imported credential lost")
	}
	creds, err := db.ListProxyCredentials(ctx)
	must(err)
	projection := buildAuthorizationProjection(1, at, data, creds)
	grants := projection.grantsAt(node.ID, at)
	if len(grants) != 1 || !grants[0].deadline.Equal(deadline) {
		t.Fatalf("lease not bounded by transition: %+v", grants)
	}
	if len(projection.grantsAt(node.ID, deadline)) != 0 {
		t.Fatal("lease survived deadline")
	}
	must(db.RevokeDeviceRetirement(ctx, batch.ID, at.Add(time.Minute)))
	data, err = db.FullRoutingConfigData(ctx)
	must(err)
	data, err = srv.loadProxyCredentialData(ctx, data)
	must(err)
	creds, err = db.ListProxyCredentials(ctx)
	must(err)
	if len(buildAuthorizationProjection(2, at.Add(time.Minute), data, creds).grantsAt(node.ID, at.Add(time.Minute))) != 0 {
		t.Fatal("revoked device still granted")
	}
}

func TestDeviceRetirementRequiresAdminChangeset(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "auth.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "secret", "")
	ctx := context.Background()
	input := json.RawMessage(`{"confirm":true,"deadline":"2099-01-01T00:00:00Z","reason":"migration"}`)
	for _, p := range []application.Principal{{ID: "viewer", Role: model.RoleViewer, Scopes: []string{"*"}}, {ID: "limited-admin", Role: model.RoleAdmin, Scopes: []string{"*"}, ResourceFilter: json.RawMessage(`{"users":{"mode":"selected","ids":[1]}}`)}} {
		_, err := srv.automation.ValidateDraft(ctx, p, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "device_retirement.start", Input: input}}})
		if err == nil {
			t.Fatal("unauthorized migration validated")
		}
	}
	if _, err := srv.validateDeviceRetirement(ctx, application.Principal{Role: model.RoleAdmin}, json.RawMessage(`{"confirm":false}`)); err == nil {
		t.Fatal("missing confirmation accepted")
	}
	adminUser := &model.User{Username: "migration-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "uuid", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, adminUser); err != nil {
		t.Fatal(err)
	}
	admin := application.HumanPrincipal(*adminUser, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	if err := db.CreateAPIPrincipal(ctx, &model.APIPrincipal{ID: admin.ID, OwnerUserID: &adminUser.ID, Name: admin.Name, Type: admin.Type, Enabled: true, Scopes: admin.Scopes, ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 60, MaxConcurrency: 2}); err != nil {
		t.Fatal(err)
	}
	valid, _ := json.Marshal(map[string]any{"confirm": true, "reason": "approved migration", "deadline": time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)})
	result := applyAutomationChangesetResult(t, srv, admin, "retirement-start", automation.OperationRequest{Capability: "device_retirement.start", Input: valid})
	if result.Status != model.ChangesetSucceeded {
		t.Fatalf("start Changeset: %s", result.Status)
	}
	batch, err := db.DeviceRetirementBatch(ctx, 1)
	if err != nil || batch.State != "review" {
		t.Fatalf("persisted batch=%+v err=%v", batch, err)
	}
	read, err := srv.queryDeviceRetirement(ctx, admin, json.RawMessage(`{"batch_id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	assertCapabilityOutputSchema(t, srv, "device_retirement.read", read)
}
