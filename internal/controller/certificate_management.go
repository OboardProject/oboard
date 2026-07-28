package controller

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

type certificateRequest struct {
	Name                  string   `json:"name"`
	Domains               []string `json:"domains"`
	ChallengeType         string   `json:"challenge_type"`
	DNSCredentialID       *int64   `json:"dns_credential_id"`
	IssuanceServerID      *int64   `json:"issuance_server_id"`
	ACMECA                string   `json:"acme_ca"`
	AccountEmail          string   `json:"account_email"`
	GoogleEABCredentialID *int64   `json:"google_eab_credential_id"`
	EABKeyID              *string  `json:"eab_key_id"`
	EABHMACKey            *string  `json:"eab_hmac_key"`
	AutoRenew             *bool    `json:"auto_renew"`
}

const certificateEABHMACKeyPurpose = "certificate-eab-hmac-key"

var errCertificateProvisioning = errors.New("certificate provisioning in progress")

func (s *Server) certificates(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.ListCertificates(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"certificates": items})
	case http.MethodPost:
		var req certificateRequest
		if !decode(w, r, &req) {
			return
		}
		certificate, err := s.buildCertificate(req, nil)
		if err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.validateCertificateReferences(r.Context(), *certificate); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateCertificate(r.Context(), certificate); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "certificate", strconv.FormatInt(certificate.ID, 10))
		write(w, 201, map[string]any{"certificate": certificate})
	default:
		method(w)
	}
}

func (s *Server) certificateSubroutes(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/certificates/")
	if len(parts) == 1 && parts[0] == "import" {
		s.importCertificate(w, r)
		return
	}
	if len(parts) == 0 {
		fail(w, errors.New("missing certificate id"), 400)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		fail(w, errors.New("invalid certificate id"), 400)
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "issue":
			s.issueCertificate(w, r, id, false)
			return
		case "renew":
			s.issueCertificate(w, r, id, true)
			return
		case "confirm-dns":
			s.confirmCertificateDNS(w, r, id)
			return
		}
	}
	if len(parts) != 1 {
		fail(w, errors.New("unknown certificate route"), 404)
		return
	}
	current, err := s.store.GetCertificate(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	switch r.Method {
	case http.MethodGet:
		write(w, 200, map[string]any{"certificate": current})
	case http.MethodPatch:
		var req certificateRequest
		if !decode(w, r, &req) {
			return
		}
		certificate, err := s.buildCertificate(req, current)
		if err != nil {
			fail(w, err, 400)
			return
		}
		certificate.ID = id
		if err := s.validateCertificateReferences(r.Context(), *certificate); err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.UpdateCertificate(r.Context(), certificate); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "update", "certificate", strconv.FormatInt(id, 10))
		write(w, 200, map[string]any{"certificate": certificate})
	case http.MethodDelete:
		if err := s.ensureCertificateUnused(r.Context(), id); err != nil {
			fail(w, err, http.StatusConflict)
			return
		}
		if err := s.store.DeleteCertificate(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "certificate", strconv.FormatInt(id, 10))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) buildCertificate(req certificateRequest, current *model.Certificate) (*model.Certificate, error) {
	certificate := &model.Certificate{Status: model.CertificateStatusPending, ACMECA: "letsencrypt", AutoRenew: true}
	if current != nil {
		*certificate = *current
	}
	if strings.TrimSpace(req.Name) != "" || current == nil {
		certificate.Name = strings.TrimSpace(req.Name)
	}
	if req.Domains != nil {
		domains, err := normalizeCertificateDomains(req.Domains)
		if err != nil {
			return nil, err
		}
		certificate.Domains = domains
	}
	if req.ChallengeType != "" || current == nil {
		certificate.ChallengeType = strings.ToLower(strings.TrimSpace(req.ChallengeType))
	}
	if req.DNSCredentialID != nil || current == nil {
		certificate.DNSCredentialID = req.DNSCredentialID
	}
	if req.IssuanceServerID != nil || current == nil {
		certificate.IssuanceServerID = req.IssuanceServerID
	}
	if strings.TrimSpace(req.ACMECA) != "" {
		certificate.ACMECA = strings.ToLower(strings.TrimSpace(req.ACMECA))
	}
	if req.AccountEmail != "" || current == nil {
		certificate.AccountEmail = strings.TrimSpace(req.AccountEmail)
	}
	if req.AutoRenew != nil {
		certificate.AutoRenew = *req.AutoRenew
	}
	if certificate.Name == "" {
		return nil, errors.New("name required")
	}
	if len(certificate.Domains) == 0 {
		return nil, errors.New("at least one certificate domain is required")
	}
	certificate.PrimaryDomain = certificate.Domains[0]
	if strings.TrimSpace(certificate.AccountEmail) == "" {
		certificate.AccountEmail = defaultCertificateAccountEmail(certificate.PrimaryDomain)
	}
	certificate.Wildcard = false
	for _, domain := range certificate.Domains {
		certificate.Wildcard = certificate.Wildcard || strings.HasPrefix(domain, "*.")
	}
	switch certificate.ChallengeType {
	case model.CertificateChallengeHTTP:
		if certificate.Wildcard {
			return nil, errors.New("HTTP-01 cannot issue wildcard certificates")
		}
		if certificate.IssuanceServerID == nil || *certificate.IssuanceServerID <= 0 {
			return nil, errors.New("HTTP-01 requires issuance_server_id")
		}
	case model.CertificateChallengeDNS:
		if certificate.DNSCredentialID == nil || *certificate.DNSCredentialID <= 0 {
			return nil, errors.New("managed DNS-01 requires dns_credential_id")
		}
	case model.CertificateChallengeDNSManual:
	default:
		return nil, fmt.Errorf("invalid challenge_type %q", certificate.ChallengeType)
	}
	switch certificate.ACMECA {
	case "letsencrypt", "zerossl", "buypass", "google":
	default:
		return nil, fmt.Errorf("unsupported ACME CA %q", certificate.ACMECA)
	}
	if err := s.applyCertificateEAB(req, certificate, current); err != nil {
		return nil, err
	}
	return certificate, nil
}

