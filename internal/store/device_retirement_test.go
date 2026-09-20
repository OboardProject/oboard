package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestDeviceRetirementRestrictedPreviousState(t *testing.T) {
	for _, state := range []string{"suspended", "reject_new", "revoked"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "previous.sqlite")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { s.Close() }()
			user := &model.User{Username: "legacy", Status: "active", Role: model.RoleViewer, PasswordHash: "hash", ProxyUUID: "uuid", ProxyPassword: "password"}
			if err := s.CreateUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			account := model.ProxyCredential{UserID: user.ID, InboundID: 2, PathID: 3, Protocol: model.ProtocolSocks}
			if err := s.ReconcileProxyCredentials(ctx, "secret", []model.ProxyCredential{account}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`update proxy_credentials set device_id_hash='legacy-device',credential_epoch=1`); err != nil {
				t.Fatal(err)
			}
			status, suspended, access := "active", 0, "active"
			switch state {
			case "suspended":
				suspended = 1
			case "reject_new":
				access = "reject_new"
			case "revoked":
				status = "revoked"
			}
			_, err = s.db.ExecContext(ctx, `insert into user_devices(id,device_id_hash,user_id,name,token_hash,token_prefix,credential_epoch,status,subscription_suspended,proxy_access_state,created_at,updated_at) values('device','legacy-device',?,'old','hash','prefix',1,?,?,?,?,?)`, user.ID, status, suspended, access, now(), now())
			if err != nil {
				t.Fatal(err)
			}
			// Reproduce the prior schema without the extension, then reopen it.
			if _, err = s.db.Exec(`drop table device_retirement_reviews`); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := s.PreviewDeviceRetirement(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if preview.RestrictedObjects != 1 || preview.ReviewAccounts != 0 || preview.LegacyCredentials != 1 || preview.NodeRevocationConfirmed {
				t.Fatalf("unexpected dry-run: %+v", preview)
			}
			for i := 0; i < 2; i++ {
				if err := s.ReconcileProxyCredentials(ctx, "secret", []model.ProxyCredential{account}); err != nil {
					t.Fatal(err)
				}
			}
			credentials, err := s.ListProxyCredentials(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(credentials) != 1 || credentials[0].Status != "active" || credentials[0].DeviceIDHash == "" {
				t.Fatalf("changed legacy credentials or issued bypass: %+v", credentials)
			}
			// An incidental legacy-state change cannot clear a persisted review hold.
			if _, err = s.db.Exec(`update user_devices set status='active',subscription_suspended=0,proxy_access_state='active'`); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.ReconcileProxyCredentials(ctx, "secret", []model.ProxyCredential{account}); err != nil {
				t.Fatal(err)
			}
			credentials, err = s.ListProxyCredentials(ctx)
			if err != nil || len(credentials) != 1 || credentials[0].DeviceIDHash == "" {
				t.Fatalf("restart bypassed review: %+v %v", credentials, err)
			}
			preview, err = s.PreviewDeviceRetirement(ctx)
			if err != nil || preview.ReviewAccounts != 1 {
				t.Fatalf("review did not persist: %+v %v", preview, err)
			}
			got, err := s.GetUser(ctx, user.ID)
			if err != nil || got.Status != "active" {
				t.Fatalf("account state changed: %+v %v", got, err)
			}
			// Disabled accounts and removed grants have no corresponding desired scope.
			if err := s.ReconcileProxyCredentials(ctx, "secret", nil); err != nil {
				t.Fatal(err)
			}
			credentials, err = s.ListProxyCredentials(ctx)
			if err != nil || len(credentials) != 1 || credentials[0].Status != "revoked" {
				t.Fatalf("review swallowed authorization removal: %+v %v", credentials, err)
			}
			var material string
			if err := s.db.QueryRow(`select material_encrypted from proxy_credentials`).Scan(&material); err != nil || material != "" {
				t.Fatalf("revoked material retained: %v", err)
			}
		})
	}
}

func TestDeviceRetirementFreshAccountUnaffected(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "fresh.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	preview, err := s.PreviewDeviceRetirement(ctx)
	if err != nil || preview != (DeviceRetirementPreflight{GraceState: "not_initialized"}) {
		t.Fatalf("fresh preview: %+v %v", preview, err)
	}
	scope := model.ProxyCredential{UserID: 1, InboundID: 2, PathID: 3, Protocol: model.ProtocolSocks}
	if err := s.ReconcileProxyCredentials(ctx, "secret", []model.ProxyCredential{scope}); err != nil {
		t.Fatal(err)
	}
	credentials, err := s.ListProxyCredentials(ctx)
	if err != nil || len(credentials) != 1 {
		t.Fatalf("fresh issuance failed: %+v %v", credentials, err)
	}
}
