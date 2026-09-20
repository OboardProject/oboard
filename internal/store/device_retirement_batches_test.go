package store

import (
	"context"
	"github.com/OboardProject/oboard/internal/model"
	"path/filepath"
	"testing"
	"time"
)

func TestDeviceRetirementBatchRealPreviousState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retirement.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user := &model.User{Username: "legacy-batch", Status: "active", Role: model.RoleViewer, PasswordHash: "hash", ProxyUUID: "uuid", ProxyPassword: "password"}
	must(s.CreateUser(ctx, user))
	server := &model.Server{Name: "retirement-node"}
	must(s.CreateServer(ctx, server))
	_, err = s.db.Exec(`update servers set agent_id='old-agent',status='online' where id=?`, server.ID)
	must(err)
	scope := model.ProxyCredential{UserID: user.ID, InboundID: 2, Protocol: model.ProtocolSocks}
	must(s.ReconcileProxyCredentials(ctx, "secret", []model.ProxyCredential{scope}))
	_, err = s.db.Exec(`update proxy_credentials set device_id_hash='legacy-device',credential_epoch=1`)
	must(err)
	_, err = s.db.Exec(`insert into user_devices(id,device_id_hash,user_id,name,token_hash,token_prefix,credential_epoch,status,subscription_suspended,proxy_access_state,created_at,updated_at) values('d','legacy-device',?,'old','hash','prefix',1,'active',0,'active',?,?)`, user.ID, now(), now())
	must(err)
	at := time.Now().UTC()
	deadline := at.Add(time.Hour)
	if _, err = s.StartDeviceRetirement(ctx, "admin", "invalid", at.Add(15*24*time.Hour), at); err == nil {
		t.Fatal("unbounded deadline accepted")
	}
	batch, err := s.StartDeviceRetirement(ctx, "admin", "controlled migration", deadline, at)
	must(err)
	if err = s.BeginDeviceRetirementTransition(ctx, batch.ID, at); err == nil {
		t.Fatal("unreviewed transition accepted")
	}
	must(s.ReviewDeviceRetirement(ctx, batch.ID, user.ID, "account_authorized", "verified account authorization"))
	must(s.BeginDeviceRetirementTransition(ctx, batch.ID, at))
	loaded, err := s.LoadProxyCredentials(ctx, "secret", []model.User{*user})
	must(err)
	if !loaded[0].DeviceTransitionUntil.Equal(deadline) || !loaded[0].ProxyCredentials[0].DeviceTransitionAllowed {
		t.Fatal("existing material not eligible for transition")
	}
	s.Close()
	s, err = Open(path)
	must(err)
	must(s.RevokeDeviceRetirement(ctx, batch.ID, at.Add(time.Minute)))
	must(s.ReconcileProxyCredentials(ctx, "secret", []model.ProxyCredential{scope}))
	var active int
	must(s.db.QueryRow(`select count(*) from proxy_credentials where device_id_hash<>'' and (status='active' or material_encrypted<>'')`).Scan(&active))
	if active != 0 {
		t.Fatal("reconciliation revived revoked material")
	}
	if err = s.BeginDeviceRetirementTransition(ctx, batch.ID, at); err == nil {
		t.Fatal("rollback revived transition")
	}
	must(s.BindDeviceRetirementDeployment(ctx, batch.ID, 42))
	if err = s.FinalizeDeviceRetirement(ctx, batch.ID); err == nil {
		t.Fatal("missing acknowledgement accepted")
	}
	_, err = s.db.Exec(`insert into configuration_sync_states(server_id,wanted_revision,state,last_config_version,changed_at,updated_at) values(?,1,'synced',41,?,?) on conflict(server_id) do update set state='synced',last_config_version=41`, server.ID, now(), now())
	must(err)
	if err = s.FinalizeDeviceRetirement(ctx, batch.ID); err == nil {
		t.Fatal("old acknowledgement accepted")
	}
	_, err = s.db.Exec(`update configuration_sync_states set last_config_version=42 where server_id=?`, server.ID)
	must(err)
	_, err = s.db.Exec(`update servers set status='offline' where id=?`, server.ID)
	must(err)
	if err = s.FinalizeDeviceRetirement(ctx, batch.ID); err == nil {
		t.Fatal("offline node accepted")
	}
	_, err = s.db.Exec(`update servers set status='online' where id=?`, server.ID)
	must(err)
	if err = s.FinalizeDeviceRetirement(ctx, batch.ID); err == nil {
		t.Fatal("configuration state without runtime acknowledgements accepted")
	}
	stamp := at.Add(2 * time.Minute).Format(time.RFC3339Nano)
	_, err = s.db.Exec(`insert into authorization_states(server_id,desired_revision,desired_digest,confirmed_revision,confirmed_digest,confirmed_at,updated_at) values(?,1,'revoked',1,'revoked',?,?)`, server.ID, stamp, stamp)
	must(err)
	_, err = s.db.Exec(`insert into runtime_user_states(server_id,desired_revision,desired_digest,confirmed_revision,confirmed_digest,confirmed_at,updated_at) values(?,1,'revoked',1,'revoked',?,?)`, server.ID, stamp, stamp)
	must(err)
	if err = s.FinalizeDeviceRetirement(ctx, batch.ID); err == nil {
		t.Fatal("semantic configuration state without deployment ACK accepted")
	}
	_, err = s.db.Exec(`insert into agent_tasks(server_id,type,payload_json,status,config_version,nonce,created_at,updated_at) values(?,'apply_deployment','{}','succeeded',42,'test',?,?)`, server.ID, stamp, stamp)
	must(err)
	must(s.FinalizeDeviceRetirement(ctx, batch.ID))
	must(s.FinalizeDeviceRetirement(ctx, batch.ID))
	if err = s.ContractDeviceRetirement(ctx, batch.ID); err == nil {
		t.Fatal("unsafe contraction accepted")
	}
	got, err := s.GetUser(ctx, user.ID)
	must(err)
	if got.Status != "active" {
		t.Fatal("migration changed account status")
	}
}
