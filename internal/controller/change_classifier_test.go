package controller

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestClassifyUserChangeSelectsDeliveryLanes(t *testing.T) {
	before := model.User{ID: 1, Username: "alice", Status: "active", Role: model.RoleViewer, SpeedLimitMbps: 10, TrafficLimitBytes: 100}
	after := before
	after.SpeedLimitMbps = 50
	plan := ClassifyUserChange(before, after)
	if !plan.TrafficPolicy || plan.Authorization || plan.RuntimeUsers || plan.CoreConfig {
		t.Fatalf("traffic-only change: %+v", plan)
	}
	after = before
	after.ProxyPassword = "rotated"
	plan = ClassifyUserChange(before, after)
	if !plan.Authorization || !plan.RuntimeUsers || plan.CoreConfig || plan.TrafficPolicy {
		t.Fatalf("credential change: %+v", plan)
	}
	after = before
	after.Status = "disabled"
	plan = ClassifyUserChange(before, after)
	if !plan.Authorization || !plan.RuntimeUsers || plan.CoreConfig {
		t.Fatalf("disable: %+v", plan)
	}
	created := ClassifyUserCreated()
	if !created.Authorization || !created.RuntimeUsers || created.CoreConfig {
		t.Fatalf("created: %+v", created)
	}
	removed := ClassifyUserRemoval()
	if !removed.Authorization || !removed.RuntimeUsers || removed.CoreConfig {
		t.Fatalf("removed: %+v", removed)
	}
	rotated := ClassifyCredentialRotation()
	if !rotated.Authorization || !rotated.RuntimeUsers || rotated.CoreConfig {
		t.Fatalf("rotated: %+v", rotated)
	}
}

func TestInboundNeedsCoreConfigFallback(t *testing.T) {
	capable := model.Server{
		AgentID: "agent-1",
		KernelCapabilities: []string{
			model.AgentCapabilityRuntimeUsers,
			model.KernelCapabilityRuntimeUsers,
			model.AgentCapabilityRuntimeUsersVLESS,
			model.AgentCapabilityRuntimeUsersHysteria2,
			model.AgentCapabilityRuntimeUsersShadowsocks,
		},
	}
	if inboundNeedsCoreConfigFallback(capable, model.Inbound{Protocol: model.ProtocolVLESS, Enabled: true}) {
		t.Fatal("capable VLESS should stay on the runtime-users lane")
	}
	if inboundNeedsCoreConfigFallback(capable, model.Inbound{Protocol: model.ProtocolHY2, Enabled: true}) {
		t.Fatal("capable HY2 should stay on the runtime-users lane")
	}
	if !inboundNeedsCoreConfigFallback(capable, model.Inbound{Protocol: model.ProtocolSS, Enabled: true, ConfigJSON: `{}`}) {
		t.Fatal("single-user Shadowsocks should fall back to apply_core_config")
	}
	if inboundNeedsCoreConfigFallback(capable, model.Inbound{Protocol: model.ProtocolSS, Enabled: true, ConfigJSON: `{"users":[{"name":"a"},{"name":"b"}]}`}) {
		t.Fatal("multi-user Shadowsocks should stay on the runtime-users lane")
	}
	if !inboundNeedsCoreConfigFallback(capable, model.Inbound{Protocol: model.ProtocolSSH, Enabled: true}) {
		t.Fatal("SSH still needs the signed ssh_inbounds plan")
	}
	if !inboundNeedsCoreConfigFallback(capable, model.Inbound{Protocol: model.ProtocolSnell, Enabled: true}) {
		t.Fatal("Snell stays on controlled restart this round")
	}
	legacy := model.Server{AgentID: "agent-2"}
	if !inboundNeedsCoreConfigFallback(legacy, model.Inbound{Protocol: model.ProtocolVLESS, Enabled: true}) {
		t.Fatal("legacy Agent must fall back to apply_core_config")
	}
}
