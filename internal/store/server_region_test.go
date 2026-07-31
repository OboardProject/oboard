package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestServerRegionRoundTripAndAutomaticDetection(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	var sshPortColumns int
	if err := s.db.QueryRowContext(ctx, `select count(*) from pragma_table_info('servers') where name='ssh_port'`).Scan(&sshPortColumns); err != nil {
		t.Fatal(err)
	}
	if sshPortColumns != 0 {
		t.Fatalf("fresh servers schema still contains ssh_port")
	}
	if _, err := s.db.ExecContext(ctx, `alter table servers add column ssh_port integer not null default 2222`); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{
		Name:               "region-server",
		AgentID:            "region-agent",
		RegionMode:         "manual",
		RegionCode:         "TW",
		DetectedRegionCode: "HK",
		ListenIP:           "0.0.0.0",
		IPStack:            model.IPStackAuto,
		UDPInboundMode:     model.UDPInboundAllow,
		PortRangeStart:     10000,
		PortRangeEnd:       20000,
		Status:             model.ServerUnknown,
	}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RegionMode != "manual" || stored.RegionCode != "TW" || stored.DetectedRegionCode != "HK" {
		t.Fatalf("unexpected stored region: %#v", stored)
	}
	windowStart := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	window := model.ServerTrafficWindow{Key: "2026-07", Start: windowStart, End: windowStart.AddDate(0, 1, 0)}
	if _, _, err := s.UpsertHealthTransition(ctx, model.HealthReport{AgentID: server.AgentID, Status: model.ServerOnline, RegionCode: "SG"}, window); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RegionCode != "TW" || stored.DetectedRegionCode != "SG" {
		t.Fatalf("automatic detection replaced manual override: %#v", stored)
	}
	stored.RegionMode = "auto"
	stored.RegionCode = ""
	if err := s.UpdateServer(ctx, stored); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RegionMode != "auto" || stored.RegionCode != "" || stored.DetectedRegionCode != "SG" {
		t.Fatalf("unexpected automatic region state: %#v", stored)
	}
	var legacySSHPort int
	if err := s.db.QueryRowContext(ctx, `select ssh_port from servers where id=?`, server.ID).Scan(&legacySSHPort); err != nil {
		t.Fatal(err)
	}
	if legacySSHPort != 2222 {
		t.Fatalf("runtime unexpectedly rewrote legacy ssh_port to %d", legacySSHPort)
	}
}
