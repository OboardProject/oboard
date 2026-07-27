package controller

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestCertificateMaterialAndSelection(t *testing.T) {
	certificatePEM, privateKeyPEM := testCertificateMaterial(t, []string{"entry.example.com"}, time.Now().Add(60*24*time.Hour))
	material, err := validateCertificateMaterial(certificatePEM, certificatePEM, privateKeyPEM, []string{"entry.example.com"}, certificateMaterialPolicy{})
	if err != nil || len(material.Domains) != 1 {
		t.Fatalf("valid certificate material rejected: material=%#v err=%v", material, err)
	}
	_, otherKey := testCertificateMaterial(t, []string{"entry.example.com"}, time.Now().Add(60*24*time.Hour))
	if _, err := validateCertificateMaterial(certificatePEM, certificatePEM, otherKey, nil, certificateMaterialPolicy{}); err == nil {
		t.Fatal("mismatched private key was accepted")
	}

	now := time.Now()
	exactExpiry := now.Add(40 * 24 * time.Hour)
	wildcardExpiry := now.Add(80 * 24 * time.Hour)
	exact := model.Certificate{ID: 1, Status: model.CertificateStatusReady, Revision: "exact", Domains: []string{"entry.example.com"}, NotAfter: &exactExpiry}
	wildcard := model.Certificate{ID: 2, Status: model.CertificateStatusReady, Revision: "wild", Domains: []string{"*.example.com"}, NotAfter: &wildcardExpiry}
	selected, err := selectCertificate([]model.Certificate{wildcard, exact}, model.CertificateModeAuto, nil, "entry.example.com", "subdomain", now)
	if err != nil || selected.ID != exact.ID {
		t.Fatalf("exact preference selected %#v, err=%v", selected, err)
	}
	selected, err = selectCertificate([]model.Certificate{wildcard, exact}, model.CertificateModeAuto, nil, "entry.example.com", "wildcard", now)
	if err != nil || selected.ID != wildcard.ID {
		t.Fatalf("wildcard preference selected %#v, err=%v", selected, err)
	}
	if wildcardDomainMatches("*.example.com", "deep.entry.example.com") {
		t.Fatal("wildcard matched more than one DNS label")
	}
}

