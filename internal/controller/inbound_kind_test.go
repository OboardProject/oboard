package controller

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func TestApplyInboundKindRejectsVLESSTLSVision(t *testing.T) {
	inbound := model.Inbound{Kind: "vless-tls-vision", Protocol: model.ProtocolVLESS, ConfigJSON: `{"tls":{"enabled":true}}`}
	err := applyInboundKindDefaults(&inbound, nil)
	if err == nil {
		t.Fatal("expected vless-tls-vision to be rejected")
	}
	var fieldErr *core.ConfigFieldError
	if !errors.As(err, &fieldErr) || fieldErr.Path != "kind" {
		t.Fatalf("error = %v", err)
	}
}

func TestInferredInboundKindOmitsVLESSTLSVision(t *testing.T) {
	if got := inferredInboundKind(model.Inbound{Protocol: model.ProtocolVLESS, ConfigJSON: `{"flow":"xtls-rprx-vision","tls":{"enabled":true}}`}); got != "" {
		t.Fatalf("tls-only vless kind = %q, want empty", got)
	}
	if got := inferredInboundKind(model.Inbound{Protocol: model.ProtocolVLESS, ConfigJSON: `{}`}); got != "vless-tcp" {
		t.Fatalf("plain vless kind = %q", got)
	}
}

func TestApplyInboundKindHY2Defaults(t *testing.T) {
	standard := model.Inbound{Kind: "hy2-tls", Protocol: model.ProtocolHY2, ConfigJSON: `{}`}
	if err := applyInboundKindDefaults(&standard, nil); err != nil {
		t.Fatal(err)
	}
	var standardCfg map[string]any
	if err := json.Unmarshal([]byte(standard.ConfigJSON), &standardCfg); err != nil {
		t.Fatal(err)
	}
	if standardCfg["up_mbps"] != float64(1000) || standardCfg["down_mbps"] != float64(500) {
		t.Fatalf("standard hy2 bandwidth = %#v", standardCfg)
	}
	if _, exists := standardCfg["obfs"]; exists {
		t.Fatalf("standard hy2 must not keep obfs: %#v", standardCfg)
	}

	salamander := model.Inbound{Kind: "hy2-salamander", Protocol: model.ProtocolHY2, ConfigJSON: `{"obfs":{"type":"salamander"}}`}
	if err := applyInboundKindDefaults(&salamander, nil); err != nil {
		t.Fatal(err)
	}
	var salamanderCfg map[string]any
	if err := json.Unmarshal([]byte(salamander.ConfigJSON), &salamanderCfg); err != nil {
		t.Fatal(err)
	}
	if salamanderCfg["up_mbps"] != float64(1000) || salamanderCfg["down_mbps"] != float64(500) {
		t.Fatalf("salamander hy2 bandwidth = %#v", salamanderCfg)
	}
	obfs, _ := salamanderCfg["obfs"].(map[string]any)
	if obfs == nil || obfs["type"] != "salamander" {
		t.Fatalf("salamander obfs = %#v", salamanderCfg["obfs"])
	}
	password, _ := obfs["password"].(string)
	if strings.TrimSpace(password) == "" {
		t.Fatal("salamander password must be generated")
	}

	keep := model.Inbound{Kind: "hy2-salamander", Protocol: model.ProtocolHY2, ConfigJSON: `{"up_mbps":200,"down_mbps":80,"obfs":{"type":"salamander","password":"keep-me"}}`}
	if err := applyInboundKindDefaults(&keep, nil); err != nil {
		t.Fatal(err)
	}
	var keepCfg map[string]any
	if err := json.Unmarshal([]byte(keep.ConfigJSON), &keepCfg); err != nil {
		t.Fatal(err)
	}
	if keepCfg["up_mbps"] != float64(200) || keepCfg["down_mbps"] != float64(80) {
		t.Fatalf("existing bandwidth overwritten: %#v", keepCfg)
	}
	keepObfs, _ := keepCfg["obfs"].(map[string]any)
	if keepObfs["password"] != "keep-me" {
		t.Fatalf("existing salamander password overwritten: %#v", keepObfs)
	}
}

func TestInferredInboundKindHY2Salamander(t *testing.T) {
	if got := inferredInboundKind(model.Inbound{Protocol: model.ProtocolHY2, ConfigJSON: `{"tls":{"enabled":true}}`}); got != "hy2-tls" {
		t.Fatalf("standard kind = %q", got)
	}
	if got := inferredInboundKind(model.Inbound{Protocol: model.ProtocolHY2, ConfigJSON: `{"obfs":{"type":"salamander","password":"x"}}`}); got != "hy2-salamander" {
		t.Fatalf("salamander kind = %q", got)
	}
}

func TestMergeInboundPresetConfigSkipsHY2BandwidthAndObfsPassword(t *testing.T) {
	preset := map[string]any{
		"tls":      map[string]any{"enabled": true},
		"up_mbps":  100,
		"down_mbps": 100,
		"obfs":     map[string]any{"type": "salamander", "password": "from-preset"},
	}
	inbound := map[string]any{
		"up_mbps":  1000,
		"down_mbps": 500,
		"obfs":     map[string]any{"type": "salamander", "password": "from-inbound"},
	}
	merged := mergeInboundPresetConfig(preset, inbound)
	if merged["up_mbps"] != 1000 || merged["down_mbps"] != 500 {
		t.Fatalf("inbound bandwidth should win, got %#v", merged)
	}
	obfs, _ := merged["obfs"].(map[string]any)
	if obfs["type"] != "salamander" || obfs["password"] != "from-inbound" {
		t.Fatalf("obfs merge = %#v", obfs)
	}

	created := mergeInboundPresetConfig(preset, map[string]any{"tls": map[string]any{"enabled": true}})
	if _, exists := created["up_mbps"]; exists {
		t.Fatalf("preset bandwidth leaked into inbound: %#v", created)
	}
	createdObfs, _ := created["obfs"].(map[string]any)
	if _, exists := createdObfs["password"]; exists {
		t.Fatalf("preset salamander password leaked: %#v", createdObfs)
	}
}