func (s *Server) applyCertificateEAB(req certificateRequest, certificate *model.Certificate, current *model.Certificate) error {
	if certificate.ACMECA != "google" {
		certificate.GoogleEABCredentialID = nil
		certificate.EABKeyID = ""
		certificate.EABHMACKeyEncrypted = ""
		certificate.EABConfigured = false
		return nil
	}
	if certificate.ChallengeType == model.CertificateChallengeHTTP {
		return errors.New("Google Trust Services 的 EAB 目前仅支持面板 DNS-01 或手动 DNS-01")
	}
	if req.GoogleEABCredentialID != nil {
		if *req.GoogleEABCredentialID > 0 {
			id := *req.GoogleEABCredentialID
			certificate.GoogleEABCredentialID = &id
			certificate.EABKeyID = ""
			certificate.EABHMACKeyEncrypted = ""
			certificate.EABConfigured = true
			return nil
		}
		certificate.GoogleEABCredentialID = nil
	}
	if req.EABKeyID != nil || req.EABHMACKey != nil {
		certificate.GoogleEABCredentialID = nil
	}
	if certificate.GoogleEABCredentialID != nil {
		certificate.EABConfigured = true
		return nil
	}

	previousKeyID := certificate.EABKeyID
	if req.EABKeyID != nil {
		if err := validateCertificateEABValue("Key ID", *req.EABKeyID, 512); err != nil {
			return err
		}
		certificate.EABKeyID = *req.EABKeyID
	}
	hmacKeyProvided := req.EABHMACKey != nil && *req.EABHMACKey != ""
	if current != nil && certificate.EABKeyID != previousKeyID && !hmacKeyProvided {
		return errors.New("更换 EAB Key ID 时，请同时填写新的 HMAC Key")
	}
	if hmacKeyProvided {
		if err := validateCertificateEABValue("HMAC Key", *req.EABHMACKey, 2048); err != nil {
			return err
		}
		encrypted, err := security.EncryptSecret(s.sessionSecret, certificateEABHMACKeyPurpose, *req.EABHMACKey)
		if err != nil {
			return fmt.Errorf("保存 EAB HMAC Key: %w", err)
		}
		certificate.EABHMACKeyEncrypted = encrypted
	}
	if certificate.EABKeyID == "" || certificate.EABHMACKeyEncrypted == "" {
		return errors.New("Google Trust Services 需要 EAB，请填写 Key ID 和 HMAC Key")
	}
	certificate.EABConfigured = true
	return nil
}

