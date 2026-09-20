package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

func TestDeviceRetirementInitialGrace(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "grace.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seedMaterial := func(id string) {
		t.Helper()
		encrypted, err := security.EncryptSecret("secret", "proxy-credential:"+id, `{"username":"old-client","password":"old-password","uuid":"old-uuid"}`)
		if err != nil {
			t.Fatal(err)
		}
		exec(`update proxy_credentials set material_encrypted=? where id=?`, encrypted, id)
	}
	user := model.User{Username: "grace", Status: "active", Role: model.RoleViewer, PasswordHash: "hash", ProxyUUID: "uuid", ProxyPassword: "password"}
	if err := s.CreateUser(ctx, &user); err != nil {
		t.Fatal(err)
	}
	scope := model.ProxyCredential{UserID: user.ID, InboundID: 2, PathID: 3, Protocol: model.ProtocolSocks}
	reconcile := func(scopes []model.ProxyCredential, when time.Time) {
		t.Helper()
		if err := s.reconcileProxyCredentialsAt(ctx, "secret", scopes, when); err != nil {
			t.Fatal(err)
		}
	}
	reconcile([]model.ProxyCredential{scope}, at)
	exec(`update proxy_credentials set device_id_hash='normal',credential_epoch=1`)
	exec(`insert into user_devices(id,device_id_hash,user_id,name,token_hash,token_prefix,credential_epoch,status,subscription_suspended,proxy_access_state,created_at,updated_at) values('normal','normal',?,'old','hash','prefix',1,'active',0,'active',?,?)`, user.ID, now(), now())
	// Seed actual previous-model encrypted material, including an explicitly restricted device.
	exec(`insert into proxy_credentials(id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,protocol,status,material_encrypted,created_at) select 'restricted',user_id,inbound_id,path_id,'restricted',1,protocol,status,material_encrypted,created_at from proxy_credentials where device_id_hash='normal'`)
	exec(`insert into user_devices(id,device_id_hash,user_id,name,token_hash,token_prefix,credential_epoch,status,subscription_suspended,proxy_access_state,created_at,updated_at) values('restricted','restricted',?,'old','hash2','prefix',1,'active',0,'reject_new',?,?)`, user.ID, now(), now())
	seedMaterial("restricted")
	var original string
	if err := s.db.QueryRow(`select material_encrypted from proxy_credentials where device_id_hash='normal'`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	deadline := at.Add(14 * 24 * time.Hour)
	load := func(when time.Time, wantDeadline time.Time, wantLegacy bool) model.User {
		t.Helper()
		loaded, err := s.loadProxyCredentialsAt(ctx, "secret", []model.User{user}, when)
		if err != nil {
			t.Fatal(err)
		}
		if !loaded[0].DeviceTransitionUntil.Equal(wantDeadline) {
			t.Fatalf("deadline=%s want=%s", loaded[0].DeviceTransitionUntil, wantDeadline)
		}
		identities := core.DataPlaneIdentitiesAt(loaded, when)
		want := 1
		if wantLegacy {
			want++
		}
		if len(identities) != want {
			t.Fatalf("identities=%d want=%d", len(identities), want)
		}
		if wantLegacy {
			legacy := identities[1]
			if legacy.DeviceIDHash != "normal" || core.UserCredentialForRouteAt(legacy, 2, 3, model.ProtocolSocks, when).AuthorizationKey == "" {
				t.Fatal("existing authorized client cannot select its exact credential")
			}
			if core.UserCredentialForRouteAt(legacy, 2, 4, model.ProtocolSocks, when).AuthorizationKey != "" {
				t.Fatal("legacy scope expanded to another route")
			}
			if core.UserCredentialForRouteAt(legacy, 2, 3, model.ProtocolSocks, wantDeadline).AuthorizationKey != "" || len(core.DataPlaneIdentitiesAt(loaded, wantDeadline)) != 1 {
				t.Fatal("cached credential survived its deadline")
			}
		}
		return loaded[0]
	}
	for i := 0; i < 3; i++ {
		when := at.Add(time.Duration(i) * time.Hour)
		reconcile([]model.ProxyCredential{scope}, when)
		load(when, deadline, true)
		var actual string
		if err := s.db.QueryRow(`select material_encrypted from proxy_credentials where device_id_hash='normal'`).Scan(&actual); err != nil || actual != original {
			t.Fatalf("material changed: %v", err)
		}
		s.Close()
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	// An active legacy scope first appearing after discovery cannot join the grace.
	exec(`insert into proxy_credentials(id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,protocol,status,material_encrypted,created_at) select 'late',user_id,inbound_id,4,device_id_hash,credential_epoch,protocol,status,material_encrypted,created_at from proxy_credentials where device_id_hash='normal'`)
	seedMaterial("late")
	late := scope
	late.PathID = 4
	reconcile([]model.ProxyCredential{scope, late}, at.Add(3*time.Hour))
	load(at.Add(3*time.Hour), deadline, true)
	batch, err := s.StartDeviceRetirement(ctx, "admin", "review", at.Add(15*24*time.Hour), at.Add(24*time.Hour))
	if err != nil || batch.Deadline != deadline.Format(time.RFC3339Nano) {
		t.Fatalf("batch extended grace: %+v %v", batch, err)
	}
	load(at.Add(24*time.Hour), deadline, true)
	if err := s.ReviewDeviceRetirement(ctx, batch.ID, user.ID, "retain_restriction", "keep restriction"); err != nil {
		t.Fatal(err)
	}
	load(at.Add(24*time.Hour), deadline, false)
	if err := s.ReviewDeviceRetirement(ctx, batch.ID, user.ID, "account_authorized", "reviewed account"); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginDeviceRetirementTransition(ctx, batch.ID, at.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Explicit review cannot widen the frozen upgrade scopes or revive restrictions.
	load(at.Add(24*time.Hour), deadline, true)
	exec(`update user_devices set proxy_access_state='active' where id='restricted'`)
	load(at.Add(24*time.Hour), deadline, true)
	shorter := at.Add(2 * 24 * time.Hour)
	exec(`update device_retirement_batches set deadline=?`, shorter.Format(time.RFC3339Nano))
	load(at.Add(24*time.Hour), shorter, true)
	exec(`update user_devices set subscription_suspended=1 where id='normal'`)
	load(at.Add(24*time.Hour), shorter, false)
	exec(`update user_devices set subscription_suspended=0 where id='normal'`)
	reconcile([]model.ProxyCredential{scope, late}, shorter)
	load(shorter, time.Time{}, false)
	reconcile(nil, shorter.Add(time.Hour))
	var remaining int
	if err := s.db.QueryRow(`select count(*) from proxy_credentials where status='active' or material_encrypted<>''`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("real authorization removal retained credentials: %d %v", remaining, err)
	}
}
