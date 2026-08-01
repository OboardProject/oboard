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
	if mode := certificateSelectionMode(model.CertificateModeAuto, "wildcard"); mode != model.CertificateModeWildcard {
		t.Fatalf("automatic wildcard preference resolved to %q", mode)
	}
	if _, err := selectCertificate([]model.Certificate{exact}, certificateSelectionMode(model.CertificateModeAuto, "wildcard"), nil, "entry.example.com", "wildcard", now); err == nil {
		t.Fatal("automatic wildcard strategy fell back to an exact certificate")
	}
	if mode := certificateSelectionMode(model.CertificateModeAuto, "subdomain"); mode != model.CertificateModeExact {
		t.Fatalf("automatic exact preference resolved to %q", mode)
	}
	wildcardDomain, err := certificateIssuanceDomain(model.CertificateModeWildcard, "entry.example.com")
	if err != nil || wildcardDomain != "*.example.com" {
		t.Fatalf("wildcard issuance domain = %q, err=%v", wildcardDomain, err)
	}
	exactDomain, err := certificateIssuanceDomain(model.CertificateModeExact, "entry.example.com")
	if err != nil || exactDomain != "entry.example.com" {
		t.Fatalf("exact issuance domain = %q, err=%v", exactDomain, err)
	}
	if wildcardDomainMatches("*.example.com", "deep.entry.example.com") {
		t.Fatal("wildcard matched more than one DNS label")
	}
	automaticRequest, err := automaticCertificateRequest(map[string]string{
		settingCertificateAutoIssueACMECA:              "google",
		settingCertificateAutoIssueGoogleEABCredential: "42",
	}, "entry.example.com", 7)
	if err != nil || automaticRequest.ACMECA != "google" || automaticRequest.GoogleEABCredentialID == nil || *automaticRequest.GoogleEABCredentialID != 42 {
		t.Fatalf("automatic Google certificate request = %#v, err=%v", automaticRequest, err)
	}
	if automaticRequest.DNSCredentialID == nil || *automaticRequest.DNSCredentialID != 7 {
		t.Fatalf("automatic certificate DNS credential = %#v", automaticRequest.DNSCredentialID)
	}
	if _, err := automaticCertificateRequest(map[string]string{settingCertificateAutoIssueACMECA: "google"}, "entry.example.com", 7); err == nil {
		t.Fatal("automatic Google certificate request accepted no default EAB")
	}
}

func TestNormalizeInboundKeepsCustomCertificateDomain(t *testing.T) {
	custom := normalizeInbound(model.Inbound{DNSDomain: "Entry.Example.COM.", CertificateMode: model.CertificateModeExplicit, CertificateDomain: "TLS.Example.NET."})
	if custom.DNSDomain != "entry.example.com" || custom.CertificateDomain != "tls.example.net" {
		t.Fatalf("custom certificate domain was not preserved: %#v", custom)
	}
	following := normalizeInbound(model.Inbound{DNSDomain: "Entry.Example.COM.", CertificateMode: model.CertificateModeAuto})
	if following.CertificateDomain != following.DNSDomain {
		t.Fatalf("empty certificate domain did not follow DNS domain: %#v", following)
	}
}

func TestInjectManagedCertificateOverridesStaleSNI(t *testing.T) {
	raw, err := injectManagedCertificate(`{"tls":{"enabled":true,"server_name":"example.com"}}`, 9, "entry.example.net")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	tls := config["tls"].(map[string]any)
	if tls["server_name"] != "entry.example.net" {
		t.Fatalf("managed TLS SNI = %#v", tls["server_name"])
	}
}