func validateCertificateEABValue(label, value string, maxLength int) error {
	if value == "" {
		return fmt.Errorf("%s 不能为空", label)
	}
	if len(value) > maxLength {
		return fmt.Errorf("%s 过长，请重新获取后再填写", label)
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%s 格式不正确，请检查是否包含空格或换行", label)
		}
	}
	return nil
}

func defaultCertificateAccountEmail(domain string) string {
	domain = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), "*.")
	if domain == "" {
		return ""
	}
	return "admin@" + domain
}

func normalizeCertificateDomains(domains []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(domains))
	for _, raw := range domains {
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		check := strings.TrimPrefix(domain, "*.")
		if !isDNSDomainName(check) || strings.Contains(check, "*") {
			return nil, fmt.Errorf("invalid certificate domain %q", raw)
		}
		if !seen[domain] {
			seen[domain] = true
			out = append(out, domain)
		}
	}
	return out, nil
}

func (s *Server) validateCertificateReferences(ctx context.Context, certificate model.Certificate) error {
	if certificate.GoogleEABCredentialID != nil {
		if certificate.ACMECA != "google" {
			return errors.New("只有 Google Trust Services 可以使用 Google EAB")
		}
		if _, err := s.store.GetGoogleEABCredential(ctx, *certificate.GoogleEABCredentialID); err != nil {
			return errors.New("选择的 Google EAB 不存在，请重新选择")
		}
	}
	if certificate.DNSCredentialID != nil {
		credential, err := s.store.GetDNSCredential(ctx, *certificate.DNSCredentialID)
		if err != nil || !credential.Enabled {
			return errors.New("DNS credential is unavailable")
		}
		for _, domain := range certificate.Domains {
			if _, err := selectDNSCredentialZone(*credential, domain, 0); err != nil {
				return err
			}
		}
	}
	if certificate.IssuanceServerID != nil {
		if _, err := s.store.GetServer(ctx, *certificate.IssuanceServerID); err != nil {
			return errors.New("issuance server is unavailable")
		}
	}
	return nil
}

func (s *Server) importCertificate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var req struct {
		Name           string `json:"name"`
		CertificatePEM string `json:"certificate_pem"`
		FullchainPEM   string `json:"fullchain_pem"`
		PrivateKeyPEM  string `json:"private_key_pem"`
		AutoRenew      bool   `json:"auto_renew"`
	}
	if !decode(w, r, &req) {
		return
	}
	material, err := validateCertificateMaterial(req.CertificatePEM, req.FullchainPEM, req.PrivateKeyPEM, nil, certificateMaterialPolicy{})
	if err != nil {
		fail(w, err, 400)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = material.Domains[0]
	}
	certificate := &model.Certificate{Name: name, PrimaryDomain: material.Domains[0], Domains: material.Domains, Wildcard: containsWildcard(material.Domains), ChallengeType: "imported", ACMECA: "imported", Status: model.CertificateStatusPending, AutoRenew: req.AutoRenew}
	if err := s.store.CreateCertificate(r.Context(), certificate); err != nil {
		fail(w, err, 500)
		return
	}
	if err := s.storeCertificateMaterial(r.Context(), certificate, req.CertificatePEM, req.FullchainPEM, req.PrivateKeyPEM, certificateMaterialPolicy{}); err != nil {
		_ = s.store.DeleteCertificate(r.Context(), certificate.ID)
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "import", "certificate", strconv.FormatInt(certificate.ID, 10))
	write(w, 201, map[string]any{"certificate": certificate})
}

