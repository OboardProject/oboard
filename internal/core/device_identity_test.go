package core

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestExpandDeviceUsersIgnoresDeviceRows(t *testing.T) {
	users := []model.User{
		{ID: 1, Username: "active", Status: "active"},
		{ID: 2, Username: "disabled", Status: "disabled"},
	}
	devices := []model.UserDevice{{
		ID: "dev_phone", UserID: 1, Status: "active", DeviceIDHash: "hash", CredentialEpoch: 2, ProxyAccessState: "active",
	}}
	got := ExpandDeviceUsers(users, devices)
	if len(got) != 1 || got[0].ID != 1 || got[0].DeviceIDHash != "" || got[0].Username != "active" {
		t.Fatalf("expanded users = %#v", got)
	}
	passthrough := ExpandDeviceUsers(users, nil)
	if len(passthrough) != 2 {
		t.Fatalf("nil devices should keep the input list: %#v", passthrough)
	}
}
