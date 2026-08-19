package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestDisabledTopologyDraftDoesNotAdvanceRuntimeRevisionUntilActivation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	entry := &model.Server{Name: "draft-entry", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	target := &model.Server{Name: "draft-target", EntryAddress: "198.51.100.12", PublicIPv4: "198.51.100.12", ListenIP: "0.0.0.0", PortRangeStart: 11001, PortRangeEnd: 12000, Status: model.ServerOnline}
	for _, server := range []*model.Server{entry, target} {
		if err := s.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	root := &model.Inbound{ServerID: entry.ID, Name: "draft-root", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, ConfigJSON: "{}", Enabled: true}
	targetInbound := &model.Inbound{ServerID: target.ID, Name: "draft-target", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10444, ConfigJSON: "{}", Enabled: true}
	for _, inbound := range []*model.Inbound{root, targetInbound} {
		if err := s.CreateInbound(ctx, inbound); err != nil {
			t.Fatal(err)
		}
	}
	baseline, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{ID: 1, Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameAuto, InboundID: root.ID, Secret: "draft-secret", Enabled: false}
	step := model.ProxyPathStep{Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, InboundID: &targetInbound.ID, ServerID: &target.ID, ConfigJSON: "{}"}
	if err := s.CreateProxyPathComposition(ctx, path, []model.ProxyPathStep{step}); err != nil {
		t.Fatal(err)
	}
	draftRevision, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if draftRevision != baseline {
		t.Fatalf("disabled topology draft advanced runtime revision: %d -> %d", baseline, draftRevision)
	}
	if err := s.ActivateProxyPathComposition(ctx, path.ID, 0); err != nil {
		t.Fatal(err)
	}
	activeRevision, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if activeRevision <= draftRevision {
		t.Fatalf("topology activation did not advance runtime revision: %d -> %d", draftRevision, activeRevision)
	}
}
