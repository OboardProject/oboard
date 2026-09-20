package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/core"

	"github.com/OboardProject/oboard/internal/model"
)

func TestDeviceRetirementPendingPreservesMaterialUntilAuthorizationRemoval(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pending.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	user := &model.User{Username: "legacy-pending", Status: "active", Role: model.RoleViewer, PasswordHash: "hash", ProxyUUID: "uuid", ProxyPassword: "password"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	scope := model.ProxyCredential{UserID: 1, InboundID: 2, PathID: 3, Protocol: model.ProtocolSocks}
	if err := s.ReconcileProxyCredentials(ctx, "secret", []model.ProxyCredential{scope}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update proxy_credentials set device_id_hash='legacy',credential_epoch=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`insert into user_devices(id,device_id_hash,user_id,name,token_hash,token_prefix,credential_epoch,status,subscription_suspended,proxy_access_state,created_at,updated_at) values('d','legacy',1,'old','hash','prefix',1,'active',0,'active',?,?)`, now(), now()); err != nil {
		t.Fatal(err)
	}
	var deadline time.Time
	var material string
	if err := s.db.QueryRow(`select material_encrypted from proxy_credentials`).Scan(&material); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.ReconcileProxyCredentials(ctx, "secret", []model.ProxyCredential{scope}); err != nil {
			t.Fatal(err)
		}
		var actual, reason string
		if err := s.db.QueryRow(`select material_encrypted from proxy_credentials where device_id_hash='legacy' and status='active'`).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != material || actual == "" {
			t.Fatal("legacy material changed")
		}
		if err := s.db.QueryRow(`select reason_code from device_retirement_reviews where user_id=1`).Scan(&reason); err != nil || reason != "transition_pending" {
			t.Fatalf("reason=%q err=%v", reason, err)
		}
		var accounts int
		if err := s.db.QueryRow(`select count(*) from proxy_credentials where device_id_hash='' and status='active'`).Scan(&accounts); err != nil || accounts != 1 {
			t.Fatalf("account credentials=%d err=%v", accounts, err)
		}
		loaded, err := s.LoadProxyCredentials(ctx, "secret", []model.User{{ID: 1, Status: "active"}})
		if err != nil {
			t.Fatal(err)
		}
		identities := core.DataPlaneIdentities(loaded)
		if len(identities) != 2 || core.UserCredentialForRoute(identities[1], 2, 3, model.ProtocolSocks).AuthorizationKey == "" {
			t.Fatal("pre-batch legacy client lost its exact scope")
		}
		if i == 0 {
			deadline = loaded[0].DeviceTransitionUntil
		}
		if !loaded[0].DeviceTransitionUntil.Equal(deadline) || !deadline.After(time.Now()) || deadline.After(time.Now().Add(14*24*time.Hour)) {
			t.Fatal("grace renewed or unbounded")
		}
		preview, err := s.PreviewDeviceRetirement(ctx)
		if err != nil || preview.GraceDeadline == "" || preview.GraceState != "review_required" || preview.NodeRevocationConfirmed {
			t.Fatalf("preflight=%+v err=%v", preview, err)
		}
		s.Close()
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	at := time.Now().UTC()
	batch, err := s.StartDeviceRetirement(ctx, "admin", "review legacy clients", at.Add(14*24*time.Hour), at)
	if err != nil || batch.Deadline != deadline.Format(time.RFC3339Nano) {
		t.Fatalf("batch extended initial grace: %+v %v", batch, err)
	}
	if _, err := s.db.Exec(`update user_devices set proxy_access_state='reject_new'`); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadProxyCredentials(ctx, "secret", []model.User{{ID: 1, Status: "active"}})
	if err != nil || len(core.DataPlaneIdentities(loaded)) != 1 {
		t.Fatal("restricted legacy identity admitted")
	}
	if _, err := s.db.Exec(`update user_devices set proxy_access_state='active'; update device_retirement_initial_grace set deadline='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileProxyCredentials(ctx, "secret", []model.ProxyCredential{scope}); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.LoadProxyCredentials(ctx, "secret", []model.User{{ID: 1, Status: "active"}})
	if err != nil || len(core.DataPlaneIdentities(loaded)) != 1 {
		t.Fatal("expired grace renewed")
	}
	if err := s.ReconcileProxyCredentials(ctx, "secret", nil); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := s.db.QueryRow(`select count(*) from proxy_credentials where status='active' or material_encrypted<>''`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("authorization removal retained material: %d %v", remaining, err)
	}
}
