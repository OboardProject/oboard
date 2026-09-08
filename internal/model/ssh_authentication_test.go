package model

import "testing"

func TestSSHAuthenticationPlanDigestTracksIdentityWithoutPasswordHash(t *testing.T) {
	plan := SSHInboundPlan{Inbounds: []SSHInbound{{InboundID: 1, ServerID: 2, ListenIP: "127.0.0.1", Port: 2222, Enabled: true, Users: []SSHInboundUser{{UserID: 3, Username: "opaque-login", Password: "test-password", AuthorizationKey: "opaque-key", PathID: 4, RouteKind: "kernel", RouteInboundTag: "in-1", RouteAuthUser: "opaque-route", Enabled: true}}}}}
	original := SSHAuthenticationPlanDigest(plan)
	plan.Version = 7
	plan.Inbounds[0].Users[0].Password = "rotated-password"
	if SSHAuthenticationPlanDigest(plan) != original {
		t.Fatal("verification digest exposes password or version")
	}
	plan.Inbounds[0].Users[0].RouteAuthUser = "different-route"
	if SSHAuthenticationPlanDigest(plan) == original {
		t.Fatal("verification digest failed to bind route identity")
	}
}
