package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func TestRetiredSnellModeRoutesReturnNotFound(t *testing.T) {
	s := &Server{}
	for _, suffix := range []string{"preview", "apply"} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/inbounds/1/listener-mode/"+suffix, nil)
		w := httptest.NewRecorder()
		s.inbounds(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("retired mode route returned %d", w.Code)
		}
	}
}

func TestSnellCreateAndUpdateUseSharedPortOnly(t *testing.T) {
	for _, kind := range []string{"", "snell-v4", "snell-v6"} {
		for _, editing := range []bool{false, true} {
			for _, mode := range []string{"", "shared_port", "per_identity_port"} {
				cfg := map[string]any{}
				if mode != "" {
					cfg["listener_mode"] = mode
				}
				raw, _ := json.Marshal(cfg)
				in := model.Inbound{Protocol: model.ProtocolSnell, Kind: kind, ConfigJSON: string(raw)}
				var current *model.Inbound
				if editing {
					value := in
					current = &value
				}
				rest := in
				err := applyInboundKindDefaults(&rest, current)
				machine, machineErr := normalizeInboundAutomationCandidate(in, current)
				if mode == "per_identity_port" {
					if err == nil || machineErr == nil {
						t.Fatalf("retired mode accepted: kind=%s editing=%v", kind, editing)
					}
					continue
				}
				if err != nil || machineErr != nil || core.SnellListenerMode(rest) != core.SnellListenerShared || core.SnellListenerMode(machine) != core.SnellListenerShared {
					t.Fatalf("shared mode rejected: kind=%s editing=%v errors=%v/%v", kind, editing, err, machineErr)
				}
			}
		}
	}
}

func TestSnellParametersSaveThroughValidatedInboundUpdate(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := model.User{Username: "snell-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active"}
	if err := db.CreateUser(ctx, &admin); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	server := model.Server{Name: "snell-node", AgentID: "snell-agent", PublicIPv4: "203.0.113.8", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{ServerID: server.ID, Name: "snell", Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6160, Enabled: true, ConfigJSON: `{"version":4,"psk":"old-server-seed","listener_mode":"shared_port"}`}
	if err := db.CreateInbound(ctx, &in); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DrainConfigurationSyncIntents(ctx); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"inbound_id": in.ID, "changes": map[string]any{"port": 7177}})
	operations := []automation.OperationRequest{{Capability: "inbounds.update", Input: input}}
	draft, err := s.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: operations})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := json.Marshal(draft.ExpectedRevisions)
	change, err := s.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "snell-save", BaseRevisions: base, Operations: operations})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.automation.Validate(ctx, principal, change.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.automation.Approve(ctx, principal, change.ID, "test approval"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.automation.Apply(ctx, principal, change.ID); err != nil {
		t.Fatal(err)
	}
	saved, err := db.GetInbound(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Port != 7177 || !core.SnellSharedPort(*saved) || saved.SnellActiveMode != "" {
		t.Fatalf("wrong saved or runtime state: %+v", saved)
	}
	intents, err := db.DrainConfigurationSyncIntents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	queued := false
	for _, intent := range intents {
		if intent.Source == "inbounds.update" && intent.ServerID == server.ID {
			queued = true
		}
	}
	if !queued {
		t.Fatal("parameter save did not persist automatic deployment intent")
	}
}
