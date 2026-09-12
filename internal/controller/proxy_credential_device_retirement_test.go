package controller

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// TestStartupRetiresStrandedDeviceScopedCredentials starts from the real state
// an installation carried before device-specific subscriptions were withdrawn:
// active proxy credentials scoped to a device hash and credential epoch.
//
// Nothing issues a device-scoped identity any more, and credential selection
// matches a stored row against the account identity, so such a row can never be
// selected again. It must not simply be stranded there - the account would hold
// an SSH inbound grant with no usable credential and would silently drop out of
// the deployed plan. Startup reconciliation consumes the complete desired scope
// set, so it has to retire the stranded row and issue the account-scoped
// replacement without any dedicated migration.
func TestStartupRetiresStrandedDeviceScopedCredentials(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
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
	strandedScope := model.ProxyCredential{UserID: user.ID, InboundID: inbound.ID, PathID: pathID, DeviceIDHash: "0123456789abcdef", CredentialEpoch: 2, Protocol: model.ProtocolSSH}
	if err := db.ReconcileProxyCredentials(ctx, srv.sessionSecret, []model.ProxyCredential{strandedScope}); err != nil {
		t.Fatal(err)
	}
	seeded, err := db.LoadProxyCredentials(ctx, srv.sessionSecret, []model.User{*user})
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded[0].ProxyCredentials) != 1 || seeded[0].ProxyCredentials[0].DeviceIDHash == "" {
		t.Fatalf("device-scoped seed not established: %#v", seeded[0].ProxyCredentials)
	}
	deviceIdentity := seeded[0]
	deviceIdentity.DeviceIDHash, deviceIdentity.CredentialEpoch = "0123456789abcdef", 2
	if core.UserCredentialForRoute(deviceIdentity, inbound.ID, pathID, model.ProtocolSSH).AuthorizationKey == "" {
		t.Fatal("seeded credential is not selectable by the identity that owned it")
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
	for _, credential := range reconciled[0].ProxyCredentials {
		if credential.DeviceIDHash != "" || credential.CredentialEpoch != 0 {
			t.Fatalf("stranded device-scoped credential survived startup: %#v", credential)
		}
	}
	selected := core.UserCredentialForRoute(reconciled[0], inbound.ID, pathID, model.ProtocolSSH)
	if selected.AuthorizationKey == "" || selected.ProxyUsername == "" || selected.ProxyPassword == "" {
		t.Fatalf("account-scoped replacement was not issued: %#v", reconciled[0].ProxyCredentials)
	}
}
