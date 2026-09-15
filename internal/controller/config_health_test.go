package controller

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/core/confighealth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func newConfigHealthServer(t *testing.T) (*Server, *store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config-health.sqlite")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return newTestServer(db, "config-health-secret", t.TempDir()), db, path
}

// writeLegacyInboundDocument installs a protocol document straight into the
// row, bypassing the validating store writes. That is how such a document
// really arrives - an older Controller stored it before the rule existed, or a
// backup restored it - and it is the state the normal form can no longer fix.
func writeLegacyInboundDocument(t *testing.T, path string, inboundID int64, configJSON string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`update inbounds set config_json=? where id=?`, configJSON, inboundID); err != nil {
		t.Fatal(err)
	}
}

// bumpRoutingCacheRevision moves the routing revision without touching any row
// the evaluator reads, which is what an ordinary runtime write does.
func bumpRoutingCacheRevision(t *testing.T, path string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`update routing_cache_revision set revision=revision+1 where id=1`); err != nil {
		t.Fatal(err)
	}
}

// seedBrokenInbound stores an inbound whose document the current model rejects.
// It goes in through the repair-free path on purpose: the whole point of the
// feature is a row the normal validating write would never have let in, which
// is how such rows arrive from older versions and restored backups.
func seedBrokenInbound(t *testing.T, db *store.Store, path string) (model.Server, model.Inbound) {
	t.Helper()
	ctx := context.Background()
	server := model.Server{Name: "hk-1", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{
		ServerID: server.ID, Name: "vless-a", Protocol: model.ProtocolVLESS,
		ListenIP: "0.0.0.0", Port: 10001, ConfigJSON: `{}`, Enabled: true,
	}
	if err := db.CreateInbound(ctx, &inbound); err != nil {
		t.Fatal(err)
	}
	// A client-side multiplex option on a listener: the kernel ignores it and
	// the current validator rejects it.
	writeLegacyInboundDocument(t, path, inbound.ID, `{"multiplex":{"enabled":true,"protocol":"smux"}}`)
	inbound.ConfigJSON = `{"multiplex":{"enabled":true,"protocol":"smux"}}`
	return server, inbound
}

func decodeCleanupResponse(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode cleanup response: %v (%s)", err, body)
	}
	return out
}

func cleanupRequest(t *testing.T, server *Server, payload configHealthCleanupRequest) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config-health/cleanup", bytes.NewReader(encoded))
	rec := httptest.NewRecorder()
	server.configHealthCleanupHandler(rec, req)
	return rec, decodeCleanupResponse(t, rec.Body.Bytes())
}

func TestConfigHealthReportFindsStoredInvalidInbound(t *testing.T) {
	server, db, path := newConfigHealthServer(t)
	_, inbound := seedBrokenInbound(t, db, path)

	entry, err := server.configHealthReport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if entry.report.Summary.Blocking != 1 {
		t.Fatalf("expected one blocking finding, got %+v", entry.report.Summary)
	}
	finding := entry.report.Findings[0]
	if finding.Code != "inbound.config.invalid" || finding.ResourceID != inbound.ID {
		t.Fatalf("unexpected finding %+v", finding)
	}
	if finding.Remedy.Kind != confighealth.RemedyNormalize {
		t.Fatalf("expected a normalize remedy, got %+v", finding.Remedy)
	}
}

// The cache is the resource-usage contract: repeated polling against an
// unchanged topology must not re-evaluate. A rebuild is observable through the
// entry identity, which only changes when a new snapshot is stored.
func TestConfigHealthReportIsBuiltOncePerRevision(t *testing.T) {
	server, db, path := newConfigHealthServer(t)
	seedBrokenInbound(t, db, path)
	ctx := context.Background()

	first, err := server.configHealthReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := server.configHealthReport(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatal("a poll against an unchanged revision rebuilt the report")
		}
	}

	// A topology write bumps the routing revision, which must invalidate it.
	extra := model.Inbound{
		ServerID: 1, Name: "vless-b", Protocol: model.ProtocolVLESS,
		ListenIP: "0.0.0.0", Port: 10002, ConfigJSON: `{}`, Enabled: true,
	}
	if err := db.CreateInbound(ctx, &extra); err != nil {
		t.Fatal(err)
	}
	after, err := server.configHealthReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after == first {
		t.Fatal("a topology change did not invalidate the cached report")
	}
}