type parsedCertificateMaterial struct {
	Leaf    *x509.Certificate
	Domains []string
}

// certificateMaterialPolicy constrains how far a certificate submission is
// trusted. Operator imports and Controller-driven ACME runs are trusted
// sources. Material reported by an Agent is not: without these constraints a
// compromised node could substitute self-signed material for the domains it
// serves, or widen the operator-approved domain set and have the poisoned
// primary domain flow back into Controller renewals.
type certificateMaterialPolicy struct {
	// RequireTrustedChain rejects material that does not chain to a publicly
	// trusted root. Agent HTTP-01 issuance always uses an allowlisted public
	// ACME CA, so an unverifiable chain means the node supplied its own.
	RequireTrustedChain bool
	// RequireExactDomains rejects any SAN outside the approved domain set.
	RequireExactDomains bool
}

// untrustedCertificateMaterial is the policy for Agent-reported material.
var untrustedCertificateMaterial = certificateMaterialPolicy{RequireTrustedChain: true, RequireExactDomains: true}

func validateCertificateMaterial(certificatePEM, fullchainPEM, privateKeyPEM string, requestedDomains []string, policy certificateMaterialPolicy) (parsedCertificateMaterial, error) {
	leafPEM := strings.TrimSpace(certificatePEM)
	if leafPEM == "" {
		leafPEM = strings.TrimSpace(fullchainPEM)
	}
	block, _ := pem.Decode([]byte(leafPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return parsedCertificateMaterial{}, errors.New("certificate_pem does not contain an X.509 certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return parsedCertificateMaterial{}, fmt.Errorf("parse certificate: %w", err)
	}
	privateKey, err := parsePrivateKey([]byte(privateKeyPEM))
	if err != nil {
		return parsedCertificateMaterial{}, err
	}
	publicFromKey, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil {
		return parsedCertificateMaterial{}, err
	}
	publicFromCert, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		return parsedCertificateMaterial{}, err
	}
	if !bytes.Equal(publicFromKey, publicFromCert) {
		return parsedCertificateMaterial{}, errors.New("private key does not match certificate")
	}
	domains, err := normalizeCertificateDomains(leaf.DNSNames)
	if err != nil || len(domains) == 0 {
		return parsedCertificateMaterial{}, errors.New("certificate has no valid DNS SAN")
	}
	for _, requested := range requestedDomains {
		if err := leaf.VerifyHostname(strings.TrimPrefix(requested, "*.")); err != nil && !certificateDomainCovered(domains, requested) {
			return parsedCertificateMaterial{}, fmt.Errorf("certificate does not cover %s", requested)
		}
	}
	if policy.RequireExactDomains {
		if err := requireApprovedDomainSet(requestedDomains, domains); err != nil {
			return parsedCertificateMaterial{}, err
		}
	}
	if policy.RequireTrustedChain {
		if err := verifyCertificateChain(leaf, fullchainPEM); err != nil {
			return parsedCertificateMaterial{}, err
		}
	}
	return parsedCertificateMaterial{Leaf: leaf, Domains: domains}, nil
}

// requireApprovedDomainSet rejects a certificate whose SANs differ from the
// operator-approved domain set. Order is irrelevant because the issuing CA
// chooses SAN ordering, so both sides are compared as sets.
func requireApprovedDomainSet(approved, actual []string) error {
	if len(approved) == 0 {
		return errors.New("certificate has no approved domain set")
	}
	allowed := make(map[string]bool, len(approved))
	for _, domain := range approved {
		allowed[domain] = true
	}
	present := make(map[string]bool, len(actual))
	for _, domain := range actual {
		present[domain] = true
		if !allowed[domain] {
			return fmt.Errorf("certificate contains unapproved domain %s", domain)
		}
	}
	for _, domain := range approved {
		if !present[domain] {
			return fmt.Errorf("certificate does not cover %s", domain)
		}
	}
	return nil
}

// verifyCertificateChain validates the leaf against the system trust store,
// using the submitted fullchain only as a source of intermediates.
func verifyCertificateChain(leaf *x509.Certificate, fullchainPEM string) error {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return fmt.Errorf("load system trust store: %w", err)
	}
	intermediates := x509.NewCertPool()
	rest := []byte(fullchainPEM)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		parsed, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("parse chain certificate: %w", err)
		}
		if !parsed.Equal(leaf) {
			intermediates.AddCert(parsed)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return fmt.Errorf("certificate does not chain to a trusted root: %w", err)
	}
	return nil
}

