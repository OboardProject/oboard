package core

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestEffectiveInboundUsersCombinesDirectUserAndGroupGrants(t *testing.T) {
	serverID := int64(1)
	inboundID := int64(10)
	inbounds := []model.Inbound{{ID: inboundID, ServerID: serverID, Name: "vless", Protocol: model.ProtocolVLESS, Enabled: true}}
	users := []model.User{
		{ID: 1, Username: "alice", Status: "active"},
		{ID: 2, Username: "bob", Status: "active"},
		{ID: 3, Username: "carol", Status: "disabled"},
	}
	groups := []model.UserGroup{{ID: 7, Name: "vip", Enabled: true}}
	members := []model.UserGroupMember{{GroupID: 7, UserID: 2, Enabled: true}, {GroupID: 7, UserID: 3, Enabled: true}}
	grants := []model.InboundAccessGrant{
		{SubjectType: model.AccessSubjectGroup, SubjectID: 7, ScopeType: model.AccessScopeServer, ServerID: &serverID, Enabled: true},
		{SubjectType: model.AccessSubjectUser, SubjectID: 1, ScopeType: model.AccessScopeGlobal, Enabled: true},
	}
	effective := EffectiveInboundUsers(inbounds, users, []model.InboundUser{{InboundID: inboundID, UserID: 1, Enabled: true}}, groups, members, grants)
	if len(effective) != 2 {
		t.Fatalf("effective bindings = %#v, want two active users", effective)
	}
	got := map[int64]bool{}
	for _, item := range effective {
		got[item.UserID] = true
	}
	if !got[1] || !got[2] || got[3] {
		t.Fatalf("effective user ids = %#v", got)
	}
}

func TestValidateInboundAccessCapacityRejectsSingleUserOverflowFromGroup(t *testing.T) {
	serverID := int64(1)
	err := ValidateInboundAccessCapacity(
		[]model.Inbound{{ID: 1, ServerID: serverID, Name: "single-password-ss", Protocol: model.ProtocolSS, ConfigJSON: `{"method":"aes-128-gcm"}`, Enabled: true}},
		[]model.User{{ID: 1, Status: "active"}, {ID: 2, Status: "active"}},
		nil,
		[]model.UserGroup{{ID: 1, Enabled: true}},
		[]model.UserGroupMember{{GroupID: 1, UserID: 1, Enabled: true}, {GroupID: 1, UserID: 2, Enabled: true}},
		[]model.InboundAccessGrant{{SubjectType: model.AccessSubjectGroup, SubjectID: 1, ScopeType: model.AccessScopeServer, ServerID: &serverID, Enabled: true}},
	)
	if err == nil {
		t.Fatal("expected single-user inbound capacity error")
	}
}

func TestEffectiveProxyPathUsersCombinesBroadAndExactAccess(t *testing.T) {
	serverID, inboundID := int64(1), int64(10)
	pathA, pathB := int64(100), int64(101)
	users := []model.User{{ID: 1, Status: "active"}, {ID: 2, Status: "active"}}
	paths := []model.ProxyPath{{ID: pathA, InboundID: inboundID, Enabled: true}, {ID: pathB, InboundID: inboundID, Enabled: true}}
	grants := []model.InboundAccessGrant{
		{SubjectType: model.AccessSubjectUser, SubjectID: 1, ScopeType: model.AccessScopeServer, ServerID: &serverID, Enabled: true},
		{SubjectType: model.AccessSubjectUser, SubjectID: 2, ScopeType: model.AccessScopeProxyPath, ProxyPathID: &pathB, Enabled: true},
	}
	effective := EffectiveProxyPathUsers(paths, []model.Inbound{{ID: inboundID, ServerID: serverID, Enabled: true}}, users, nil, nil, nil, grants)
	if !ProxyPathUserAllowed(pathA, 1, effective) || !ProxyPathUserAllowed(pathB, 1, effective) {
		t.Fatalf("broad user missing path access: %#v", effective)
	}
	if ProxyPathUserAllowed(pathA, 2, effective) || !ProxyPathUserAllowed(pathB, 2, effective) {
		t.Fatalf("exact path access leaked or missing: %#v", effective)
	}
}

func TestEffectiveUserLimitsInheritStrictestEnabledGroup(t *testing.T) {
	user := model.User{ID: 10, Username: "alice", SpeedLimitMbps: 0, TrafficLimitBytes: 0}
	groups := []model.UserGroup{
		{ID: 1, Name: "vip", Enabled: true, SpeedLimitMbps: 100, TrafficLimitBytes: 500},
		{ID: 2, Name: "strict", Enabled: true, SpeedLimitMbps: 50, TrafficLimitBytes: 300},
		{ID: 3, Name: "disabled", Enabled: false, SpeedLimitMbps: 10, TrafficLimitBytes: 10},
	}
	members := []model.UserGroupMember{
		{GroupID: 1, UserID: user.ID, Enabled: true},
		{GroupID: 2, UserID: user.ID, Enabled: true},
		{GroupID: 3, UserID: user.ID, Enabled: true},
	}
	policy := EffectiveUserLimitPolicy(user, groups, members)
	if policy.SpeedLimitMbps != 50 || policy.TrafficLimitBytes != 300 {
		t.Fatalf("effective limits = %d/%d, want strictest enabled group 50/300", policy.SpeedLimitMbps, policy.TrafficLimitBytes)
	}
}

func TestEffectiveUserLimitsUserOverrideWins(t *testing.T) {
	user := model.User{ID: 10, SpeedLimitMbps: 20, TrafficLimitBytes: 200}
	groups := []model.UserGroup{{ID: 1, Enabled: true, SpeedLimitMbps: 10, TrafficLimitBytes: 100}}
	members := []model.UserGroupMember{{GroupID: 1, UserID: user.ID, Enabled: true}}
	policy := EffectiveUserLimitPolicy(user, groups, members)
	if policy.SpeedLimitMbps != 20 || policy.TrafficLimitBytes != 200 {
		t.Fatalf("effective limits = %d/%d, want user override 20/200", policy.SpeedLimitMbps, policy.TrafficLimitBytes)
	}
}
