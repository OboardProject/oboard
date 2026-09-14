package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func repairInbound(protocol model.Protocol, configJSON string) model.Inbound {
	return model.Inbound{ID: 1, ServerID: 1, Name: "n", Protocol: protocol, ListenIP: "0.0.0.0", Port: 10001, ConfigJSON: configJSON}
}

func TestNormalizeInboundConfigRemovesUnsupportedMultiplex(t *testing.T) {
	// Hysteria2 multiplexes over QUIC, so a stored sing-box multiplex object is
	// dead weight the kernel ignores.
	inbound := repairInbound(model.ProtocolHY2, `{"tls":{"enabled":true},"up_mbps":1000,"multiplex":{"enabled":true}}`)
	if err := ValidateStoredInbound(inbound); err == nil {
		t.Fatal("expected the stored HY2 multiplex object to be rejected")
	}
	result, err := NormalizeInboundConfig(inbound)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !result.Resolved {
		t.Fatalf("expected the document to be repairable, remaining=%q", result.Remaining)
	}
	if len(result.RemovedPaths) != 1 || result.RemovedPaths[0] != "multiplex" {
		t.Fatalf("unexpected removals %v", result.RemovedPaths)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(result.ConfigJSON), &document); err != nil {
		t.Fatalf("decode repaired document: %v", err)
	}
	if _, exists := document["multiplex"]; exists {
		t.Fatal("multiplex survived the repair")
	}
	if _, exists := document["up_mbps"]; !exists {
		t.Fatal("repair dropped an unrelated field")
	}
}

func TestNormalizeInboundConfigRemovesInapplicableTCPFastOpen(t *testing.T) {
	inbound := repairInbound(model.ProtocolHY2, `{"tls":{"enabled":true},"tcp_fast_open":true}`)
	result, err := NormalizeInboundConfig(inbound)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !result.Resolved || len(result.RemovedPaths) != 1 || result.RemovedPaths[0] != "tcp_fast_open" {
		t.Fatalf("unexpected result %+v", result)
	}
}

func TestNormalizeInboundConfigRemovesClientOnlyMultiplexOptions(t *testing.T) {
	// VLESS accepts the multiplex object, but protocol/stream limits are dial
	// side only. Removing them keeps the operator's enabled/padding choice.
	inbound := repairInbound(model.ProtocolVLESS, `{"multiplex":{"enabled":true,"protocol":"smux","max_streams":8}}`)
	result, err := NormalizeInboundConfig(inbound)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !result.Resolved {
		t.Fatalf("expected repair, remaining=%q", result.Remaining)
	}
	if strings.Join(result.RemovedPaths, ",") != "multiplex.max_streams,multiplex.protocol" {
		t.Fatalf("unexpected removals %v", result.RemovedPaths)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(result.ConfigJSON), &document); err != nil {
		t.Fatalf("decode: %v", err)
	}
	multiplex, ok := document["multiplex"].(map[string]any)
	if !ok {
		t.Fatalf("multiplex object was dropped entirely: %s", result.ConfigJSON)
	}
	if enabled, _ := multiplex["enabled"].(bool); !enabled {
		t.Fatal("repair lost the operator's multiplex choice")
	}
}

func TestNormalizeInboundConfigRemovesUnsupportedRealityField(t *testing.T) {
	// The Reality allowlist reports the exact path, so the repair can act on it
	// without owning a second allowlist of its own.
	config := `{"tls":{"enabled":true,"server_name":"example.com","reality":{"enabled":true,"private_key":"ELFRvjNeWsAKKnhXqPCXL7Zm9U0ES_gT-4SqCU1cwlY","public_key":"ELFRvjNeWsAKKnhXqPCXL7Zm9U0ES_gT-4SqCU1cwlY","short_id":"ab12","handshake":{"server":"example.com","server_port":443},"legacy_dest":"example.com:443"}}}`
	inbound := repairInbound(model.ProtocolVLESS, config)
	result, err := NormalizeInboundConfig(inbound)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !result.Resolved {
		t.Fatalf("expected repair, remaining=%q", result.Remaining)
	}
	if len(result.RemovedPaths) != 1 || result.RemovedPaths[0] != "tls.reality.legacy_dest" {
		t.Fatalf("unexpected removals %v", result.RemovedPaths)
	}
}

func TestNormalizeInboundConfigLeavesMissingRequiredFieldAlone(t *testing.T) {
	// A required field that is absent cannot be repaired by removing anything.
	// Reporting it unresolved is the honest answer; guessing a value would
	// change behaviour instead of clearing debt.
	config := `{"tls":{"enabled":true,"reality":{"enabled":true,"private_key":"ELFRvjNeWsAKKnhXqPCXL7Zm9U0ES_gT-4SqCU1cwlY","public_key":"ELFRvjNeWsAKKnhXqPCXL7Zm9U0ES_gT-4SqCU1cwlY","short_id":"ab12","handshake":{"server":"example.com","server_port":443}}}}`
	inbound := repairInbound(model.ProtocolVLESS, config)
	result, err := NormalizeInboundConfig(inbound)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if result.Resolved {
		t.Fatal("expected the missing server_name to stay unresolved")
	}
	if len(result.RemovedPaths) != 0 {
		t.Fatalf("repair removed %v for a missing-required error", result.RemovedPaths)
	}
	if !strings.Contains(result.Remaining, "server_name") {
		t.Fatalf("remaining error lost the field location: %q", result.Remaining)
	}
}

func TestNormalizeInboundConfigKeepsValidDocumentUnchanged(t *testing.T) {
	inbound := repairInbound(model.ProtocolSS, `{"method":"aes-128-gcm","multiplex":{"enabled":true}}`)
	result, err := NormalizeInboundConfig(inbound)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !result.Resolved {
		t.Fatalf("valid document reported unresolved: %q", result.Remaining)
	}
	if len(result.RemovedPaths) != 0 {
		t.Fatalf("repair touched a valid document: %v", result.RemovedPaths)
	}
}

func TestNormalizeInboundConfigReportsUndecodableDocument(t *testing.T) {
	inbound := repairInbound(model.ProtocolVLESS, `not json`)
	result, err := NormalizeInboundConfig(inbound)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if result.Resolved || result.Remaining == "" {
		t.Fatalf("expected an unresolved finding for a broken document: %+v", result)
	}
	if result.ConfigJSON != inbound.ConfigJSON {
		t.Fatal("an undecodable document must be left byte-identical")
	}
}

func TestDeleteDocumentPathDropsEmptiedParent(t *testing.T) {
	document := map[string]any{"tls": map[string]any{"reality": map[string]any{"bad": true}}}
	if !deleteDocumentPath(document, "tls.reality.bad") {
		t.Fatal("expected the path to be removed")
	}
	if len(document) != 0 {
		t.Fatalf("emptied parents survived: %v", document)
	}
}
