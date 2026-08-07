package core

import (
	"reflect"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestCompareServerAuthUserSets(t *testing.T) {
	servers := []model.Server{{ID: 1, Name: "s1"}, {ID: 2, Name: "s2"}}
	inbounds := []model.Inbound{{ID: 10, ServerID: 1, Enabled: true}, {ID: 11, ServerID: 1, Enabled: true}}
	paths := []model.ProxyPath{{ID: 100, InboundID: 10, Kind: model.ProxyPathKindDirect, Enabled: true}}
	usernames := map[int64]string{1: "alice", 2: "bob"}
	legacyInbound := []model.InboundUser{{InboundID: 10, UserID: 1, Enabled: true}}
	planInbound := []model.InboundUser{{InboundID: 10, UserID: 2, Enabled: true}}
	divergences := CompareServerAuthUserSets(servers, paths, nil, inbounds, usernames, legacyInbound, nil, planInbound, nil, 10)
	if len(divergences) != 1 {
		t.Fatalf("divergences = %#v, want 1", divergences)
	}
	d := divergences[0]
	if d.ServerID != 1 || !reflect.DeepEqual(d.LegacyUsers, []string{"alice"}) || !reflect.DeepEqual(d.PlanUsers, []string{"bob"}) {
		t.Fatalf("divergence = %#v", d)
	}
}

func TestCompareSSHUserSets(t *testing.T) {
	inbounds := []model.Inbound{{ID: 20, Protocol: model.ProtocolSSH, Enabled: true}}
	paths := []model.ProxyPath{{ID: 200, InboundID: 20, Kind: model.ProxyPathKindDirect, Enabled: true, Name: "ssh-direct"}, {ID: 201, InboundID: 999, Kind: model.ProxyPathKindDirect, Enabled: true, Name: "other"}}
	usernames := map[int64]string{1: "alice"}
	legacy := []model.ProxyPathUser{{ProxyPathID: 200, UserID: 1, Enabled: true}}
	divergences := CompareSSHUserSets(paths, inbounds, legacy, nil, usernames, 10)
	if len(divergences) != 1 || divergences[0].PathID != 200 {
		t.Fatalf("ssh divergences = %#v", divergences)
	}
	if !reflect.DeepEqual(divergences[0].LegacyUsers, []string{"alice"}) || len(divergences[0].PlanUsers) != 0 {
		t.Fatalf("ssh divergence = %#v", divergences[0])
	}
}

func TestCompareUserPolicies(t *testing.T) {
	users := []model.User{{ID: 1, Username: "alice", Status: "active", SpeedLimitMbps: 10, TrafficLimitBytes: 1000}}
	planPolicies := map[int64]UserLimitPolicy{1: {SpeedLimitMbps: 20, TrafficLimitBytes: 1000, TrafficResetMode: "monthly", TrafficResetDay: 1}}
	divergences := CompareUserPolicies(users, nil, nil, planPolicies, 10)
	if len(divergences) != 1 {
		t.Fatalf("policy divergences = %#v", divergences)
	}
	d := divergences[0]
	if d.UserID != 1 || d.LegacySpeedMbps != 10 || d.PlanSpeedMbps != 20 {
		t.Fatalf("policy divergence = %#v", d)
	}
}
