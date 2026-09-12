package controller

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// TestSSHDeliveryStatusReportsUnprojectedUsers covers the gap that made an
// unusable account invisible: an SSH inbound is projected into the plan whether
// or not any account could be attached to it, and the listener digest ignores
// the user set, so a server whose authorized accounts all dropped out still
// compared equal to its deployed state and reported as converged.
func TestSSHDeliveryStatusReportsUnprojectedUsers(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "projection-test-secret", "")
	server := &model.Server{Name: "ssh-node", PublicIPv4: "203.0.113.51"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "ssh", Protocol: model.ProtocolSSH, Port: 2222, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inbound.ID)

	// The account is authorized on this SSH inbound but no credential has been
	// issued for the scope, so it cannot be deployed.
	servers := []model.Server{*server}
	srv.annotateSSHUserDeliveryStatuses(ctx, servers)
	if servers[0].UsersConfirmed {
		t.Fatal("server reported confirmed while an authorized account was missing from the plan")
	}
	if servers[0].UsersPendingReason != "ssh_users_unprojected" {
		t.Fatalf("pending reason = %q", servers[0].UsersPendingReason)
	}
	if !strings.Contains(servers[0].UsersPendingDetail, "credential_unavailable") {
		t.Fatalf("pending detail = %q", servers[0].UsersPendingDetail)
	}
	if servers[0].UsersFallback != "" {
		t.Fatalf("an operator-resolved state offered a retry fallback: %q", servers[0].UsersFallback)
	}

	// Once the credential exists the account projects normally, and the server
	// falls back to the ordinary unverified-deployment state instead.
	if err := srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	servers = []model.Server{*server}
	srv.annotateSSHUserDeliveryStatuses(ctx, servers)
	if servers[0].UsersPendingReason != "ssh_authentication_unverified" {
		t.Fatalf("pending reason after issuance = %q detail=%q", servers[0].UsersPendingReason, servers[0].UsersPendingDetail)
	}
}
