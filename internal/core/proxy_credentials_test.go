package core

import (
	"encoding/json"
	"github.com/OboardProject/oboard/internal/model"
	"testing"
)

func TestProxyCredentialSelectionRequiresExactPersistedScope(t *testing.T) {
	user := model.User{ID: 1, Username: "account", Status: "active", ProxyUUID: "old-uuid", ProxyPassword: "old-password"}
	if got := UserCredentialForRoute(user, 2, 3, model.ProtocolSocks); got.ProxyPassword != "" || got.ProxyUsername != "" || got.AuthorizationKey != "" {
		t.Fatal("derived legacy authentication")
	}
	credential := model.ProxyCredential{ID: "opaque-key", UserID: 1, InboundID: 2, PathID: 3, Protocol: model.ProtocolSocks, Status: "active", Username: "opaque-login", Password: "random-material", UUID: "random-uuid"}
	user.ProxyCredentials = []model.ProxyCredential{credential}
	selected := UserCredentialForRoute(user, 2, 3, model.ProtocolSocks)
	if selected.ProxyUsername != credential.Username || selected.ProxyPassword != credential.Password || selected.AuthorizationKey != credential.ID {
		t.Fatal("persisted material not selected")
	}
	for _, scope := range []struct {
		inbound, path int64
		protocol      model.Protocol
	}{{2, 4, model.ProtocolSocks}, {4, 3, model.ProtocolSocks}, {2, 3, model.ProtocolSSH}} {
		if UserCredentialForRoute(user, scope.inbound, scope.path, scope.protocol).AuthorizationKey != "" {
			t.Fatal("credential crossed authorization scopes")
		}
	}
	device := user
	device.DeviceIDHash, device.CredentialEpoch = "device", 1
	if UserCredentialForRoute(device, 2, 3, model.ProtocolSocks).AuthorizationKey != "" {
		t.Fatal("device-scoped identity inherited account credential")
	}
	user.ProxyCredentials[0].Status = "revoked"
	if UserCredentialForRoute(user, 2, 3, model.ProtocolSocks).AuthorizationKey != "" {
		t.Fatal("revoked credential selected")
	}
}

// TestIncoherentDeviceIdentityIsNeverProjected starts from the stale state a
// credential rotation can leave behind: an account row whose credential epoch
// moved without a device hash (or the reverse), plus a proxy credential still
// stored against that same pair. Issuance already refuses such an identity, so
// selection must refuse it too - otherwise the secrets reach an SSH deployment
// payload and the Agent rejects the entire plan for that server, taking every
// other user on the node down with it.
func TestIncoherentDeviceIdentityIsNeverProjected(t *testing.T) {
	for _, test := range []struct {
		name         string
		deviceIDHash string
		epoch        int64
		coherent     bool
	}{
		{"legacy account", "", 0, true},
		{"device bound", "0123456789abcdef", 2, true},
		{"epoch without device", "", 4, false},
		{"device without epoch", "0123456789abcdef", 0, false},
		{"device with negative epoch", "0123456789abcdef", -1, false},
		{"blank device with epoch", "   ", 4, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ConsistentDeviceCredentialIdentity(test.deviceIDHash, test.epoch); got != test.coherent {
				t.Fatalf("coherent = %v want %v", got, test.coherent)
			}
			user := model.User{ID: 1, Username: "account", Status: "active", DeviceIDHash: test.deviceIDHash, CredentialEpoch: test.epoch}
			user.ProxyCredentials = []model.ProxyCredential{{
				ID: "opaque-key", UserID: 1, InboundID: 2, PathID: 3, Protocol: model.ProtocolSSH, Status: "active",
				DeviceIDHash: test.deviceIDHash, CredentialEpoch: test.epoch,
				Username: "opaque-login", Password: "random-material", UUID: "random-uuid",
			}}
			selected := UserCredentialForRoute(user, 2, 3, model.ProtocolSSH)
			if test.coherent {
				if selected.AuthorizationKey != "opaque-key" || selected.ProxyPassword == "" {
					t.Fatalf("coherent identity lost its credential: %#v", selected)
				}
				return
			}
			if selected.AuthorizationKey != "" || selected.ProxyUsername != "" || selected.ProxyPassword != "" || selected.ProxyUUID != "" {
				t.Fatalf("incoherent identity projected credential material: %#v", selected)
			}
		})
	}
}

func TestAuthorizationIdentityChangesOperationalDigest(t *testing.T) {
	base := `{"_oboard":{"rate_limits":{"users":{"opaque":{"user_id":1,"authorization_key":"key","credential_status":"active"}}},"authorization":{"revision":1}},"inbounds":[]}`
	renew := `{"_oboard":{"rate_limits":{"users":{"opaque":{"user_id":1,"authorization_key":"key","credential_status":"active","lease_bytes":123}}},"authorization":{"revision":2}},"inbounds":[]}`
	status := `{"_oboard":{"rate_limits":{"users":{"opaque":{"user_id":1,"authorization_key":"key","credential_status":"reject_new"}}}},"inbounds":[]}`
	comparison, err := CompareConfigSemantics(base, renew)
	if err != nil || !comparison.DataPlaneEqual {
		t.Fatal("renewal changed operational identity")
	}
	comparison, err = CompareConfigSemantics(base, status)
	if err != nil || comparison.DataPlaneEqual {
		t.Fatal("credential status omitted from operational identity")
	}
}

func TestSSHAuthorizationDoesNotDuplicateKernelAccounting(t *testing.T) {
	user := model.User{ID: 1, Username: "account", Status: "active", LegacyProxyEnabled: true, ProxyCredentials: []model.ProxyCredential{{ID: "key", UserID: 1, InboundID: 2, PathID: 2, Protocol: model.ProtocolSSH, Status: "active", Username: "opaque", Password: "password", UUID: "uuid"}}}
	raw, err := GenerateServerConfigWithOptions(model.Server{ID: 1, PublicIPv4: "203.0.113.1"}, []model.Inbound{{ID: 2, ServerID: 1, Protocol: model.ProtocolSSH, Port: 2222, Enabled: true}}, nil, testDNSState(1), []model.User{user}, ConfigOptions{InboundUsers: []model.InboundUser{{InboundID: 2, UserID: 1, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	var config SingBoxConfig
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	if config.OBoard != nil && (len(config.OBoard.RateLimits.Users) > 0 || len(config.OBoard.RateLimits.Inbounds) > 0) {
		t.Fatal("SSH payload would be charged twice")
	}
}
