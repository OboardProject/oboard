package controller

import (
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func contractValidSSHPlanUser() model.SSHInboundUser {
	return model.SSHInboundUser{
		AuthorizationKey: strings.Repeat("k", 32),
		UserID:           7,
		Username:         "u0123456789abcdef0123456789abcdef",
		Password:         "material",
		CredentialStatus: "active",
		PathID:           3,
		RouteKind:        "kernel",
		RouteInboundTag:  "in-4",
		RouteAuthUser:    "u0123456789abcdef0123456789abcdef",
		Enabled:          true,
	}
}

// TestSSHInboundUserContractMirrorsAgentValidation keeps the Controller-side
// pre-flight aligned with the Agent's validateSSHInboundPlan. The Agent rejects
// a whole plan on one bad user, so any divergence here means a server-wide
// deployment failure instead of one omitted account.
func TestSSHInboundUserContractMirrorsAgentValidation(t *testing.T) {
	if reason := sshInboundUserContractViolation(contractValidSSHPlanUser()); reason != "" {
		t.Fatalf("valid user rejected: %s", reason)
	}
	// A staged candidate credential on an account that owns no device is valid:
	// an access change puts every candidate through reject_new.
	staged := contractValidSSHPlanUser()
	staged.CredentialStatus = "reject_new"
	if reason := sshInboundUserContractViolation(staged); reason != "" {
		t.Fatalf("staged legacy credential rejected: %s", reason)
	}
	for name, test := range map[string]struct {
		mutate func(*model.SSHInboundUser)
		reason string
	}{
		"no user id":            {func(u *model.SSHInboundUser) { u.UserID = 0 }, "invalid_username"},
		"bad username charset":  {func(u *model.SSHInboundUser) { u.Username = "bad name"; u.RouteAuthUser = "bad name" }, "invalid_username"},
		"empty password":        {func(u *model.SSHInboundUser) { u.Password = " " }, "missing_password"},
		"epoch without device":  {func(u *model.SSHInboundUser) { u.CredentialEpoch = 3 }, "incoherent_device_identity"},
		"short device hash":     {func(u *model.SSHInboundUser) { u.DeviceIDHash = "short"; u.CredentialEpoch = 1 }, "incoherent_device_identity"},
		"device without epoch":  {func(u *model.SSHInboundUser) { u.DeviceIDHash = strings.Repeat("a", 16) }, "incoherent_device_identity"},
		"unknown status":        {func(u *model.SSHInboundUser) { u.CredentialStatus = "paused" }, "invalid_credential_status"},
		"missing path":          {func(u *model.SSHInboundUser) { u.PathID = 0 }, "missing_path_id"},
		"non kernel route":      {func(u *model.SSHInboundUser) { u.RouteKind = "direct" }, "invalid_route_kind"},
		"outbound tag set":      {func(u *model.SSHInboundUser) { u.OutboundTag = "out" }, "invalid_route_kind"},
		"bad route inbound tag": {func(u *model.SSHInboundUser) { u.RouteInboundTag = "in-0" }, "invalid_route_inbound_tag"},
		"auth user mismatch":    {func(u *model.SSHInboundUser) { u.RouteAuthUser = "u" + strings.Repeat("b", 32) }, "invalid_route_auth_user"},
		"short authorization":   {func(u *model.SSHInboundUser) { u.AuthorizationKey = "short" }, "invalid_route_auth_user"},
	} {
		t.Run(name, func(t *testing.T) {
			user := contractValidSSHPlanUser()
			test.mutate(&user)
			if reason := sshInboundUserContractViolation(user); reason != test.reason {
				t.Fatalf("reason = %q want %q", reason, test.reason)
			}
		})
	}
}

// TestSanitizeSSHInboundUsersContainsOneBadUser is the whole point of the
// pre-flight: one unusable account must cost only that account.
func TestSanitizeSSHInboundUsersContainsOneBadUser(t *testing.T) {
	good := contractValidSSHPlanUser()
	bad := contractValidSSHPlanUser()
	bad.UserID, bad.PathID = 9, 0
	disabled := contractValidSSHPlanUser()
	disabled.UserID, disabled.Enabled = 11, false
	disabled.Username, disabled.RouteAuthUser = "", ""
	duplicate := contractValidSSHPlanUser()
	duplicate.UserID = 13

	kept, rejections := sanitizeSSHInboundUsers(model.SSHInbound{InboundID: 4, Enabled: true, Users: []model.SSHInboundUser{good, bad, disabled, duplicate}})
	if len(kept) != 2 {
		t.Fatalf("kept = %#v", kept)
	}
	if kept[0].UserID != 7 || kept[1].UserID != 11 {
		t.Fatalf("wrong users kept: %#v", kept)
	}
	if len(rejections) != 2 {
		t.Fatalf("rejections = %#v", rejections)
	}
	if rejections[0].UserID != 9 || rejections[0].Reason != "missing_path_id" || rejections[0].InboundID != 4 {
		t.Fatalf("unexpected rejection: %#v", rejections[0])
	}
	if rejections[1].UserID != 13 || rejections[1].Reason != "duplicate_username" {
		t.Fatalf("unexpected rejection: %#v", rejections[1])
	}
	if got := describeSSHInboundRejections(rejections); got != "duplicate_username=1,missing_path_id=1" {
		t.Fatalf("summary = %q", got)
	}
	if describeSSHInboundRejections(nil) != "" {
		t.Fatal("empty rejection set produced a summary")
	}
}
