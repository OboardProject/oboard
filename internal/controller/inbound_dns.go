package controller

import (
	"context"
	"sort"

	"github.com/OboardProject/oboard/internal/model"
)

const missingDNSCredentialCode = "missing_dns_credential"

// dnsCredentialRef is the non-secret option list returned with
// missing_dns_credential so Web and MCP can render a credential picker.
type dnsCredentialRef struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

type missingDNSCredentialError struct {
	Available []dnsCredentialRef
}

func (e missingDNSCredentialError) Error() string {
	return "启用 DNS 自动解析时需要选择 DNS 凭据"
}

func (e missingDNSCredentialError) Code() string { return missingDNSCredentialCode }

func (e missingDNSCredentialError) ErrorDetails() map[string]any {
	return map[string]any{"available_credentials": e.Available}
}

func dnsCredentialRefs(credentials []model.DNSCredential) []dnsCredentialRef {
	out := make([]dnsCredentialRef, 0, len(credentials))
	for _, credential := range credentials {
		out = append(out, dnsCredentialRef{ID: credential.ID, Name: credential.Name, Provider: string(credential.Provider)})
	}
	return out
}

func enabledDNSCredentials(credentials []model.DNSCredential) []model.DNSCredential {
	out := make([]model.DNSCredential, 0, len(credentials))
	for _, credential := range credentials {
		if credential.Enabled {
			out = append(out, credential)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// defaultDNSCredentialID is the bootstrap default: the sole enabled credential,
// otherwise the lowest-ID verified enabled credential. Zero means the caller
// must choose.
func defaultDNSCredentialID(credentials []model.DNSCredential) int64 {
	enabled := enabledDNSCredentials(credentials)
	if len(enabled) == 1 {
		return enabled[0].ID
	}
	for _, credential := range enabled {
		if credential.VerifiedAt != nil {
			return credential.ID
		}
	}
	return 0
}

func coveringDNSCredentialsFor(credentials []model.DNSCredential, domain string, serverID int64) []model.DNSCredential {
	var verified, enabled []model.DNSCredential
	for _, credential := range enabledDNSCredentials(credentials) {
		if _, err := selectDNSCredentialZone(credential, domain, serverID); err != nil {
			continue
		}
		enabled = append(enabled, credential)
		if credential.VerifiedAt != nil {
			verified = append(verified, credential)
		}
	}
	if len(verified) > 0 {
		return verified
	}
	return enabled
}

func inboundDNSCredentialID(inbound model.Inbound) int64 {
	if inbound.DNSCredentialID == nil {
		return 0
	}
	return *inbound.DNSCredentialID
}

func assignInboundDNSCredential(inbound *model.Inbound, id int64) {
	if inbound == nil || id <= 0 {
		return
	}
	inbound.DNSCredentialID = &id
}

func pickInboundDNSCredential(credentials []model.DNSCredential, domain string, serverID, currentID int64) (int64, []dnsCredentialRef, bool) {
	enabled := enabledDNSCredentials(credentials)
	available := dnsCredentialRefs(enabled)
	if currentID > 0 {
		for _, credential := range enabled {
			if credential.ID == currentID {
				return currentID, available, true
			}
		}
	}
	if len(enabled) == 0 {
		return 0, available, false
	}
	candidates := coveringDNSCredentialsFor(credentials, domain, serverID)
	if len(candidates) == 0 {
		candidates = enabled
	}
	if len(candidates) == 1 {
		return candidates[0].ID, available, true
	}
	defaultID := defaultDNSCredentialID(credentials)
	if defaultID > 0 {
		for _, credential := range candidates {
			if credential.ID == defaultID {
				return credential.ID, available, true
			}
		}
		return defaultID, available, true
	}
	return 0, available, false
}

// resolveInboundDNSCredential fills dns_credential_id when DNS sync is on.
// A single tenant credential is used automatically; multiple credentials use
// the bootstrap default. Remaining ambiguity returns missing_dns_credential
// with available_credentials for a picker.
//
// A managed-certificate inbound without record sync still uses the credential
// for DNS-01 issuance, but it may legitimately have none when the operator
// already holds a covering certificate. That pass therefore only fills an
// unambiguous credential and never fails the request.
func (s *Server) resolveInboundDNSCredential(ctx context.Context, inbound *model.Inbound) error {
	if inbound == nil {
		return nil
	}
	issuanceOnly := !inbound.DNSSyncEnabled
	if issuanceOnly && !inboundUsesManagedCertificate(*inbound) {
		return nil
	}
	credentials, err := s.store.ListDNSCredentials(ctx)
	if err != nil {
		return err
	}
	domain := inbound.DNSDomain
	if issuanceOnly {
		domain = inboundCertificateDomain(*inbound)
	}
	id, available, ok := pickInboundDNSCredential(credentials, domain, inbound.ServerID, inboundDNSCredentialID(*inbound))
	if !ok {
		if issuanceOnly {
			return nil
		}
		return missingDNSCredentialError{Available: available}
	}
	assignInboundDNSCredential(inbound, id)
	return nil
}

func (s *Server) dnsBootstrapContext(ctx context.Context) map[string]any {
	credentials, err := s.store.ListDNSCredentials(ctx)
	if err != nil {
		return map[string]any{"default_dns_credential_id": nil, "available_credentials": []dnsCredentialRef{}}
	}
	enabled := enabledDNSCredentials(credentials)
	var defaultID any
	if id := defaultDNSCredentialID(credentials); id > 0 {
		defaultID = id
	}
	return map[string]any{
		"default_dns_credential_id": defaultID,
		"available_credentials":     dnsCredentialRefs(enabled),
	}
}
