package core

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

// A single-user inbound's runtime limit rides in the per-inbound map keyed by
// its listener tag. That entry must carry the inbound id: the traffic ledger on
// the Controller rejects a report without inbound_id with a request-fatal 400,
// which stalls lease renewal for every user on the server. The per-user map
// assigns into its range copy, so the per-inbound map used to inherit a zero
// inbound id from the unmodified source map.
func TestSingleUserInboundRuntimeLimitCarriesInboundID(t *testing.T) {
	server := snellTestServer()
	inbound := snellTestInbound()
	users := snellTestUsers(1)
	config, err := GenerateServerConfigWithOptions(server, []model.Inbound{inbound}, nil, testDNSState(1), users, ConfigOptions{
		Servers: []model.Server{server}, Inbounds: []model.Inbound{inbound},
		PortLedger: NewProxyPathPortLedger(nil),
		TrafficPolicies: map[int64]model.TrafficRuntimePolicy{
			users[0].ID: {UserID: users[0].ID, Billable: true, TrafficLimitBytes: 1000, LeaseEnforced: true, LeaseBytes: 100, PeriodKey: "2026-09-01"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed := parseSingBoxConfig(t, config)
	if parsed.OBoard == nil {
		t.Fatalf("missing runtime metadata: %s", config)
	}
	found := false
	for tag, limit := range parsed.OBoard.RateLimits.Inbounds {
		found = true
		if limit.InboundID != inbound.ID {
			t.Fatalf("inbounds[%s].inbound_id = %d, want %d", tag, limit.InboundID, inbound.ID)
		}
	}
	if !found {
		t.Fatalf("no per-inbound runtime limit rendered: %s", config)
	}
	for username, limit := range parsed.OBoard.RateLimits.Users {
		if limit.InboundID != inbound.ID {
			t.Fatalf("users[%s].inbound_id = %d, want %d", username, limit.InboundID, inbound.ID)
		}
	}
}
