package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

func TestSnellProfileAutomationLifecycle(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "root", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	adminPrincipal := userAutomationPrincipal(t, db, admin.ID)
	createInput := json.RawMessage(`{"snell_profile":{"name":"机房A v4","version":4,"psk":"secret-psk-1234","obfs_mode":"http","obfs_host":"bing.com","mode":"default"}}`)
	applyAutomationChangeset(t, server, adminPrincipal, "snell-profile-create", automation.OperationRequest{Capability: "snell_profiles.create", Input: createInput})
	profiles, err := db.ListSnellProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var profile *model.SnellProfile
	for index := range profiles {
		if profiles[index].Name == "机房A v4" {
			profile = &profiles[index]
			break
		}
	}
	if profile == nil || profile.Version != 4 || profile.PSK != "secret-psk-1234" || profile.ObfsMode != "http" || profile.ObfsHost != "bing.com" {
		t.Fatalf("created profile missing: %#v", profiles)
	}
	updateInput, _ := json.Marshal(map[string]any{"snell_profile_id": profile.ID, "changes": map[string]any{"psk": "new-psk-5678", "obfs_mode": "tls"}})
	applyAutomationChangeset(t, server, adminPrincipal, "snell-profile-update", automation.OperationRequest{Capability: "snell_profiles.update", Input: updateInput})
	after, err := db.GetSnellProfile(ctx, profile.ID)
	if err != nil || after.PSK != "new-psk-5678" || after.ObfsMode != "tls" {
		t.Fatalf("profile after update: %#v err=%v", after, err)
	}
	// Deleting an unreferenced custom profile succeeds.
	deleteInput, _ := json.Marshal(map[string]any{"snell_profile_id": profile.ID, "confirm": true})
	applyAutomationChangeset(t, server, adminPrincipal, "snell-profile-delete", automation.OperationRequest{Capability: "snell_profiles.delete", Input: deleteInput})
	if _, err := db.GetSnellProfile(ctx, profile.ID); err == nil {
		t.Fatal("profile not deleted")
	}
}