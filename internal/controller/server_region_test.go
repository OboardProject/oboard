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

func TestEffectiveLatencyProbePublicTarget(t *testing.T) {
	tests := []struct {
		name   string
		server model.Server
		want   model.ConnectivityTarget
	}{
		{name: "detected mainland China", server: model.Server{RegionMode: "auto", DetectedRegionCode: "CN"}, want: model.ConnectivityProbeTarget12306},
		{name: "manual mainland China", server: model.Server{RegionMode: "manual", RegionCode: "CN", DetectedRegionCode: "US"}, want: model.ConnectivityProbeTarget12306},
		{name: "non-China automatic", server: model.Server{RegionMode: "auto", DetectedRegionCode: "JP"}, want: model.ConnectivityProbeTargetCloudflare},
		{name: "explicit Google", server: model.Server{RegionMode: "auto", DetectedRegionCode: "CN", LatencyProbePublicTarget: model.ConnectivityProbeTargetGoogle}, want: model.ConnectivityProbeTargetGoogle},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := effectiveLatencyProbePublicTarget(test.server); got != test.want {
				t.Fatalf("effective target = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateLatencyProbePublicTarget(t *testing.T) {
	server := validRegionTestServer()
	server.LatencyProbePublicTarget = "arbitrary"
	if err := validateServer(&server); err == nil {
		t.Fatal("arbitrary connectivity probe target was accepted")
	}
	server.LatencyProbePublicTarget = ""
	if err := validateServer(&server); err != nil {
		t.Fatal(err)
	}
	if server.LatencyProbePublicTarget != model.ConnectivityProbeTargetAuto {
		t.Fatalf("default latency probe public target = %q, want auto", server.LatencyProbePublicTarget)
	}
}

func TestValidateServerRejectsEmptyManualRegion(t *testing.T) {
	server := validRegionTestServer()
	server.RegionMode = "manual"
	if err := validateServer(&server); err == nil {
		t.Fatal("expected empty manual region to be rejected")
	}
}