func TestValidateInboundAllowsSnellAdvertisePort(t *testing.T) {
	inbound := normalizeInbound(model.Inbound{
		ServerID:      1,
		Name:          "snell",
		Protocol:      model.ProtocolSnell,
		Port:          3005,
		AdvertisePort: 2627,
		ConfigJSON:    `{"version":4,"psk":"inbound-seed-psk-1234"}`,
		Enabled:       true,
	})
	if err := validateInbound(inbound); err != nil {
		t.Fatalf("single-listener Snell advertise_port rejected: %v", err)
	}
	inbound.AdvertisePort = inbound.Port
	if err := validateInbound(inbound); err != nil {
		t.Fatalf("Snell advertise_port matching its logical port rejected: %v", err)
	}
}

// A managed TLS inbound needs an SNI hostname its certificate covers, not a DNS
// record: clients may dial the server IP and still complete TLS from that SNI.
func TestManagedTLSInboundNeedsSNIWithoutDNSRecord(t *testing.T) {
	hy2 := normalizeInbound(model.Inbound{ServerID: 1, Name: "hy2", Protocol: model.ProtocolHY2, Port: 443, TLS: true, ConfigJSON: `{"tls":{"enabled":true}}`, Enabled: true})
	if !inboundUsesManagedCertificate(hy2) || hy2.DNSSyncEnabled {
		t.Fatalf("hy2 managed TLS defaults: managed=%v dns_sync=%v mode=%s", inboundUsesManagedCertificate(hy2), hy2.DNSSyncEnabled, hy2.CertificateMode)
	}
	if err := validateInbound(hy2); err == nil || !strings.Contains(err.Error(), "SNI 域名") {
		t.Fatalf("hy2 without SNI: %v", err)
	}
	hy2.CertificateDomain = "entry.example.com"
	if err := validateInbound(hy2); err != nil {
		t.Fatalf("hy2 with SNI but without a DNS record: %v", err)
	}
	anytls := normalizeInbound(model.Inbound{ServerID: 1, Name: "anytls", Protocol: model.ProtocolAnyTLS, Port: 443, TLS: true, CertificateDomain: "entry.example.com", ConfigJSON: `{"tls":{"enabled":true}}`, Enabled: true})
	if anytls.DNSSyncEnabled {
		t.Fatal("anytls must not force DNS record sync")
	}
	if err := validateInbound(anytls); err != nil {
		t.Fatalf("anytls with SNI but without a DNS record: %v", err)
	}
	synced := normalizeInbound(model.Inbound{ServerID: 1, Name: "anytls-dns", Protocol: model.ProtocolAnyTLS, Port: 443, TLS: true, DNSSyncEnabled: true, DNSDomain: "entry.example.com", ConfigJSON: `{"tls":{"enabled":true}}`, Enabled: true})
	if synced.CertificateDomain != "entry.example.com" {
		t.Fatalf("synced SNI = %q, want it to follow dns_domain", synced.CertificateDomain)
	}
	if err := validateInbound(synced); err == nil || !strings.Contains(err.Error(), "DNS 凭据") {
		t.Fatalf("dns sync without a credential: %v", err)
	}
	external := normalizeInbound(model.Inbound{ServerID: 1, Name: "anytls-ext", Protocol: model.ProtocolAnyTLS, Port: 443, CertificateMode: model.CertificateModeExternal, ConfigJSON: `{"tls":{"enabled":true,"certificate_path":"/tmp/cert.pem","key_path":"/tmp/key.pem"}}`, Enabled: true})
	if inboundUsesManagedCertificate(external) {
		t.Fatal("external AnyTLS should not use a managed certificate")
	}
}

func TestFollowInboundCertificateDomainOnDNSChange(t *testing.T) {
	current := model.Inbound{DNSDomain: "old.example.com", CertificateDomain: "old.example.com", CertificateMode: model.CertificateModeAuto}
	next := current
	next.DNSDomain = "new.example.com"
	next.CertificateDomain = "old.example.com"
	followInboundCertificateDomain(&next, &current)
	if next.CertificateDomain != "new.example.com" {
		t.Fatalf("followed SNI = %q", next.CertificateDomain)
	}

	current.CertificateDomain = "sni.example.com"
	next = current
	next.DNSDomain = "new.example.com"
	next.CertificateDomain = "sni.example.com"
	followInboundCertificateDomain(&next, &current)
	if next.CertificateDomain != "sni.example.com" {
		t.Fatalf("custom SNI overwritten: %q", next.CertificateDomain)
	}

	create := model.Inbound{DNSDomain: "entry.example.com", CertificateMode: model.CertificateModeAuto}
	followInboundCertificateDomain(&create, nil)
	if create.CertificateDomain != "entry.example.com" {
		t.Fatalf("create SNI = %q", create.CertificateDomain)
	}
}
