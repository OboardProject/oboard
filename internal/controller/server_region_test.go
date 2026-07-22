package controller

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func validRegionTestServer() model.Server {
	return model.Server{
		Name:           "region-test",
		ListenIP:       "0.0.0.0",
		IPStack:        model.IPStackAuto,
		UDPInboundMode: model.UDPInboundAllow,
		PortRangeStart: 10000,
		PortRangeEnd:   20000,
	}
}

func TestValidateServerManualRegion(t *testing.T) {
	server := validRegionTestServer()
	server.RegionMode = "manual"
	server.RegionCode = "tw"
	if err := validateServer(&server); err != nil {
		t.Fatal(err)
	}
	if server.RegionMode != "manual" || server.RegionCode != "TW" {
		t.Fatalf("unexpected normalized region: mode=%q code=%q", server.RegionMode, server.RegionCode)
	}
}

func TestValidateServerAutoRegionClearsManualOverride(t *testing.T) {
	server := validRegionTestServer()
	server.RegionMode = "auto"
	server.RegionCode = "US"
	if err := validateServer(&server); err != nil {
		t.Fatal(err)
	}
	if server.RegionMode != "auto" || server.RegionCode != "" {
		t.Fatalf("unexpected auto region: mode=%q code=%q", server.RegionMode, server.RegionCode)
	}
}

func TestValidateServerRejectsEmptyManualRegion(t *testing.T) {
	server := validRegionTestServer()
	server.RegionMode = "manual"
	if err := validateServer(&server); err == nil {
		t.Fatal("expected empty manual region to be rejected")
	}
}