func TestCustomEntryAcceptsExplicitCertificateSNI(t *testing.T) {
	certificateID := int64(9)
	inbound := normalizeInbound(model.Inbound{
		ServerID:          1,
		Name:              "custom-anytls",
		Protocol:          model.ProtocolAnyTLS,
		Port:              443,
		EntryIPMode:       model.EntryIPModeCustom,
		ExternalIP:        "203.0.113.10",
		CertificateMode:   model.CertificateModeExplicit,
		CertificateID:     &certificateID,
		CertificateDomain: "entry.example.net",
		ConfigJSON:        `{"tls":{"enabled":true}}`,
		Enabled:           true,
	})
	if err := validateInbound(inbound); err != nil {
		t.Fatalf("custom entry with explicit certificate SNI was rejected: %v", err)
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
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	const hmacKey = "test-google-eab-hmac-key"
	created := request(t, h, http.MethodPost, "/api/v2/ui/certificates", token, map[string]any{
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

	request(t, h, http.MethodPatch, "/api/v2/ui/certificates/"+strconv.FormatInt(id, 10), token, map[string]any{"eab_key_id": "google-key-id"}, http.StatusOK)
	stored, err = db.GetCertificate(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EABHMACKeyEncrypted != originalEncrypted {
		t.Fatal("PATCH without an HMAC key replaced the saved secret")
	}
	request(t, h, http.MethodPatch, "/api/v2/ui/certificates/"+strconv.FormatInt(id, 10), token, map[string]any{"eab_key_id": "different-key-id"}, http.StatusBadRequest)

	request(t, h, http.MethodPost, "/api/v2/ui/certificates", token, map[string]any{
		"name": "missing-eab", "domains": []string{"missing.example.com"}, "challenge_type": model.CertificateChallengeDNSManual, "acme_ca": "google",
	}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v2/ui/certificates", token, map[string]any{
		"name": "http-eab", "domains": []string{"http.example.com"}, "challenge_type": model.CertificateChallengeHTTP, "issuance_server_id": 1, "acme_ca": "google", "eab_key_id": "kid", "eab_hmac_key": "secret",
	}, http.StatusBadRequest)
}

func TestGoogleEABIsIncludedInACMEIssueArguments(t *testing.T) {
	args := issueACMEArgs("/tmp/acme", model.Certificate{
		ACMECA: "google", EABKeyID: "google-key-id", AccountEmail: "admin@example.com", Domains: []string{"example.com"},
	}, false, "google-key-id", "google-hmac-key")
	joined := strings.Join(args, "\x00")
	for _, expected := range []string{"--eab-kid\x00google-key-id", "--eab-hmac-key\x00google-hmac-key"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("ACME args missing %q: %#v", expected, args)
		}
	}
}

func TestSavedGoogleEABCredentialAPIAndCertificateSelection(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	const hmacKey = "saved-google-eab-hmac-key"
	created := request(t, h, http.MethodPost, "/api/v2/ui/google-eab-credentials", token, map[string]any{
		"key_id": "saved-google-key-id", "hmac_key": hmacKey, "remark": "生产账号",
	}, http.StatusCreated)["google_eab_credential"].(map[string]any)
	if created["key_id"] != "saved-google-key-id" || created["remark"] != "生产账号" || created["usage_count"] != float64(0) {
		t.Fatalf("saved Google EAB response = %#v", created)
	}
	for _, secretField := range []string{"hmac_key", "hmac_key_encrypted", "updated_at"} {
		if _, exists := created[secretField]; exists {
			t.Fatalf("saved Google EAB response exposed %s", secretField)
		}
	}
	credentialID := int64(created["id"].(float64))
	storedCredential, err := db.GetGoogleEABCredential(context.Background(), credentialID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := security.DecryptSecret("test-secret", googleEABHMACKeyPurpose, storedCredential.HMACKeyEncrypted)
	if err != nil || plain != hmacKey || storedCredential.HMACKeyEncrypted == hmacKey {
		t.Fatalf("stored saved EAB HMAC: value=%q err=%v", plain, err)
	}
	listed := request(t, h, http.MethodGet, "/api/v2/ui/google-eab-credentials", token, nil, http.StatusOK)["google_eab_credentials"].([]any)
	if len(listed) != 1 {
		t.Fatalf("saved Google EAB list = %#v", listed)
	}
	request(t, h, http.MethodPost, "/api/v2/ui/settings", token, map[string]any{
		"certificate_auto_issue_acme_ca": "google",
	}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v2/ui/settings", token, map[string]any{
		"certificate_auto_issue_acme_ca": "google", "certificate_auto_issue_google_eab_credential_id": credentialID + 1,
	}, http.StatusBadRequest)
	settingsResponse := request(t, h, http.MethodPost, "/api/v2/ui/settings", token, map[string]any{
		"certificate_auto_issue_acme_ca": "google", "certificate_auto_issue_google_eab_credential_id": credentialID,
	}, http.StatusOK)["settings"].(map[string]any)
	if settingsResponse[settingCertificateAutoIssueACMECA] != "google" || settingsResponse[settingCertificateAutoIssueGoogleEABCredential] != strconv.FormatInt(credentialID, 10) {
		t.Fatalf("automatic certificate issuer settings = %#v", settingsResponse)
	}
	request(t, h, http.MethodDelete, "/api/v2/ui/google-eab-credentials/"+strconv.FormatInt(credentialID, 10), token, nil, http.StatusConflict)
	request(t, h, http.MethodPost, "/api/v2/ui/settings", token, map[string]any{
		"certificate_auto_issue_acme_ca": "letsencrypt",
	}, http.StatusOK)

	createdCertificate := request(t, h, http.MethodPost, "/api/v2/ui/certificates", token, map[string]any{
		"name": "saved-google-eab", "domains": []string{"saved.example.com"}, "challenge_type": model.CertificateChallengeDNSManual,
		"acme_ca": "google", "google_eab_credential_id": credentialID,
	}, http.StatusCreated)["certificate"].(map[string]any)
	certificateID := int64(createdCertificate["id"].(float64))
	if int64(createdCertificate["google_eab_credential_id"].(float64)) != credentialID || createdCertificate["eab_configured"] != true {
		t.Fatalf("certificate saved EAB response = %#v", createdCertificate)
	}
	storedCertificate, err := db.GetCertificate(context.Background(), certificateID)
	if err != nil {
		t.Fatal(err)
	}
	keyID, resolvedHMAC, err := srv.certificateEABCredentials(context.Background(), storedCertificate)
	if err != nil || keyID != "saved-google-key-id" || resolvedHMAC != hmacKey {
		t.Fatalf("resolved saved EAB = key_id=%q hmac=%q err=%v", keyID, resolvedHMAC, err)
	}
	request(t, h, http.MethodDelete, "/api/v2/ui/google-eab-credentials/"+strconv.FormatInt(credentialID, 10), token, nil, http.StatusConflict)
	request(t, h, http.MethodDelete, "/api/v2/ui/certificates/"+strconv.FormatInt(certificateID, 10), token, nil, http.StatusOK)
	request(t, h, http.MethodDelete, "/api/v2/ui/google-eab-credentials/"+strconv.FormatInt(credentialID, 10), token, nil, http.StatusOK)

	request(t, h, http.MethodPost, "/api/v2/ui/certificates", token, map[string]any{
		"name": "missing-saved-eab", "domains": []string{"missing.example.com"}, "challenge_type": model.CertificateChallengeDNSManual,
		"acme_ca": "google", "google_eab_credential_id": credentialID,
	}, http.StatusBadRequest)
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
