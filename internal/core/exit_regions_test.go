package core

import (
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestRegionFlagEmojiUsesAntarcticaForUnresolvedRegion(t *testing.T) {
	for input, want := range map[string]string{
		"us":  "🇺🇸",
		"CN":  "🇨🇳",
		"":    "🇦🇶",
		"USA": "🇦🇶",
	} {
		if got := RegionFlagEmoji(input); got != want {
			t.Fatalf("RegionFlagEmoji(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveProxyPathExitRegionsUsesFinalControlledServerForDirectAndWARP(t *testing.T) {
	serverBID := int64(2)
	servers := []model.Server{
		{ID: 1, RegionMode: "auto", DetectedRegionCode: "HK"},
		{ID: 2, RegionMode: "manual", RegionCode: "us"},
	}
	inbounds := []model.Inbound{{ID: 10, ServerID: 1, Enabled: true}}
	paths := []model.ProxyPath{
		{ID: 100, Kind: model.ProxyPathKindDirect, InboundID: 10, Enabled: true},
		{ID: 200, Kind: model.ProxyPathKindChain, InboundID: 10, Enabled: true},
	}
	steps := []model.ProxyPathStep{
		{ID: 1, PathID: 100, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID},
		{ID: 2, PathID: 200, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverBID},
		{ID: 3, PathID: 200, Position: 2, NodeType: model.ProxyPathStepWARP},
	}

	resolved, _ := ResolveProxyPathExitRegions(paths, steps, servers, inbounds, nil, nil)
	for _, path := range resolved {
		if path.EffectiveExitRegionCode != "US" || path.ExitRegionSource != RegionSourceServerManual || path.ExitRegionStatus != RegionStatusResolved {
			t.Fatalf("path %d region = %#v", path.ID, path)
		}
	}
}

func TestResolveProxyPathExitRegionsAppliesManualAndProbePrecedence(t *testing.T) {
	externalID := int64(30)
	servers := []model.Server{{ID: 1, DetectedRegionCode: "HK"}}
	inbounds := []model.Inbound{{ID: 10, ServerID: 1, Enabled: true}}
	basePaths := []model.ProxyPath{
		{ID: 100, InboundID: 10, ExitRegionMode: "manual", ExitRegionCode: "jp", Enabled: true},
		{ID: 200, InboundID: 10, Enabled: true},
		{ID: 300, InboundID: 10, Enabled: true},
	}
	steps := []model.ProxyPathStep{
		{ID: 1, PathID: 100, Position: 1, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &externalID},
		{ID: 2, PathID: 200, Position: 1, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &externalID},
		{ID: 3, PathID: 300, Position: 1, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &externalID},
	}
	externals := []model.ExternalOutbound{{ID: externalID, Protocol: model.ProtocolSocks, Scope: model.ExternalOutboundScopeGlobal, TargetAddress: "8.8.8.8", TargetPort: 1080, RegionMode: "manual", RegionCode: "de", Enabled: true}}
	targets := ExternalEgressProbeTargets(basePaths, steps, servers, inbounds, externals)
	if len(targets) != 3 {
		t.Fatalf("probe targets = %d, want 3", len(targets))
	}
	results := make([]model.ProxyPathEgressResult, 0, len(targets))
	for _, target := range targets {
		results = append(results, model.ProxyPathEgressResult{PathID: target.PathID, TopologyFingerprint: target.TopologyFingerprint, Status: "succeeded", LastRegionCode: "US"})
	}

	resolved, _ := ResolveProxyPathExitRegions(basePaths, steps, servers, inbounds, externals, results)
	if got := resolved[0]; got.EffectiveExitRegionCode != "JP" || got.ExitRegionSource != RegionSourcePathManual {
		t.Fatalf("path manual precedence = %#v", got)
	}
	if got := resolved[1]; got.EffectiveExitRegionCode != "DE" || got.ExitRegionSource != RegionSourceExternalManual {
		t.Fatalf("external manual precedence = %#v", got)
	}

	externals[0].RegionMode = "auto"
	externals[0].RegionCode = ""
	resolved, _ = ResolveProxyPathExitRegions(basePaths, steps, servers, inbounds, externals, results)
	if got := resolved[2]; got.EffectiveExitRegionCode != "US" || got.DetectedExitRegionCode != "US" || got.ExitRegionSource != RegionSourceProbe {
		t.Fatalf("probe region = %#v", got)
	}

	for i := range results {
		if results[i].PathID == 300 {
			results[i].Status = RegionStatusFailed
			results[i].LastError = "temporary failure"
		}
	}
	resolved, _ = ResolveProxyPathExitRegions(basePaths, steps, servers, inbounds, externals, results)
	if got := resolved[2]; got.EffectiveExitRegionCode != "US" || got.ExitRegionStatus != RegionStatusFailed || got.ExitRegionError != "temporary failure" {
		t.Fatalf("same-topology failed probe should retain successful region: %#v", got)
	}
}

func TestResolveProxyPathExitRegionsRejectsStaleAndAntarcticaProbeRegions(t *testing.T) {
	externalID := int64(30)
	path := model.ProxyPath{ID: 100, InboundID: 10, Enabled: true}
	step := model.ProxyPathStep{ID: 1, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &externalID}
	servers := []model.Server{{ID: 1}}
	inbounds := []model.Inbound{{ID: 10, ServerID: 1, Enabled: true}}
	externals := []model.ExternalOutbound{{ID: externalID, Protocol: model.ProtocolSocks, Scope: model.ExternalOutboundScopeGlobal, TargetAddress: "8.8.8.8", TargetPort: 1080, Enabled: true}}
	target := ExternalEgressProbeTargets([]model.ProxyPath{path}, []model.ProxyPathStep{step}, servers, inbounds, externals)[0]

	resolved, _ := ResolveProxyPathExitRegions([]model.ProxyPath{path}, []model.ProxyPathStep{step}, servers, inbounds, externals, []model.ProxyPathEgressResult{{PathID: path.ID, TopologyFingerprint: "old", Status: "succeeded", LastRegionCode: "US"}})
	if got := resolved[0]; got.EffectiveExitRegionCode != "" || got.ExitRegionStatus != RegionStatusStale {
		t.Fatalf("stale result = %#v", got)
	}
	resolved, _ = ResolveProxyPathExitRegions([]model.ProxyPath{path}, []model.ProxyPathStep{step}, servers, inbounds, externals, []model.ProxyPathEgressResult{{PathID: path.ID, TopologyFingerprint: target.TopologyFingerprint, Status: "succeeded", LastRegionCode: "AQ"}})
	if got := resolved[0]; got.EffectiveExitRegionCode != "" || got.DetectedExitRegionCode != "" || got.ExitRegionStatus != RegionStatusFailed {
		t.Fatalf("automatic AQ result = %#v", got)
	}
}

func TestResolveProxyPathExitRegionsReportsImportedNodeConflicts(t *testing.T) {
	externalID := int64(30)
	paths := []model.ProxyPath{{ID: 100, InboundID: 10, Enabled: true}, {ID: 200, InboundID: 10, Enabled: true}}
	steps := []model.ProxyPathStep{
		{ID: 1, PathID: 100, Position: 1, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &externalID},
		{ID: 2, PathID: 200, Position: 1, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &externalID},
	}
	servers := []model.Server{{ID: 1}}
	inbounds := []model.Inbound{{ID: 10, ServerID: 1, Enabled: true}}
	externals := []model.ExternalOutbound{{ID: externalID, Protocol: model.ProtocolSocks, Scope: model.ExternalOutboundScopeGlobal, TargetAddress: "8.8.8.8", TargetPort: 1080, Enabled: true}}
	targets := ExternalEgressProbeTargets(paths, steps, servers, inbounds, externals)
	results := []model.ProxyPathEgressResult{
		{PathID: targets[0].PathID, TopologyFingerprint: targets[0].TopologyFingerprint, Status: "succeeded", LastRegionCode: "US"},
		{PathID: targets[1].PathID, TopologyFingerprint: targets[1].TopologyFingerprint, Status: "succeeded", LastRegionCode: "JP"},
	}
	_, resolvedExternals := ResolveProxyPathExitRegions(paths, steps, servers, inbounds, externals, results)
	if got := resolvedExternals[0]; got.EffectiveRegionCode != "" || got.RegionStatus != RegionStatusConflict {
		t.Fatalf("conflicting imported node regions = %#v", got)
	}
}

func TestExternalEgressTopologyFingerprintIgnoresPresentationAndRegionFields(t *testing.T) {
	externalID := int64(30)
	serverID := int64(2)
	now := time.Now().UTC()
	path := model.ProxyPath{ID: 100, Name: "old", InboundID: 10, ExitRegionMode: "manual", ExitRegionCode: "US", Enabled: true, UpdatedAt: now}
	steps := []model.ProxyPathStep{
		{ID: 1, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverID, ConfigJSON: `{ "chain_method": "2022-blake3-aes-128-gcm" }`, UpdatedAt: now},
		{ID: 2, PathID: path.ID, Position: 2, NodeType: model.ProxyPathStepImported, ExternalOutboundID: &externalID, ConfigJSON: `{}`, UpdatedAt: now},
	}
	servers := []model.Server{{ID: 1, Name: "entry", DetectedRegionCode: "HK"}, {ID: 2, Name: "exit", RegionMode: "manual", RegionCode: "JP", EntryAddress: "8.8.4.4"}}
	inbounds := []model.Inbound{{ID: 10, ServerID: 1, Name: "root", Protocol: model.ProtocolVLESS, Port: 443, ConfigJSON: `{}`, Enabled: true}}
	externals := []model.ExternalOutbound{{ID: externalID, Name: "old import", Protocol: model.ProtocolSocks, Scope: model.ExternalOutboundScopeGlobal, TargetAddress: "8.8.8.8", TargetPort: 1080, ConfigJSON: `{ "username": "a" }`, RegionMode: "manual", RegionCode: "DE", Enabled: true}}
	first := ExternalEgressProbeTargets([]model.ProxyPath{path}, steps, servers, inbounds, externals)[0]

	path.Name = "renamed"
	path.ExitRegionMode = "auto"
	path.ExitRegionCode = ""
	path.UpdatedAt = now.Add(time.Hour)
	steps[0].ConfigJSON = `{"chain_method":"2022-blake3-aes-128-gcm"}`
	steps[0].UpdatedAt = now.Add(time.Hour)
	servers[1].Name = "renamed exit"
	servers[1].RegionCode = "FR"
	externals[0].Name = "renamed import"
	externals[0].RegionMode = "auto"
	externals[0].RegionCode = ""
	second := ExternalEgressProbeTargets([]model.ProxyPath{path}, steps, servers, inbounds, externals)[0]
	if first.TopologyFingerprint != second.TopologyFingerprint {
		t.Fatalf("presentation-only changes altered fingerprint: %s != %s", first.TopologyFingerprint, second.TopologyFingerprint)
	}

	externals[0].TargetPort++
	third := ExternalEgressProbeTargets([]model.ProxyPath{path}, steps, servers, inbounds, externals)[0]
	if second.TopologyFingerprint == third.TopologyFingerprint {
		t.Fatal("topology change did not alter fingerprint")
	}
}
