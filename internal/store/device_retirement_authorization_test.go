package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestDeviceRetirementRejectsDeviceIssuance(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	account := model.ProxyCredential{UserID: 1, InboundID: 2, PathID: 3, Protocol: model.ProtocolSocks}
	device := account
	device.DeviceIDHash, device.CredentialEpoch = "device", 1
	if err := s.ReconcileProxyCredentials(context.Background(), "secret", []model.ProxyCredential{account, device}); err == nil {
		t.Fatal("accepted device issuance")
	}
	credentials, err := s.ListProxyCredentials(context.Background())
	if err != nil || len(credentials) != 0 {
		t.Fatalf("invalid batch had side effects: %+v %v", credentials, err)
	}
}

func TestDeviceRetirementHoldRequiresExactAuthorizedAccountScope(t *testing.T) {
	for _, changed := range []string{"none", "inbound", "path", "protocol", "disabled"} {
		t.Run(changed, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "controller.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			// A pre-existing account row sorts before the legacy row. Matching it must
			// not consume the authorization evidence used to decide the legacy hold.
			_, err = s.db.Exec(`insert into proxy_credentials(id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,protocol,status,material_encrypted,created_at) values
 ('a',1,2,3,'',0,'socks','active','fixture','old'),
 ('z',1,2,3,'device',1,'socks','active','fixture','old')`)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`insert into device_retirement_reviews values(1,'legacy_device_restriction','old')`); err != nil {
				t.Fatal(err)
			}
			account := model.ProxyCredential{UserID: 1, InboundID: 2, PathID: 3, Protocol: model.ProtocolSocks}
			switch changed {
			case "inbound":
				account.InboundID++
			case "path":
				account.PathID++
			case "protocol":
				account.Protocol = model.ProtocolSSH
			}
			desired := []model.ProxyCredential{account}
			if changed == "disabled" {
				desired = nil
			}
			if err := s.ReconcileProxyCredentials(ctx, "secret", desired); err != nil {
				t.Fatal(err)
			}
			var status, material string
			if err := s.db.QueryRow(`select status,material_encrypted from proxy_credentials where id='z'`).Scan(&status, &material); err != nil {
				t.Fatal(err)
			}
			if changed == "none" {
				if status != "active" || material != "fixture" {
					t.Fatal("authorized hold lost")
				}
			} else if status != "revoked" || material != "" {
				t.Fatal("hold swallowed scope revocation")
			}
		})
	}
}
