package core

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

// TestDataPlaneIdentitiesKeepsOnlyActiveAccounts pins the identity model after
// device-specific subscriptions were withdrawn: a device row is never an extra
// proxy identity, and eligibility is decided by account status alone. The old
// signature took a device slice whose only remaining effect was that a nil
// value skipped the status filter entirely, so a caller that simply left the
// field unset projected disabled accounts into the data plane.
func TestDataPlaneIdentitiesKeepsOnlyActiveAccounts(t *testing.T) {
	users := []model.User{
		{ID: 1, Username: "active", Status: "active"},
		{ID: 2, Username: "disabled", Status: "disabled"},
		{ID: 3, Username: "pending", Status: "pending"},
	}
	got := DataPlaneIdentities(users)
	if len(got) != 1 || got[0].ID != 1 || got[0].Username != "active" {
		t.Fatalf("identities = %#v", got)
	}
	if got[0].DeviceIDHash != "" || got[0].CredentialEpoch != 0 {
		t.Fatalf("account identity carried device material: %#v", got[0])
	}
	if len(DataPlaneIdentities(nil)) != 0 {
		t.Fatal("nil input produced identities")
	}
}
