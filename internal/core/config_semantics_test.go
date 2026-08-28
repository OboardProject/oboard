package core

import (
	"strings"
	"testing"
)

func TestSemanticConfigDigestSeparatesTrafficFromDataPlane(t *testing.T) {
	base := `{
		"inbounds": [{"type":"vless","listen_port":443,"tag":"in-1"}],
		"outbounds": [{"type":"direct","tag":"direct"}],
		"route": {"final":"direct"},
		"dns": {"servers":[{"type":"local","tag":"local"}]},
		"_oboard": {
			"rate_limits": {"users": {"alice": {"user_id": 7, "used_baseline_bytes": 100, "lease_bytes": 64}}},
			"trusted_forward": {"receivers": [{"id": "tf-1", "key": "secret-a"}]},
			"connection_audit": {"enabled": true}
		}
	}`
	grown := strings.Replace(base, `"used_baseline_bytes": 100`, `"used_baseline_bytes": 200`, 1)
	left, err := SemanticConfigDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	right, err := SemanticConfigDigest(grown)
	if err != nil {
		t.Fatal(err)
	}
	if left.ExactDigest == right.ExactDigest {
		t.Fatal("exact digest should change when used_baseline_bytes changes")
	}
	if left.TrafficPolicyDigest == right.TrafficPolicyDigest {
		t.Fatal("traffic policy digest should change when used_baseline_bytes changes")
	}
	if left.DataPlaneDigest != right.DataPlaneDigest {
		t.Fatalf("data-plane digest changed on traffic baseline: %s vs %s", left.DataPlaneDigest, right.DataPlaneDigest)
	}

	ported := strings.Replace(grown, `"listen_port":443`, `"listen_port":8443`, 1)
	portedDigest, err := SemanticConfigDigest(ported)
	if err != nil {
		t.Fatal(err)
	}
	if portedDigest.DataPlaneDigest == right.DataPlaneDigest {
		t.Fatal("data-plane digest must change when inbound port changes")
	}

	rotated := strings.Replace(base, `"key": "secret-a"`, `"key": "secret-b"`, 1)
	rotatedDigest, err := SemanticConfigDigest(rotated)
	if err != nil {
		t.Fatal(err)
	}
	if rotatedDigest.DataPlaneDigest == left.DataPlaneDigest {
		t.Fatal("data-plane digest must change when trusted_forward key changes")
	}
}

func TestCompareConfigSemanticsThreeWay(t *testing.T) {
	previous := `{"inbounds":[{"listen_port":443}],"_oboard":{"rate_limits":{"users":{"a":{"used_baseline_bytes":1}}},"trusted_forward":{"receivers":[{"id":"x"}]}}}`
	next := `{"inbounds":[{"listen_port":443}],"_oboard":{"rate_limits":{"users":{"a":{"used_baseline_bytes":2}}},"trusted_forward":{"receivers":[{"id":"x"}]}}}`
	got, err := CompareConfigSemantics(previous, next)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExactEqual || got.TrafficPolicyEqual || !got.DataPlaneEqual {
		t.Fatalf("comparison = %+v", got)
	}
}
