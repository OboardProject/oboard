package core

import (
	"github.com/OboardProject/oboard/internal/model"
	"testing"
)

func TestReadyWARPProfileUsesCurrentUnderlay(t *testing.T) {
	endpoint, err := warpProfileToSingBox(model.WARPProfile{ID: 1, UnderlayJSON: `{"mode":"interface","interface_name":"he-ipv6","family":"ipv6_only"}`, ConfigJSON: `{"type":"wireguard","bind_interface":"old","inet4_bind_address":"192.0.2.1","peers":[{"address":"engage.cloudflareclient.com"}]}`}, model.Server{ID: 1, IPStack: model.IPStackIPv4Only})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint["bind_interface"] != "he-ipv6" || endpoint["inet4_bind_address"] != nil || endpoint["domain_resolver"].(map[string]any)["strategy"] != "ipv6_only" {
		t.Fatal("ready profile retained cached underlay or server family")
	}
}

func TestWARPUnderlayKeepsOuterFamilySeparate(t *testing.T) {
	dc, err := ValidateDialConstraint(`{"mode":"interface","interface_name":"he-ipv6","source_address":"2001:db8::2","family":"ipv6_only"}`)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := map[string]any{"peers": []map[string]any{{"address": "engage.cloudflareclient.com", "allowed_ips": []string{"0.0.0.0/0", "::/0"}}}}
	if err := ApplyDialConstraintToEndpoint(endpoint, dc); err != nil {
		t.Fatal(err)
	}
	if endpoint["bind_interface"] != "he-ipv6" || endpoint["inet6_bind_address"] != "2001:db8::2" || endpoint["domain_resolver"].(map[string]any)["strategy"] != "ipv6_only" {
		t.Fatal("outer socket is not constrained")
	}
	if len(endpoint["peers"].([]map[string]any)[0]["allowed_ips"].([]string)) != 2 {
		t.Fatal("outer family restricted tunnel payload")
	}
	endpoint["peers"].([]map[string]any)[0]["address"] = "192.0.2.1"
	if err := ApplyDialConstraintToEndpoint(endpoint, dc); err == nil {
		t.Fatal("IPv4 literal peer accepted by IPv6-only underlay")
	}
	if err := ApplyDialConstraintToEndpoint(endpoint, &NormalizedDialConstraint{Mode: "auto"}); err != nil {
		t.Fatal(err)
	}
	if endpoint["bind_interface"] != nil || endpoint["inet6_bind_address"] != nil {
		t.Fatal("automatic underlay retained stale binding")
	}
}

func TestWARPUnderlaySourceAddressIsNotPrefix(t *testing.T) {
	if _, err := ValidateDialConstraint(`{"mode":"interface","interface_name":"he-ipv6","source_address":"2001:db8::/64"}`); err == nil {
		t.Fatal("prefix was silently converted to an exact source address")
	}
}
