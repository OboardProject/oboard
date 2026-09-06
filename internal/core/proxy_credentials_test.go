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
	device := UserForDevice(user, model.UserDevice{DeviceIDHash: "device", CredentialEpoch: 1})
	if UserCredentialForRoute(device, 2, 3, model.ProtocolSocks).AuthorizationKey != "" {
		t.Fatal("device inherited account credential")
	}
	user.ProxyCredentials[0].Status = "revoked"
	if UserCredentialForRoute(user, 2, 3, model.ProtocolSocks).AuthorizationKey != "" {
		t.Fatal("revoked credential selected")
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