func parsePrivateKey(data []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("private_key_pem does not contain a PEM private key")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if signer, ok := key.(crypto.Signer); ok {
			return signer, nil
		}
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("unsupported private key format")
}

func (s *Server) storeCertificateMaterial(ctx context.Context, certificate *model.Certificate, certificatePEM, fullchainPEM, privateKeyPEM string, policy certificateMaterialPolicy) error {
	material, err := validateCertificateMaterial(certificatePEM, fullchainPEM, privateKeyPEM, certificate.Domains, policy)
	if err != nil {
		return err
	}
	encrypted, err := security.EncryptSecret(s.sessionSecret, "certificate-private-key", privateKeyPEM)
	if err != nil {
		return err
	}
	if strings.TrimSpace(fullchainPEM) == "" {
		fullchainPEM = certificatePEM
	}
	sum := sha256.Sum256([]byte(fullchainPEM + "\x00" + privateKeyPEM))
	now := time.Now().UTC()
	certificate.CertificatePEM = certificatePEM
	certificate.FullchainPEM = fullchainPEM
	certificate.PrivateKeyEncrypted = encrypted
	certificate.Revision = hex.EncodeToString(sum[:])
	certificate.NotBefore = &material.Leaf.NotBefore
	certificate.NotAfter = &material.Leaf.NotAfter
	certificate.Domains = material.Domains
	certificate.PrimaryDomain = material.Domains[0]
	certificate.Wildcard = containsWildcard(material.Domains)
	certificate.Status = model.CertificateStatusReady
	certificate.ValidationRecords = nil
	certificate.LastError = ""
	certificate.LastIssuedAt = &now
	return s.store.UpdateCertificate(ctx, certificate)
}

func containsWildcard(domains []string) bool {
	for _, domain := range domains {
		if strings.HasPrefix(domain, "*.") {
			return true
		}
	}
	return false
}

func (s *Server) ensureCertificateUnused(ctx context.Context, id int64) error {
	bindings, err := s.store.ListInboundCertificateBindings(ctx)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding.CertificateID != nil && *binding.CertificateID == id {
			return fmt.Errorf("certificate is used by inbound %d", binding.InboundID)
		}
	}
	return nil
}

func (s *Server) saveInboundCertificateBinding(ctx context.Context, inbound model.Inbound) error {
	mode := strings.TrimSpace(inbound.CertificateMode)
	if mode == "" {
		mode = model.CertificateModeExternal
	}
	binding := &model.InboundCertificateBinding{InboundID: inbound.ID, CertificateID: inbound.CertificateID, Mode: mode, ServerName: normalizeDomainName(inbound.CertificateDomain)}
	return s.store.UpsertInboundCertificateBinding(ctx, binding)
}

func (s *Server) validateInboundManagedReferences(ctx context.Context, inbound model.Inbound) error {
	if inbound.DNSSyncEnabled && inbound.DNSCredentialID != nil {
		credential, err := s.store.GetDNSCredential(ctx, *inbound.DNSCredentialID)
		if err != nil || !credential.Enabled {
			return errors.New("DNS credential is unavailable")
		}
		if inbound.DNSProxyEnabled && credential.Provider != model.DNSProviderCloudflare {
			return errors.New("DNS proxy is only supported by Cloudflare")
		}
		if _, err := selectDNSCredentialZone(*credential, inbound.DNSDomain, inbound.ServerID); err != nil {
			return err
		}
	}
	if inbound.CertificateMode == model.CertificateModeExplicit && inbound.CertificateID != nil {
		certificate, err := s.store.GetCertificate(ctx, *inbound.CertificateID)
		if err != nil {
			return errors.New("所选证书不可用")
		}
		if !certificateDomainCovered(certificate.Domains, inbound.CertificateDomain) {
			return fmt.Errorf("所选证书不包含 SNI 域名 %s", inbound.CertificateDomain)
		}
	}
	return nil
}

