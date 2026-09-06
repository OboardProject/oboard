package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestProxyCredentialsPersistRotateAndNeverRevive(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "controller.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	scope := model.ProxyCredential{UserID: 1, InboundID: 2, PathID: 3, Protocol: model.ProtocolSocks}
	secret := "test-credential-encryption-secret"
	if err := s.ReconcileProxyCredentials(ctx, secret, []model.ProxyCredential{scope}); err != nil {
		t.Fatal(err)
	}
	load := func() model.ProxyCredential {
		t.Helper()
		users, err := s.LoadProxyCredentials(ctx, secret, []model.User{{ID: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if len(users[0].ProxyCredentials) != 1 {
			t.Fatal("missing credential")
		}
		return users[0].ProxyCredentials[0]
	}
	first := load()
	if len(first.Username) != 33 || len(first.Password) != 43 || len(first.UUID) != 36 {
		t.Fatal("unexpected credential entropy encoding")
	}
	var encrypted string
	if err := s.db.QueryRow("select material_encrypted from proxy_credentials where id=?", first.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encrypted, first.Password) {
		t.Fatal("plaintext password stored")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileProxyCredentials(ctx, secret, []model.ProxyCredential{scope}); err != nil {
		t.Fatal(err)
	}
	if load() != first {
		t.Fatal("reconcile/restart rotated stable credentials")
	}
	if err := s.ReconcileProxyCredentials(ctx, secret, nil); err != nil {
		t.Fatal(err)
	}
	users, err := s.LoadProxyCredentials(ctx, secret, []model.User{{ID: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(users[0].ProxyCredentials) != 0 {
		t.Fatal("revoked credential loaded")
	}
	if err := s.ReconcileProxyCredentials(ctx, secret, []model.ProxyCredential{scope}); err != nil {
		t.Fatal(err)
	}
	next := load()
	if next.ID == first.ID || next.Username == first.Username || next.Password == first.Password || next.UUID == first.UUID {
		t.Fatal("regrant reused revoked material")
	}
	var status, material string
	if err := s.db.QueryRow("select status,material_encrypted from proxy_credentials where id=?", first.ID).Scan(&status, &material); err != nil {
		t.Fatal(err)
	}
	if status != "revoked" || material != "" {
		t.Fatal("revocation tombstone missing")
	}
}

func TestProxyCredentialMigrationFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "previous-user", Status: "active", Role: model.RoleViewer, PasswordHash: "hash", ProxyUUID: "old-uuid", ProxyPassword: "old-password"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	// The preceding schema has the same users table and no scoped credentials.
	for _, statement := range []string{"drop trigger proxy_credentials_user_rotation", "drop trigger proxy_credentials_user_delete", "drop table proxy_credentials"} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err := s.LoadProxyCredentials(ctx, "test-secret", []model.User{*user})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded[0].ProxyCredentials) != 0 {
		t.Fatal("migration derived old credentials")
	}
	scope := model.ProxyCredential{UserID: user.ID, InboundID: 2, Protocol: model.ProtocolVLESS}
	if err := s.ReconcileProxyCredentials(ctx, "test-secret", []model.ProxyCredential{scope}); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.LoadProxyCredentials(ctx, "test-secret", []model.User{*user})
	if err != nil {
		t.Fatal(err)
	}
	credential := loaded[0].ProxyCredentials[0]
	if credential.Password == user.ProxyPassword || credential.UUID == user.ProxyUUID {
		t.Fatal("migration reused old authentication")
	}
	user.ProxyPassword = "rotated-account-password"
	if err := s.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	records, err := s.ListProxyCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Status != "revoked" {
		t.Fatal("account rotation did not revoke scope")
	}
}