func TestCertificateAccountEmailDefaultsToPrimaryDomain(t *testing.T) {
	srv := &Server{}
	certificate, err := srv.buildCertificate(certificateRequest{
		Name:          "wildcard",
		Domains:       []string{"*.Example.COM", "example.com"},
		ChallengeType: model.CertificateChallengeDNSManual,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if certificate.AccountEmail != "admin@example.com" {
		t.Fatalf("default account email = %q, want admin@example.com", certificate.AccountEmail)
	}

	custom, err := srv.buildCertificate(certificateRequest{
		Name:          "custom",
		Domains:       []string{"entry.example.net"},
		ChallengeType: model.CertificateChallengeDNSManual,
		AccountEmail:  "ops@example.net",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if custom.AccountEmail != "ops@example.net" {
		t.Fatalf("custom account email was overwritten: %q", custom.AccountEmail)
	}
}

func TestGoogleEABCertificateAPIEncryptsAndPreservesSecret(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	const hmacKey = "test-google-eab-hmac-key"
	created := request(t, h, http.MethodPost, "/api/v1/certificates", token, map[string]any{
		"name":           "google-eab",
		"domains":        []string{"entry.example.com"},
		"challenge_type": model.CertificateChallengeDNSManual,
		"acme_ca":        "google",
		"account_email":  "admin@example.com",
		"eab_key_id":     "google-key-id",
		"eab_hmac_key":   hmacKey,
	}, http.StatusCreated)["certificate"].(map[string]any)
	if created["eab_key_id"] != "google-key-id" || created["eab_configured"] != true {
		t.Fatalf("EAB status response = %#v", created)
	}
	if _, exists := created["eab_hmac_key"]; exists {
		t.Fatal("API returned the EAB HMAC key")
	}
	if _, exists := created["eab_hmac_key_encrypted"]; exists {
		t.Fatal("API returned the encrypted EAB HMAC key")
	}

	id := int64(created["id"].(float64))
	stored, err := db.GetCertificate(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EABHMACKeyEncrypted == "" || stored.EABHMACKeyEncrypted == hmacKey {
		t.Fatalf("stored EAB HMAC value is not encrypted: %q", stored.EABHMACKeyEncrypted)
	}
	plain, err := security.DecryptSecret("test-secret", certificateEABHMACKeyPurpose, stored.EABHMACKeyEncrypted)
	if err != nil || plain != hmacKey {
		t.Fatalf("decrypt EAB HMAC: value=%q err=%v", plain, err)
	}
	originalEncrypted := stored.EABHMACKeyEncrypted

	request(t, h, http.MethodPatch, "/api/v1/certificates/"+strconv.FormatInt(id, 10), token, map[string]any{"eab_key_id": "google-key-id"}, http.StatusOK)
	stored, err = db.GetCertificate(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EABHMACKeyEncrypted != originalEncrypted {
		t.Fatal("PATCH without an HMAC key replaced the saved secret")
	}
	request(t, h, http.MethodPatch, "/api/v1/certificates/"+strconv.FormatInt(id, 10), token, map[string]any{"eab_key_id": "different-key-id"}, http.StatusBadRequest)

	request(t, h, http.MethodPost, "/api/v1/certificates", token, map[string]any{
		"name": "missing-eab", "domains": []string{"missing.example.com"}, "challenge_type": model.CertificateChallengeDNSManual, "acme_ca": "google",
	}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/certificates", token, map[string]any{
		"name": "http-eab", "domains": []string{"http.example.com"}, "challenge_type": model.CertificateChallengeHTTP, "issuance_server_id": 1, "acme_ca": "google", "eab_key_id": "kid", "eab_hmac_key": "secret",
	}, http.StatusBadRequest)
}

func TestGoogleEABIsIncludedInACMEIssueArguments(t *testing.T) {
	args := issueACMEArgs("/tmp/acme", model.Certificate{
		ACMECA: "google", EABKeyID: "google-key-id", AccountEmail: "admin@example.com", Domains: []string{"example.com"},
	}, false, "google-hmac-key")
	joined := strings.Join(args, "\x00")
	for _, expected := range []string{"--eab-kid\x00google-key-id", "--eab-hmac-key\x00google-hmac-key"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("ACME args missing %q: %#v", expected, args)
		}
	}
}

func TestParseACMEDNSChallenges(t *testing.T) {
	output := `[Sat] Domain: '_acme-challenge.example.com'
[Sat] TXT value: 'first-token'
[Sat] Domain: "_acme-challenge.example.com"
[Sat] TXT value: "second-token"`
	records := parseACMEDNSChallenges(output)
	if len(records) != 2 || records[0].Name != "_acme-challenge.example.com" || records[0].Content != "first-token" || records[1].Content != "second-token" {
		t.Fatalf("unexpected ACME DNS records: %#v", records)
	}
}

func TestAgentCertificateAssetAuthorization(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "edge", AgentID: "agent-1", AgentTokenHash: security.HashSecret("token-1"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	other := &model.Server{Name: "other", AgentID: "agent-2", AgentTokenHash: security.HashSecret("token-2"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, other); err != nil {
		t.Fatal(err)
	}
	certificate := &model.Certificate{Name: "entry", PrimaryDomain: "entry.example.com", Domains: []string{"entry.example.com"}, ChallengeType: model.CertificateChallengeDNSManual, ACMECA: "letsencrypt", Status: model.CertificateStatusPending}
	if err := db.CreateCertificate(ctx, certificate); err != nil {
		t.Fatal(err)
	}
	certificatePEM, privateKeyPEM := testCertificateMaterial(t, certificate.Domains, time.Now().Add(60*24*time.Hour))
	srv := newTestServer(db, "test-secret", "")
	if err := srv.storeCertificateMaterial(ctx, certificate, certificatePEM, certificatePEM, privateKeyPEM, certificateMaterialPolicy{}); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "tls", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, TLS: true, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInboundCertificateBinding(ctx, &model.InboundCertificateBinding{InboundID: inbound.ID, CertificateID: &certificate.ID, Mode: model.CertificateModeAuto, ServerName: "entry.example.com"}); err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()
	body, _ := json.Marshal(model.ManagedAssetRequest{Assets: []model.ManagedAssetReference{{Kind: "certificate", ID: certificate.ID, Revision: certificate.Revision}}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/assets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-ID", server.AgentID)
	req.Header.Set("Authorization", "Bearer token-1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "privkey.pem") || strings.Contains(recorder.Body.String(), privateKeyPEM) {
		t.Fatalf("authorized asset response status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/agent/assets", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-ID", other.AgentID)
	req.Header.Set("Authorization", "Bearer token-2")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unbound agent received certificate: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCoreConfigRefreshIncludesManagedCertificateAssets(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "edge", AgentID: "agent-1", AgentTokenHash: security.HashSecret("token-1"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	certificate := &model.Certificate{Name: "entry", PrimaryDomain: "entry.example.com", Domains: []string{"entry.example.com"}, ChallengeType: model.CertificateChallengeDNSManual, ACMECA: "letsencrypt", Status: model.CertificateStatusPending}
	if err := db.CreateCertificate(ctx, certificate); err != nil {
		t.Fatal(err)
	}
	certificatePEM, privateKeyPEM := testCertificateMaterial(t, certificate.Domains, time.Now().Add(60*24*time.Hour))
	srv := newTestServer(db, "test-secret", "")
	if err := srv.storeCertificateMaterial(ctx, certificate, certificatePEM, certificatePEM, privateKeyPEM, certificateMaterialPolicy{}); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "tls", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, TLS: true, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInboundCertificateBinding(ctx, &model.InboundCertificateBinding{InboundID: inbound.ID, CertificateID: &certificate.ID, Mode: model.CertificateModeExplicit, ServerName: "entry.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.queueCoreConfigRefreshForUserRemoval(ctx, 99, "user_deleted"); err != nil {
		t.Fatal(err)
	}
	tasks, err := db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("queued tasks=%d err=%v", len(tasks), err)
	}
	var payload model.ApplyCoreConfigTaskPayload
	if err := json.Unmarshal([]byte(tasks[0].PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	wantReference := model.ManagedAssetReference{Kind: "certificate", ID: certificate.ID, Revision: certificate.Revision}
	if len(payload.Assets) != 1 || payload.Assets[0] != wantReference {
		t.Fatalf("managed assets = %#v, want %#v", payload.Assets, wantReference)
	}
	if !strings.Contains(payload.Config, "oboard-asset://certificate/") || strings.Contains(payload.Config, privateKeyPEM) {
		t.Fatalf("refresh config did not use a managed certificate placeholder: %s", payload.Config)
	}
}

func TestManualDNSACMEIssueAndResume(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	certificatePEM, privateKeyPEM := testCertificateMaterial(t, []string{"entry.example.com"}, time.Now().Add(60*24*time.Hour))
	fixtureDir := t.TempDir()
	certSource := filepath.Join(fixtureDir, "cert.pem")
	keySource := filepath.Join(fixtureDir, "key.pem")
	if err := os.WriteFile(certSource, []byte(certificatePEM), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keySource, []byte(privateKeyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(fixtureDir, "fake-acme.sh")
	scriptBody := `#!/bin/sh
set -eu
mode=""
cert_file=""
fullchain_file=""
key_file=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --issue) mode=issue ;;
    --renew) mode=renew ;;
    --install-cert) mode=install ;;
    --cert-file) shift; cert_file=$1 ;;
    --fullchain-file) shift; fullchain_file=$1 ;;
    --key-file) shift; key_file=$1 ;;
  esac
  shift
done
if [ "$mode" = issue ]; then
  echo "Domain: '_acme-challenge.entry.example.com'"
  echo "TXT value: 'manual-token'"
  exit 1
fi
if [ "$mode" = install ]; then
  cp "$FAKE_ACME_CERT" "$cert_file"
  cp "$FAKE_ACME_CERT" "$fullchain_file"
  cp "$FAKE_ACME_KEY" "$key_file"
fi
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_ACME_CERT", certSource)
	t.Setenv("FAKE_ACME_KEY", keySource)
	srv := newTestServer(db, "test-secret", "")
	srv.acmeCommand = script
	srv.acmeHome = filepath.Join(t.TempDir(), "acme")
	certificate := &model.Certificate{Name: "manual", PrimaryDomain: "entry.example.com", Domains: []string{"entry.example.com"}, ChallengeType: model.CertificateChallengeDNSManual, ACMECA: "letsencrypt", Status: model.CertificateStatusPending, AutoRenew: true}
	if err := db.CreateCertificate(context.Background(), certificate); err != nil {
		t.Fatal(err)
	}
	if err := srv.startACMECertificateIssue(context.Background(), certificate, false, false); err != nil {
		t.Fatal(err)
	}
	waitForCertificateStatus(t, db, certificate.ID, model.CertificateStatusAwaitingDNS)
	awaiting, err := db.GetCertificate(context.Background(), certificate.ID)
	if err != nil || len(awaiting.ValidationRecords) != 1 || awaiting.ValidationRecords[0].Content != "manual-token" {
		t.Fatalf("manual challenge state=%#v err=%v", awaiting, err)
	}
	if err := srv.startACMECertificateIssue(context.Background(), awaiting, false, true); err != nil {
		t.Fatal(err)
	}
	waitForCertificateStatus(t, db, certificate.ID, model.CertificateStatusReady)
	ready, err := db.GetCertificate(context.Background(), certificate.ID)
	if err != nil || ready.Revision == "" || ready.PrivateKeyEncrypted == "" || strings.Contains(ready.PrivateKeyEncrypted, "BEGIN PRIVATE KEY") {
		t.Fatalf("issued certificate was not encrypted: %#v err=%v", ready, err)
	}
}

func waitForCertificateStatus(t *testing.T, db *store.Store, id int64, status string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		certificate, err := db.GetCertificate(context.Background(), id)
		if err == nil && certificate.Status == status {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	certificate, _ := db.GetCertificate(context.Background(), id)
	t.Fatalf("certificate status = %q, want %q: %#v", certificate.Status, status, certificate)
}

func testCertificateMaterial(t *testing.T, domains []string, notAfter time.Time) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Hour)
	template := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: domains[0]}, DNSNames: domains, NotBefore: now, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

// TestAgentReportedCertificateMaterialIsUntrusted pins the trust boundary for
// Agent-reported certificate material. A node must not be able to substitute
// self-signed material or widen the operator-approved domain set, because the
// stored certificate is redistributed to every node bound to it and its
// primary domain flows back into Controller-driven renewals.
func TestAgentReportedCertificateMaterialIsUntrusted(t *testing.T) {
	approved := []string{"entry.example.com"}
	selfSignedPEM, selfSignedKey := testCertificateMaterial(t, approved, time.Now().Add(60*24*time.Hour))

	// Operator import and Controller ACME remain able to store this material.
	if _, err := validateCertificateMaterial(selfSignedPEM, selfSignedPEM, selfSignedKey, approved, certificateMaterialPolicy{}); err != nil {
		t.Fatalf("trusted path rejected valid material: %v", err)
	}

	// The same material reported by an Agent must be refused: an allowlisted
	// public ACME CA can never produce an unverifiable chain.
	if _, err := validateCertificateMaterial(selfSignedPEM, selfSignedPEM, selfSignedKey, approved, untrustedCertificateMaterial); err == nil {
		t.Fatal("agent-reported self-signed certificate was accepted")
	}

	// A SAN outside the approved set must be refused regardless of chain.
	widenedPEM, widenedKey := testCertificateMaterial(t, []string{"entry.example.com", "victim.example.com"}, time.Now().Add(60*24*time.Hour))
	if _, err := validateCertificateMaterial(widenedPEM, widenedPEM, widenedKey, approved, untrustedCertificateMaterial); err == nil {
		t.Fatal("agent-reported certificate widened the approved domain set")
	}
}

func TestRequireApprovedDomainSetIgnoresOrdering(t *testing.T) {
	// The issuing CA chooses SAN ordering, so equality must be set-based.
	if err := requireApprovedDomainSet([]string{"a.example.com", "b.example.com"}, []string{"b.example.com", "a.example.com"}); err != nil {
		t.Fatalf("reordered SAN set rejected: %v", err)
	}
	if err := requireApprovedDomainSet([]string{"a.example.com"}, []string{"a.example.com", "b.example.com"}); err == nil {
		t.Fatal("extra SAN accepted")
	}
	if err := requireApprovedDomainSet([]string{"a.example.com", "b.example.com"}, []string{"a.example.com"}); err == nil {
		t.Fatal("missing SAN accepted")
	}
	if err := requireApprovedDomainSet(nil, []string{"a.example.com"}); err == nil {
		t.Fatal("empty approved set accepted")
	}
}

// TestWARPPrivateKeyIsNotExposedByAPI pins that WireGuard key material stays
// server-side. WARP profiles are Operator-reachable, so a raw config_json
// would hand every operator the node's live private key.
func TestWARPPrivateKeyIsNotExposedByAPI(t *testing.T) {
	const stored = `{"type":"wireguard","private_key":"c2VjcmV0LWtleS12YWx1ZQ==","peers":[{"public_key":"cHVi"}]}`

	redacted := publicWARPProfile(model.WARPProfile{ConfigJSON: stored})
	if strings.Contains(redacted.ConfigJSON, "c2VjcmV0LWtleS12YWx1ZQ==") {
		t.Fatalf("private key leaked in API response: %s", redacted.ConfigJSON)
	}
	if !strings.Contains(redacted.ConfigJSON, "private_key_configured") {
		t.Fatalf("redacted config lost the configured marker: %s", redacted.ConfigJSON)
	}
	// Non-secret fields must survive so the UI keeps working.
	if !strings.Contains(redacted.ConfigJSON, "cHVi") {
		t.Fatalf("redaction dropped non-secret fields: %s", redacted.ConfigJSON)
	}

	// A redacted round-trip must not destroy the stored key.
	restored := restoreWARPPrivateKey(redacted.ConfigJSON, stored)
	if !strings.Contains(restored, "c2VjcmV0LWtleS12YWx1ZQ==") {
		t.Fatalf("round-trip destroyed the stored private key: %s", restored)
	}
	if strings.Contains(restored, "private_key_configured") {
		t.Fatalf("restored config kept the redaction marker: %s", restored)
	}

	// A genuinely new key must still replace the stored one.
	rotated := restoreWARPPrivateKey(`{"type":"wireguard","private_key":"bmV3LWtleQ=="}`, stored)
	if !strings.Contains(rotated, "bmV3LWtleQ==") {
		t.Fatalf("rotation was ignored: %s", rotated)
	}
}