func (s *Server) prepareCertificateInbounds(ctx context.Context, inbounds []model.Inbound, serverID int64) ([]model.Inbound, []model.ManagedAssetReference, error) {
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return nil, nil, err
	}
	autoEnabled := !strings.EqualFold(strings.TrimSpace(settings["certificate_auto_match_enabled"]), "false")
	preference := strings.ToLower(strings.TrimSpace(settings["certificate_default_preference"]))
	if preference != "wildcard" {
		preference = "subdomain"
	}
	certificates, err := s.store.ListCertificates(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := append([]model.Inbound(nil), inbounds...)
	assetsByID := map[int64]model.ManagedAssetReference{}
	for i := range out {
		inbound := &out[i]
		if inbound.ServerID != serverID || !inbound.Enabled || inbound.CertificateMode == "" || inbound.CertificateMode == model.CertificateModeExternal {
			continue
		}
		domain := inboundCertificateDomain(*inbound)
		if !isDNSDomainName(domain) {
			return nil, nil, fmt.Errorf("inbound %d has no valid certificate domain", inbound.ID)
		}
		if inbound.CertificateMode == model.CertificateModeAuto && !autoEnabled {
			return nil, nil, fmt.Errorf("automatic certificate matching is disabled for inbound %d", inbound.ID)
		}
		selectionMode := certificateSelectionMode(inbound.CertificateMode, preference)
		certificate, err := selectCertificate(certificates, selectionMode, inbound.CertificateID, domain, preference, time.Now())
		if err != nil {
			if selectionMode == model.CertificateModeExplicit {
				return nil, nil, fmt.Errorf("inbound %d: %w", inbound.ID, err)
			}
			if issueErr := s.ensureManagedCertificateIssue(ctx, *inbound, selectionMode, domain, certificates); issueErr != nil {
				return nil, nil, fmt.Errorf("inbound %d: %w", inbound.ID, issueErr)
			}
			return nil, nil, fmt.Errorf("inbound %d: %w", inbound.ID, errCertificateProvisioning)
		}
		inbound.CertificateID = &certificate.ID
		inbound.CertificateDomain = domain
		inbound.ConfigJSON, err = injectManagedCertificate(inbound.ConfigJSON, certificate.ID, domain)
		if err != nil {
			return nil, nil, err
		}
		binding := &model.InboundCertificateBinding{InboundID: inbound.ID, CertificateID: &certificate.ID, Mode: inbound.CertificateMode, ServerName: domain}
		if err := s.store.UpsertInboundCertificateBinding(ctx, binding); err != nil {
			return nil, nil, err
		}
		assetsByID[certificate.ID] = model.ManagedAssetReference{Kind: "certificate", ID: certificate.ID, Revision: certificate.Revision}
	}
	assets := make([]model.ManagedAssetReference, 0, len(assetsByID))
	for _, asset := range assetsByID {
		assets = append(assets, asset)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].ID < assets[j].ID })
	return out, assets, nil
}

func certificateSelectionMode(mode, preference string) string {
	if mode != model.CertificateModeAuto {
		return mode
	}
	if preference == "wildcard" {
		return model.CertificateModeWildcard
	}
	return model.CertificateModeExact
}

func certificateIssuanceDomain(mode, domain string) (string, error) {
	domain = normalizeDomainName(domain)
	if mode != model.CertificateModeWildcard {
		return domain, nil
	}
	labelEnd := strings.IndexByte(domain, '.')
	if labelEnd <= 0 || labelEnd == len(domain)-1 {
		return "", fmt.Errorf("cannot derive wildcard certificate domain from %s", domain)
	}
	return "*." + domain[labelEnd+1:], nil
}

