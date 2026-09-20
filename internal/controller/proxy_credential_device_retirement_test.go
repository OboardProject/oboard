package controller

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// Startup must not destroy legacy material before controlled retirement.
func TestStartupPreservesPendingDeviceScopedCredentials(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "controller.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "device-retirement-secret", "")
	server := &model.Server{Name: "ssh-node", PublicIPv4: "203.0.113.41"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "old-uuid", ProxyPassword: "old-password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "ssh", Protocol: model.ProtocolSSH, Port: 2222, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inbound.ID)
	pathID := core.SSHDirectBranchPathID(inbound.ID)

	// The pre-withdrawal state: the only active credential for this grant is
	// bound to a device identity.
	strandedScope := model.ProxyCredential{UserID: user.ID, InboundID: inbound.ID, PathID: pathID, Protocol: model.ProtocolSSH}
	if err := db.ReconcileProxyCredentials(ctx, srv.sessionSecret, []model.ProxyCredential{strandedScope}); err != nil {
		t.Fatal(err)
	}
	fixtureDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fixtureDB.Close()
	if _, err := fixtureDB.Exec(`update proxy_credentials set device_id_hash='0123456789abcdef',credential_epoch=2`); err != nil {
		t.Fatal(err)
	}
	seeded, err := db.LoadProxyCredentials(ctx, srv.sessionSecret, []model.User{*user})
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded[0].ProxyCredentials) != 1 || seeded[0].ProxyCredentials[0].DeviceIDHash == "" {
		t.Fatalf("device-scoped seed not established: %#v", seeded[0].ProxyCredentials)
	}
	// Before reconciliation, unreviewed material is not implicitly projected.
	deviceIdentity := seeded[0]
	deviceIdentity.DeviceIDHash, deviceIdentity.CredentialEpoch = "0123456789abcdef", 2
	if core.UserCredentialForRoute(deviceIdentity, inbound.ID, pathID, model.ProtocolSSH).AuthorizationKey != "" {
		t.Fatal("unreviewed device material was implicitly projected")
	}
	// The account identity cannot reach it, which is exactly why leaving it
	// active would strand the grant.
	if core.UserCredentialForRoute(seeded[0], inbound.ID, pathID, model.ProtocolSSH).AuthorizationKey != "" {
		t.Fatal("account identity selected a device-scoped credential")
	}

	if err := srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}

	reconciled, err := db.LoadProxyCredentials(ctx, srv.sessionSecret, []model.User{*user})
	if err != nil {
		t.Fatal(err)
	}
	legacyCount := 0
	for _, credential := range reconciled[0].ProxyCredentials {
		if credential.DeviceIDHash != "" {
			legacyCount++
			if credential.ID != seeded[0].ProxyCredentials[0].ID || credential.Password != seeded[0].ProxyCredentials[0].Password || credential.Username != seeded[0].ProxyCredentials[0].Username || credential.UUID != seeded[0].ProxyCredentials[0].UUID {
				t.Fatal("legacy credential material changed")
			}
		}
	}
	if legacyCount != 1 {
		t.Fatal("legacy credential was automatically retired")
	}
	var reason string
	if err := fixtureDB.QueryRow(`select reason_code from device_retirement_reviews where user_id=?`, user.ID).Scan(&reason); err != nil || reason != "transition_pending" {
		t.Fatalf("pending review: %q, %v", reason, err)
	}
	selected := core.UserCredentialForRoute(reconciled[0], inbound.ID, pathID, model.ProtocolSSH)
	if selected.AuthorizationKey == "" || selected.ProxyUsername == "" || selected.ProxyPassword == "" {
		t.Fatalf("account-scoped replacement was not issued: %#v", reconciled[0].ProxyCredentials)
	}
}
