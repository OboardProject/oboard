package controller

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"net/netip"
	"testing"
)

func TestSnellModeSwitchUsesValidatedChangesetAndConfirmsLater(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := model.User{Username: "snell-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active"}
	if err := db.CreateUser(ctx, &admin); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	server := model.Server{Name: "snell-node", AgentID: "snell-agent", PublicIPv4: "203.0.113.8", Status: model.ServerOnline, PortRangeStart: 40000, PortRangeEnd: 40100, KernelCapabilities: []string{model.AgentCapabilityRuntimeUsers, model.KernelCapabilityRuntimeUsers, "authorization_lease_v1", "runtime_users_snell_psk_v1", "runtime_users_snell_psk_control_v1", "snell_multi_psk_v4_v1"}}
	if err := db.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{ServerID: server.ID, Name: "snell", Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 6160, Enabled: true, ConfigJSON: `{"version":4,"psk":"old-server-seed"}`}
	if err := db.CreateInbound(ctx, &in); err != nil {
		t.Fatal(err)
	}
	req := snellModeRequest{InboundID: in.ID, ListenerMode: core.SnellListenerShared}
	preview, next, err := s.previewSnellMode(ctx, principal, req)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CapabilityReady || !preview.RequiresRestart || preview.PreviewDigest == "" || next == nil {
		t.Fatalf("incomplete switch preview: %+v", preview)
	}
	if _, err = s.applySnellMode(ctx, principal, req); !errors.Is(err, core.ErrSnellModeChange) {
		t.Fatalf("missing preview accepted: %v", err)
	}
	foreign := principal
	foreign.ResourceFilter = json.RawMessage(`{"server_ids":[999999]}`)
	if _, _, err = s.previewSnellMode(ctx, foreign, req); !errors.Is(err, errSnellModeForbidden) {
		t.Fatal("cross-server mode preview accepted")
	}
	viewer := principal
	viewer.Role = model.RoleViewer
	if _, _, err = s.previewSnellMode(ctx, viewer, req); !errors.Is(err, errSnellModeForbidden) {
		t.Fatal("viewer mode preview accepted")
	}
	if err = validateSnellModeMutation(in, *next); !errors.Is(err, core.ErrSnellModeChange) {
		t.Fatal("raw inbound update bypassed mode operation")
	}
	req.PreviewDigest = preview.PreviewDigest
	input, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := s.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "inbounds.listener_mode.apply", Input: input}}})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := json.Marshal(draft.ExpectedRevisions)
	change, err := s.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "snell-mode", BaseRevisions: base, Operations: []automation.OperationRequest{{Capability: "inbounds.listener_mode.apply", Input: input}}})
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
	if !core.SnellSharedPort(*saved) || saved.SnellActiveMode != "" {
		t.Fatal("save falsely confirmed running endpoint")
	}
	if _, err = s.applySnellMode(ctx, principal, req); !errors.Is(err, core.ErrSnellModeChange) {
		t.Fatalf("stale preview replay accepted: %v", err)
	}
}