func (s *Server) ensureManagedCertificateIssue(ctx context.Context, inbound model.Inbound, selectionMode, domain string, certificates []model.Certificate) error {
	if inbound.DNSCredentialID == nil || *inbound.DNSCredentialID <= 0 {
		return errors.New("自动申请证书需要入口的域名服务账号")
	}
	issuanceDomain, err := certificateIssuanceDomain(selectionMode, domain)
	if err != nil {
		return err
	}
	for i := range certificates {
		certificate := &certificates[i]
		if certificate.ChallengeType != model.CertificateChallengeDNS || certificate.DNSCredentialID == nil || *certificate.DNSCredentialID != *inbound.DNSCredentialID || !containsCertificateDomain(certificate.Domains, issuanceDomain) {
			continue
		}
		switch certificate.Status {
		case model.CertificateStatusPending:
			if err := s.startCertificateIssue(ctx, certificate, false, false); err != nil {
				return err
			}
			return fmt.Errorf("%w: 已开始签发 %s", errCertificateProvisioning, issuanceDomain)
		case model.CertificateStatusIssuing:
			return fmt.Errorf("%w: %s 正在签发", errCertificateProvisioning, issuanceDomain)
		case model.CertificateStatusFailed:
			return fmt.Errorf("证书 %s 自动签发失败：%s", issuanceDomain, certificate.LastError)
		case model.CertificateStatusReady:
			if err := s.startCertificateIssue(ctx, certificate, true, false); err != nil {
				return err
			}
			return fmt.Errorf("%w: 已开始续签 %s", errCertificateProvisioning, issuanceDomain)
		default:
			return fmt.Errorf("%w: %s 当前状态为 %s", errCertificateProvisioning, issuanceDomain, certificate.Status)
		}
	}

	credentialID := *inbound.DNSCredentialID
	request := certificateRequest{
		Name:            "自动证书 " + issuanceDomain,
		Domains:         []string{issuanceDomain},
		ChallengeType:   model.CertificateChallengeDNS,
		DNSCredentialID: &credentialID,
		ACMECA:          "letsencrypt",
	}
	certificate, err := s.buildCertificate(request, nil)
	if err != nil {
		return err
	}
	if err := s.validateCertificateReferences(ctx, *certificate); err != nil {
		return err
	}
	if err := s.store.CreateCertificate(ctx, certificate); err != nil {
		return err
	}
	if err := s.startCertificateIssue(ctx, certificate, false, false); err != nil {
		return err
	}
	return fmt.Errorf("%w: 已自动创建并开始签发 %s", errCertificateProvisioning, issuanceDomain)
}

func containsCertificateDomain(domains []string, want string) bool {
	for _, domain := range domains {
		if strings.EqualFold(strings.TrimSpace(domain), want) {
			return true
		}
	}
	return false
}

func selectCertificate(certificates []model.Certificate, mode string, explicitID *int64, domain, preference string, now time.Time) (model.Certificate, error) {
	var exact, wildcard []model.Certificate
	for _, certificate := range certificates {
		if certificate.Status != model.CertificateStatusReady || certificate.Revision == "" || certificate.NotAfter == nil || !certificate.NotAfter.After(now) {
			continue
		}
		if mode == model.CertificateModeExplicit && explicitID != nil && certificate.ID == *explicitID {
			if !certificateDomainCovered(certificate.Domains, domain) {
				return model.Certificate{}, fmt.Errorf("certificate %d does not cover %s", certificate.ID, domain)
			}
			return certificate, nil
		}
		for _, san := range certificate.Domains {
			if strings.EqualFold(san, domain) {
				exact = append(exact, certificate)
				break
			}
			if wildcardDomainMatches(san, domain) {
				wildcard = append(wildcard, certificate)
				break
			}
		}
	}
	newest := func(items []model.Certificate) (model.Certificate, bool) {
		if len(items) == 0 {
			return model.Certificate{}, false
		}
		sort.Slice(items, func(i, j int) bool { return items[i].NotAfter.After(*items[j].NotAfter) })
		return items[0], true
	}
	switch mode {
	case model.CertificateModeExplicit:
		return model.Certificate{}, errors.New("explicit certificate is not ready")
	case model.CertificateModeExact:
		if certificate, ok := newest(exact); ok {
			return certificate, nil
		}
	case model.CertificateModeWildcard:
		if certificate, ok := newest(wildcard); ok {
			return certificate, nil
		}
	case model.CertificateModeAuto:
		first, second := exact, wildcard
		if preference == "wildcard" {
			first, second = wildcard, exact
		}
		if certificate, ok := newest(first); ok {
			return certificate, nil
		}
		if certificate, ok := newest(second); ok {
			return certificate, nil
		}
	}
	return model.Certificate{}, fmt.Errorf("no ready %s certificate covers %s", mode, domain)
}

