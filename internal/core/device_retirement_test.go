package core

import (
	"github.com/OboardProject/oboard/internal/model"
	"testing"
	"time"
)

func TestDeviceRetirementProjectsOnlyExistingEligibleMaterial(t *testing.T) {
	user := model.User{ID: 1, Username: "account", Status: "active", DeviceTransitionUntil: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), ProxyCredentials: []model.ProxyCredential{
		{ID: "legacy", UserID: 1, DeviceIDHash: "old", CredentialEpoch: 1, Status: "active", DeviceTransitionAllowed: true},
		{ID: "restricted", UserID: 1, DeviceIDHash: "restricted", CredentialEpoch: 1, Status: "active"},
		{ID: "revoked", UserID: 1, DeviceIDHash: "revoked", CredentialEpoch: 1, Status: "revoked", DeviceTransitionAllowed: true},
	}}
	got := DataPlaneIdentities([]model.User{user})
	if len(got) != 2 || got[1].DeviceIDHash != "old" {
		t.Fatalf("identities: %+v", got)
	}
	user.DeviceTransitionUntil = time.Now().Add(-time.Second)
	if got := DataPlaneIdentities([]model.User{user}); len(got) != 1 {
		t.Fatal("expired transition projected device")
	}
	user.DeviceTransitionUntil = time.Time{}
	if got := DataPlaneIdentities([]model.User{user}); len(got) != 1 {
		t.Fatal("no transition projected device")
	}
	user.DeviceTransitionUntil = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	user.Status = "disabled"
	if got := DataPlaneIdentities([]model.User{user}); len(got) != 0 {
		t.Fatal("disabled account projected")
	}
}

func TestDeviceRetirementDoesNotExpandExistingScopes(t *testing.T) {
	user := model.User{ID: 1, Username: "account", Status: "active", DeviceTransitionUntil: time.Now().Add(time.Hour), ProxyCredentials: []model.ProxyCredential{
		{ID: "legacy", UserID: 1, InboundID: 1, Protocol: model.ProtocolSocks, DeviceIDHash: "old", CredentialEpoch: 1, Status: "active", DeviceTransitionAllowed: true, Username: "old", Password: "secret", UUID: "uuid"},
		{ID: "unreviewed", UserID: 1, InboundID: 2, Protocol: model.ProtocolSocks, DeviceIDHash: "old", CredentialEpoch: 1, Status: "active", Username: "other", Password: "secret", UUID: "uuid"},
	}}
	legacy := DataPlaneIdentities([]model.User{user})[1]
	if UserCredentialForRoute(legacy, 2, 0, model.ProtocolSocks).AuthorizationKey != "" {
		t.Fatal("unreviewed scope projected")
	}
	inbounds := []model.Inbound{{ID: 1, Protocol: model.ProtocolSocks, Enabled: true}, {ID: 2, Protocol: model.ProtocolSocks, Enabled: true}, {ID: 3, Protocol: model.ProtocolSocks, Enabled: true}}
	scopes := ProxyCredentialScopes([]model.User{user}, inbounds, ConfigOptions{InboundUsers: []model.InboundUser{{InboundID: 1, UserID: 1, Enabled: true}, {InboundID: 2, UserID: 1, Enabled: true}, {InboundID: 3, UserID: 1, Enabled: true}}})
	devices := 0
	for _, scope := range scopes {
		if scope.DeviceIDHash != "" {
			devices++
			if scope.InboundID != 1 {
				t.Fatal("expanded legacy scope")
			}
		}
	}
	if devices != 1 {
		t.Fatalf("legacy scopes=%d", devices)
	}
	legacy.DeviceTransitionUntil = time.Now().Add(-time.Second)
	if UserCredentialForRoute(legacy, 1, 0, model.ProtocolSocks).AuthorizationKey != "" {
		t.Fatal("cached identity survived deadline")
	}
}
