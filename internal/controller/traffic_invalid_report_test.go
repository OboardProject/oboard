package controller

import (
	"context"
	"net/http"
	"testing"
)

// A structurally malformed report - one that names no inbound - used to fail
// the whole request with a 400. The Agent keeps a rejected batch in its local
// state, so the same batch failed on every retry forever, taking the healthy
// reports and the lease-renewing policy response down with it. A real fleet
// hit this through a Controller release that rendered single-user inbound
// runtime limits without inbound_id: the Agent relayed the kernel counters
// faithfully, and one inbound-less report wedged lease renewal on the server
// until every lease ran out. It is terminal for the one report instead.
func TestAgentTrafficInboundlessReportDoesNotPoisonTheBatch(t *testing.T) {
	db, server, inbound, user, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	healthy := ledgerTrafficBody(user.ID, inbound.ID, "tr-healthy", 0, 100, 0, 200)["reports"].([]map[string]any)[0]
	// Same shape the Agent produced for a single-user inbound whose runtime
	// limit carried no inbound_id: every field valid except inbound_id.
	poison := map[string]any{
		"report_id": "tr-poison", "source": "core", "stream_id": "ts_core", "counter_epoch": "ce_1",
		"user_id": user.ID,
		"from_upload_bytes": 0, "to_upload_bytes": 50, "from_download_bytes": 0, "to_download_bytes": 60,
	}
	response := postAgentTraffic(t, h, server.AgentID, "token-a", map[string]any{"reports": []map[string]any{healthy, poison}}, http.StatusOK)
	assertTrafficRejection(t, response, "tr-poison", "invalid_report")

	accepted, _ := response["accepted_report_ids"].([]any)
	found := false
	for _, id := range accepted {
		if id == "tr-healthy" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the healthy report in the batch was not accounted: %#v", response)
	}
	if response["policies"] == nil {
		t.Fatalf("the policy response that renews traffic leases was not returned: %#v", response)
	}
	stored, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUsedBytes != 300 {
		t.Fatalf("bound user billed %d bytes, want 300", stored.TrafficUsedBytes)
	}
}

// The other structural malformations follow the same rule: terminal for the
// one report, never a request-fatal 400.
func TestAgentTrafficStructuralMalformationsArePerReport(t *testing.T) {
	db, server, inbound, user, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	order := []string{"negative path id", "missing stream identity", "invalid source"}
	for index, name := range order {
		report := map[string]map[string]any{
			"negative path id": {
				"report_id": "tr-neg-path", "source": "core", "stream_id": "ts_core", "counter_epoch": "ce_1",
				"user_id": user.ID, "inbound_id": inbound.ID, "path_id": -1,
				"from_upload_bytes": 0, "to_upload_bytes": 10, "from_download_bytes": 0, "to_download_bytes": 10,
			},
			"missing stream identity": {
				"report_id": "tr-no-stream", "source": "core", "stream_id": "", "counter_epoch": "ce_1",
				"user_id": user.ID, "inbound_id": inbound.ID,
				"from_upload_bytes": 0, "to_upload_bytes": 10, "from_download_bytes": 0, "to_download_bytes": 10,
			},
			"invalid source": {
				"report_id": "tr-bad-source", "source": "other", "stream_id": "ts_core", "counter_epoch": "ce_1",
				"user_id": user.ID, "inbound_id": inbound.ID,
				"from_upload_bytes": 0, "to_upload_bytes": 10, "from_download_bytes": 0, "to_download_bytes": 10,
			},
		}[name]
		t.Run(name, func(t *testing.T) {
			from := int64(index * 10)
			healthy := ledgerTrafficBody(user.ID, inbound.ID, "tr-healthy-"+name, from, from+10, from, from+10)["reports"].([]map[string]any)[0]
			response := postAgentTraffic(t, h, server.AgentID, "token-a", map[string]any{"reports": []map[string]any{healthy, report}}, http.StatusOK)
			assertTrafficRejection(t, response, report["report_id"].(string), "invalid_report")
		})
	}
	// Only the malformed reports were refused; the healthy ones were each
	// accounted. They share one stream identity and advance its checkpoint
	// range, so the total is the sum of their bytes.
	stored, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUsedBytes != 60 {
		t.Fatalf("used = %d, want the three healthy reports billed once each", stored.TrafficUsedBytes)
	}
}