func TestConfigHealthCleanupDryRunDoesNotWrite(t *testing.T) {
	server, db, path := newConfigHealthServer(t)
	_, inbound := seedBrokenInbound(t, db, path)

	rec, body := cleanupRequest(t, server, configHealthCleanupRequest{
		Confirm: false,
		Actions: []configHealthCleanupAction{{Code: "inbound.config.invalid", Scope: confighealth.ScopeInbound, ResourceID: inbound.ID}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if dryRun, _ := body["dry_run"].(bool); !dryRun {
		t.Fatal("expected a dry run")
	}
	if requires, _ := body["requires_deployment"].(bool); requires {
		t.Fatal("a dry run must not report pending deployment")
	}
	stored, err := db.GetInbound(context.Background(), inbound.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConfigJSON != inbound.ConfigJSON {
		t.Fatalf("dry run mutated the stored document: %s", stored.ConfigJSON)
	}
}

func TestConfigHealthCleanupNormalizesInvalidInbound(t *testing.T) {
	server, db, path := newConfigHealthServer(t)
	_, inbound := seedBrokenInbound(t, db, path)
	ctx := context.Background()

	rec, body := cleanupRequest(t, server, configHealthCleanupRequest{
		Confirm: true,
		Actions: []configHealthCleanupAction{{Code: "inbound.config.invalid", Scope: confighealth.ScopeInbound, ResourceID: inbound.ID}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if applied, _ := body["applied"].(float64); applied != 1 {
		t.Fatalf("expected one applied action: %s", rec.Body.String())
	}
	if requires, _ := body["requires_deployment"].(bool); !requires {
		t.Fatal("a repair that changed desired state must tell the operator to deploy")
	}

	stored, err := db.GetInbound(ctx, inbound.ID)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(stored.ConfigJSON), &document); err != nil {
		t.Fatal(err)
	}
	multiplex, ok := document["multiplex"].(map[string]any)
	if !ok {
		t.Fatalf("repair dropped the whole multiplex object: %s", stored.ConfigJSON)
	}
	if _, exists := multiplex["protocol"]; exists {
		t.Fatalf("the unsupported option survived: %s", stored.ConfigJSON)
	}
	if enabled, _ := multiplex["enabled"].(bool); !enabled {
		t.Fatalf("repair discarded the operator's choice: %s", stored.ConfigJSON)
	}

	// The finding is gone on the next evaluation.
	entry, err := server.freshConfigHealthReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !entry.report.Summary.Clean() {
		t.Fatalf("findings survived the repair: %+v", entry.report.Findings)
	}
}

func TestConfigHealthCleanupRejectsStaleReport(t *testing.T) {
	server, db, path := newConfigHealthServer(t)
	_, inbound := seedBrokenInbound(t, db, path)

	rec, body := cleanupRequest(t, server, configHealthCleanupRequest{
		Fingerprint: "0000000000000000000000000000000000000000000000000000000000000000",
		Confirm:     true,
		Actions:     []configHealthCleanupAction{{Code: "inbound.config.invalid", Scope: confighealth.ScopeInbound, ResourceID: inbound.ID}},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if body["code"] != "report_changed" {
		t.Fatalf("unexpected body %v", body)
	}
	stored, err := db.GetInbound(context.Background(), inbound.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConfigJSON != inbound.ConfigJSON {
		t.Fatal("a refused cleanup must not write anything")
	}
}

// The guard exists to catch a changed finding set, not a changed database. A
// live fleet writes device rows, probe results and access-change records
// constantly, and every one of them bumps the routing revision; binding the
// guard to that revision made cleanup permanently impossible.
func TestConfigHealthCleanupSurvivesUnrelatedRoutingRevisionBump(t *testing.T) {
	server, db, path := newConfigHealthServer(t)
	_, inbound := seedBrokenInbound(t, db, path)
	ctx := context.Background()

	entry, err := server.configHealthReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := entry.fingerprint

	before, err := db.RoutingCacheRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Stand in for the runtime writes a busy controller performs between the
	// report and the click: a device row, an egress probe result, an access
	// change. None of them changes a finding, all of them move this counter.
	bumpRoutingCacheRevision(t, path)
	after, err := db.RoutingCacheRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("the routing revision did not move")
	}

	rec, body := cleanupRequest(t, server, configHealthCleanupRequest{
		Fingerprint: fingerprint,
		Confirm:     true,
		Actions:     []configHealthCleanupAction{{Code: "inbound.config.invalid", Scope: confighealth.ScopeInbound, ResourceID: inbound.ID}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("an unrelated routing write refused the cleanup: %d %s", rec.Code, rec.Body.String())
	}
	if applied, _ := body["applied"].(float64); applied != 1 {
		t.Fatalf("expected one applied action: %s", rec.Body.String())
	}
}

// A selection the operator made against an older view must be dropped, not
// reinterpreted: the action names a finding, and a finding that no longer
// exists has no mutation to derive.
func TestConfigHealthCleanupSkipsFindingThatNoLongerExists(t *testing.T) {
	server, db, path := newConfigHealthServer(t)
	_, inbound := seedBrokenInbound(t, db, path)

	rec, body := cleanupRequest(t, server, configHealthCleanupRequest{
		Confirm: true,
		Actions: []configHealthCleanupAction{
			{Code: "inbound.config.invalid", Scope: confighealth.ScopeInbound, ResourceID: inbound.ID + 999},
			{Code: "routing_rule.match.invalid", Scope: confighealth.ScopeRoutingRule, ResourceID: 12345},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if skipped, _ := body["skipped"].(float64); skipped != 2 {
		t.Fatalf("expected both actions skipped: %s", rec.Body.String())
	}
	if applied, _ := body["applied"].(float64); applied != 0 {
		t.Fatalf("expected nothing applied: %s", rec.Body.String())
	}
}

// One failing action must not poison the batch that carries it, matching how
// the Agent report paths already answer per item.
func TestConfigHealthCleanupReportsPerActionOutcome(t *testing.T) {
	server, db, path := newConfigHealthServer(t)
	_, inbound := seedBrokenInbound(t, db, path)

	_, body := cleanupRequest(t, server, configHealthCleanupRequest{
		Confirm: true,
		Actions: []configHealthCleanupAction{
			{Code: "inbound.config.invalid", Scope: confighealth.ScopeInbound, ResourceID: inbound.ID},
			{Code: "inbound.config.invalid", Scope: confighealth.ScopeInbound, ResourceID: inbound.ID + 999},
		},
	})
	if applied, _ := body["applied"].(float64); applied != 1 {
		t.Fatalf("the valid action did not apply: %v", body)
	}
	if skipped, _ := body["skipped"].(float64); skipped != 1 {
		t.Fatalf("the stale action was not skipped: %v", body)
	}
}

func TestConfigHealthCleanupWritesAuditEntry(t *testing.T) {
	server, db, path := newConfigHealthServer(t)
	_, inbound := seedBrokenInbound(t, db, path)
	ctx := context.Background()

	cleanupRequest(t, server, configHealthCleanupRequest{
		Confirm: true,
		Actions: []configHealthCleanupAction{{Code: "inbound.config.invalid", Scope: confighealth.ScopeInbound, ResourceID: inbound.ID}},
	})
	logs, err := db.ListAuditPage(ctx, 20, 0, "config_health_normalize")
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected one audit entry, got %d", len(logs))
	}
	if logs[0].Target != "config_health" {
		t.Fatalf("unexpected audit entry %+v", logs[0])
	}
}

func TestRepairInboundConfigJSONRejectsAStillInvalidDocument(t *testing.T) {
	_, db, path := newConfigHealthServer(t)
	_, inbound := seedBrokenInbound(t, db, path)
	ctx := context.Background()

	err := db.RepairInboundConfigJSON(ctx, inbound.ID, `{"multiplex":{"enabled":true,"max_streams":4}}`)
	if err == nil {
		t.Fatal("a repair that leaves the document invalid must be refused")
	}
	stored, getErr := db.GetInbound(ctx, inbound.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored.ConfigJSON != inbound.ConfigJSON {
		t.Fatal("a refused repair must leave the row untouched")
	}
}