func certificateDomainCovered(domains []string, domain string) bool {
	for _, san := range domains {
		if strings.EqualFold(san, domain) || wildcardDomainMatches(san, domain) {
			return true
		}
	}
	return false
}

func wildcardDomainMatches(pattern, domain string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	domain = strings.ToLower(strings.TrimSpace(domain))
	if !strings.HasPrefix(pattern, "*.") {
		return false
	}
	suffix := strings.TrimPrefix(pattern, "*.")
	if !strings.HasSuffix(domain, "."+suffix) {
		return false
	}
	return !strings.Contains(strings.TrimSuffix(domain, "."+suffix), ".")
}

func inboundCertificateDomain(inbound model.Inbound) string {
	if domain := normalizeDomainName(inbound.CertificateDomain); domain != "" {
		return domain
	}
	if domain := normalizeDomainName(inbound.DNSDomain); domain != "" {
		return domain
	}
	var config map[string]any
	if json.Unmarshal([]byte(inbound.ConfigJSON), &config) == nil {
		if tls, ok := config["tls"].(map[string]any); ok {
			if serverName, ok := tls["server_name"].(string); ok {
				return normalizeDomainName(serverName)
			}
		}
	}
	return ""
}

func injectManagedCertificate(configJSON string, certificateID int64, domain string) (string, error) {
	config := map[string]any{}
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return "", err
	}
	tls, _ := config["tls"].(map[string]any)
	if tls == nil {
		tls = map[string]any{}
	}
	tls["enabled"] = true
	tls["server_name"] = domain
	base := "oboard-asset://certificate/" + strconv.FormatInt(certificateID, 10) + "/"
	tls["certificate_path"] = base + "fullchain.pem"
	tls["key_path"] = base + "privkey.pem"
	config["tls"] = tls
	encoded, err := json.Marshal(config)
	return string(encoded), err
}

func (s *Server) issueCertificate(w http.ResponseWriter, r *http.Request, id int64, renew bool) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	certificate, err := s.store.GetCertificate(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if certificate.ChallengeType == "imported" {
		fail(w, errors.New("imported certificates cannot be issued"), 400)
		return
	}
	if err := s.startCertificateIssue(context.WithoutCancel(r.Context()), certificate, renew, false); err != nil {
		fail(w, err, 409)
		return
	}
	auditReq(s, r, "issue", "certificate", strconv.FormatInt(id, 10))
	write(w, 202, map[string]any{"certificate": certificate, "accepted": true})
}

func (s *Server) confirmCertificateDNS(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	certificate, err := s.store.GetCertificate(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if certificate.ChallengeType != model.CertificateChallengeDNSManual || certificate.Status != model.CertificateStatusAwaitingDNS {
		fail(w, errors.New("certificate is not awaiting manual DNS confirmation"), 409)
		return
	}
	if err := s.startCertificateIssue(context.WithoutCancel(r.Context()), certificate, false, true); err != nil {
		fail(w, err, 409)
		return
	}
	auditReq(s, r, "confirm_dns", "certificate", strconv.FormatInt(id, 10))
	write(w, 202, map[string]any{"certificate": certificate, "accepted": true})
}

// startCertificateIssue is implemented in acme.go so matching and storage can
// be tested without invoking an external ACME client.
func (s *Server) startCertificateIssue(ctx context.Context, certificate *model.Certificate, renew, resumeManual bool) error {
	return s.startACMECertificateIssue(ctx, certificate, renew, resumeManual)
}
