package core

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

type DNSConfigState struct {
	Policy        *model.ServerDNSPolicy
	EncryptedList *model.DNSList
	BootstrapList *model.DNSList
}

func DNSConfigStateForServer(serverID int64, lists []model.DNSList, policies []model.ServerDNSPolicy) (*DNSConfigState, error) {
	var policy *model.ServerDNSPolicy
	for i := range policies {
		if policies[i].ServerID == serverID {
			policy = &policies[i]
			break
		}
	}
	if policy == nil {
		return nil, fmt.Errorf("server %d has no dns policy", serverID)
	}
	state := &DNSConfigState{Policy: policy}
	for i := range lists {
		switch lists[i].ID {
		case policy.EncryptedListID:
			state.EncryptedList = &lists[i]
		case policy.BootstrapListID:
			state.BootstrapList = &lists[i]
		}
	}
	if state.EncryptedList == nil || state.BootstrapList == nil {
		return nil, fmt.Errorf("server %d dns policy references a missing list", serverID)
	}
	return state, nil
}

func BuildDNSConfig(server model.Server, state *DNSConfigState) (map[string]any, error) {
	if state == nil || state.Policy == nil || state.EncryptedList == nil || state.BootstrapList == nil {
		state = defaultDNSConfigState(server.ID)
	}
	if err := ValidateDNSList(*state.EncryptedList); err != nil {
		return nil, fmt.Errorf("encrypted dns list: %w", err)
	}
	if err := ValidateDNSList(*state.BootstrapList); err != nil {
		return nil, fmt.Errorf("bootstrap dns list: %w", err)
	}
	if state.EncryptedList.Kind != model.DNSListEncrypted || state.BootstrapList.Kind != model.DNSListBootstrap {
		return nil, errors.New("dns policy list kinds do not match")
	}

	encrypted := selectedOrDraft(state.Policy.EncryptedSelected, state.Policy.EncryptedSelectionRevision, *state.EncryptedList)
	bootstrap := selectedOrDraft(state.Policy.BootstrapSelected, state.Policy.BootstrapSelectionRevision, *state.BootstrapList)
	if len(encrypted) == 0 || len(bootstrap) == 0 {
		return nil, errors.New("dns policy has no usable draft candidates")
	}

	servers := make([]map[string]any, 0, len(encrypted)+len(bootstrap)+1)
	for i, candidate := range encrypted {
		item := candidateToSingBoxDNS(candidate)
		item["tag"] = []string{"remote-primary", "remote-secondary"}[i]
		if hostNeedsResolver(candidate.Server) {
			item["domain_resolver"] = "bootstrap-primary"
		}
		servers = append(servers, item)
	}
	for i, candidate := range bootstrap {
		item := candidateToSingBoxDNS(candidate)
		item["tag"] = []string{"bootstrap-primary", "bootstrap-secondary"}[i]
		servers = append(servers, item)
	}
	servers = append(servers, map[string]any{"type": "local", "tag": "local"})
	return map[string]any{
		"servers":  servers,
		"final":    "remote-primary",
		"strategy": normalizeDNSStrategy(state.Policy.Strategy, server.IPStack),
	}, nil
}

func selectedOrDraft(selected []model.DNSCandidate, selectedRevision int64, list model.DNSList) []model.DNSCandidate {
	items := list.Candidates
	if selectedRevision == list.Revision && len(selected) > 0 {
		items = selected
	}
	if len(items) > 2 {
		items = items[:2]
	}
	out := append([]model.DNSCandidate(nil), items...)
	for i := range out {
		normalizeCandidate(&out[i])
	}
	return out
}

func DNSBenchmarkPlanForPolicy(version int64, policy model.ServerDNSPolicy, encrypted, bootstrap model.DNSList, mode model.DNSAutoTestMode, requestID string) (*model.DNSBenchmarkPlan, error) {
	if policy.EncryptedListID != encrypted.ID || policy.BootstrapListID != bootstrap.ID {
		return nil, errors.New("dns benchmark lists do not match policy")
	}
	if err := ValidateDNSList(encrypted); err != nil {
		return nil, fmt.Errorf("encrypted dns list: %w", err)
	}
	if err := ValidateDNSList(bootstrap); err != nil {
		return nil, fmt.Errorf("bootstrap dns list: %w", err)
	}
	if mode == "" {
		mode = policy.AutoTest
	}
	interval := policy.TestIntervalSeconds
	if interval == 0 {
		interval = 3600
	}
	return &model.DNSBenchmarkPlan{
		Version:               version,
		ServerID:              policy.ServerID,
		PolicyRevision:        policy.Revision,
		EncryptedListID:       encrypted.ID,
		EncryptedListRevision: encrypted.Revision,
		BootstrapListID:       bootstrap.ID,
		BootstrapListRevision: bootstrap.Revision,
		Mode:                  mode,
		IntervalSeconds:       interval,
		RequestID:             requestID,
		EncryptedCandidates:   append([]model.DNSCandidate(nil), encrypted.Candidates...),
		BootstrapCandidates:   append([]model.DNSCandidate(nil), bootstrap.Candidates...),
	}, nil
}

func ValidateDNSList(v model.DNSList) error {
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("dns list name is required")
	}
	if v.Kind != model.DNSListEncrypted && v.Kind != model.DNSListBootstrap {
		return fmt.Errorf("unsupported dns list kind %q", v.Kind)
	}
	if len(v.Candidates) < 2 || len(v.Candidates) > 32 {
		return errors.New("dns list must contain between 2 and 32 candidates")
	}
	seen := map[string]bool{}
	for i, candidate := range v.Candidates {
		normalizeCandidate(&candidate)
		if candidate.Tag == "" || seen[candidate.Tag] {
			return fmt.Errorf("candidate[%d]: dns tag must be non-empty and unique", i)
		}
		seen[candidate.Tag] = true
		if err := ValidateDNSCandidate(candidate); err != nil {
			return fmt.Errorf("candidate[%d]: %w", i, err)
		}
		if v.Kind == model.DNSListEncrypted {
			switch candidate.Transport {
			case model.DNSTransportDoH, model.DNSTransportDoT, model.DNSTransportDoQ:
			default:
				return fmt.Errorf("candidate[%d]: encrypted list only supports doh, dot, or doq", i)
			}
		} else {
			switch candidate.Transport {
			case model.DNSTransportUDP, model.DNSTransportTCP:
			default:
				return fmt.Errorf("candidate[%d]: bootstrap list only supports udp or tcp", i)
			}
			if net.ParseIP(strings.Trim(candidate.Server, "[]")) == nil {
				return fmt.Errorf("candidate[%d]: bootstrap server must be a public IP literal", i)
			}
		}
	}
	return nil
}

func ValidateDNSTransport(v model.DNSTransport) error {
	switch v {
	case model.DNSTransportUDP, model.DNSTransportTCP, model.DNSTransportDoT, model.DNSTransportDoH, model.DNSTransportDoQ:
		return nil
	default:
		return fmt.Errorf("unsupported dns transport %q", v)
	}
}

func ValidateDNSAutoTest(v model.DNSAutoTestMode) error {
	switch v {
	case model.DNSAutoTestNever, model.DNSAutoTestFirstApply, model.DNSAutoTestPeriodic, model.DNSAutoTestAlways:
		return nil
	default:
		return fmt.Errorf("unsupported dns auto_test %q", v)
	}
}

func ValidateDNSCandidate(c model.DNSCandidate) error {
	if err := ValidateDNSTransport(c.Transport); err != nil {
		return err
	}
	if err := ValidateSafeHost(c.Server); err != nil {
		return fmt.Errorf("dns server: %w", err)
	}
	if err := rejectPrivateDNSHost(c.Server); err != nil {
		return err
	}
	if c.Port != 0 {
		if err := ValidatePort(c.Port); err != nil {
			return fmt.Errorf("dns port: %w", err)
		}
	}
	if c.Path != "" {
		if !strings.HasPrefix(c.Path, "/") || strings.Contains(c.Path, "://") || strings.Contains(c.Path, "@") || len(c.Path) > 256 {
			return errors.New("dns path is invalid")
		}
		for _, r := range c.Path {
			if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
				return errors.New("dns path is invalid")
			}
		}
	}
	if c.TLSName != "" {
		if err := ValidateSafeHost(c.TLSName); err != nil {
			return fmt.Errorf("dns tls_name: %w", err)
		}
	}
	if len(c.Tag) > 64 || strings.ContainsAny(c.Tag, " \t\n\r") {
		return errors.New("dns tag is invalid")
	}
	return nil
}

func ValidateDNSCandidates(items []model.DNSCandidate) error {
	if len(items) > 32 {
		return errors.New("too many dns benchmark candidates")
	}
	for i, candidate := range items {
		normalizeCandidate(&candidate)
		if err := ValidateDNSCandidate(candidate); err != nil {
			return fmt.Errorf("candidate[%d]: %w", i, err)
		}
	}
	return nil
}

func rejectPrivateDNSHost(host string) error {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return errors.New("dns server must be a public address")
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 169 && ip4[1] == 254 || ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return errors.New("dns server must be a public address")
		}
	}
	return nil
}

func ValidateIPStack(v model.IPStack) error {
	switch v {
	case "", model.IPStackAuto, model.IPStackIPv4Only, model.IPStackIPv6Only, model.IPStackDualStack, model.IPStackPreferIPv4, model.IPStackPreferIPv6:
		return nil
	default:
		return fmt.Errorf("unsupported ip_stack %q", v)
	}
}

func ValidateUDPInboundMode(v model.UDPInboundMode) error {
	switch v {
	case "", model.UDPInboundAllow, model.UDPInboundBlock, model.UDPInboundUoT:
		return nil
	default:
		return fmt.Errorf("unsupported udp_inbound_mode %q", v)
	}
}

func normalizeDNSStrategy(strategy string, stack model.IPStack) string {
	if strategy != "" && strategy != "auto" {
		return strategy
	}
	switch stack {
	case model.IPStackIPv4Only:
		return "ipv4_only"
	case model.IPStackIPv6Only:
		return "ipv6_only"
	case model.IPStackPreferIPv6:
		return "prefer_ipv6"
	default:
		return "prefer_ipv4"
	}
}

func DefaultBootstrapForIPStack(stack model.IPStack) string {
	if stack == model.IPStackIPv6Only || stack == model.IPStackPreferIPv6 {
		return "2606:4700:4700::1111"
	}
	return "1.1.1.1"
}

func candidateToSingBoxDNS(c model.DNSCandidate) map[string]any {
	normalizeCandidate(&c)
	typeName := string(c.Transport)
	switch c.Transport {
	case model.DNSTransportDoT:
		typeName = "tls"
	case model.DNSTransportDoH:
		typeName = "https"
	case model.DNSTransportDoQ:
		typeName = "quic"
	}
	item := map[string]any{"type": typeName, "server": c.Server, "server_port": c.Port}
	if c.Transport == model.DNSTransportDoH {
		item["path"] = c.Path
	}
	if c.Transport == model.DNSTransportDoT || c.Transport == model.DNSTransportDoH || c.Transport == model.DNSTransportDoQ {
		tls := map[string]any{}
		if c.TLSName != "" {
			tls["server_name"] = c.TLSName
		}
		item["tls"] = tls
	}
	return item
}

func normalizeCandidate(c *model.DNSCandidate) {
	if c.Port == 0 {
		c.Port = defaultDNSPort(c.Transport)
	}
	if c.Path == "" && c.Transport == model.DNSTransportDoH {
		c.Path = "/dns-query"
	}
	if c.TLSName == "" && (c.Transport == model.DNSTransportDoT || c.Transport == model.DNSTransportDoH || c.Transport == model.DNSTransportDoQ) && hostNeedsResolver(c.Server) {
		c.TLSName = c.Server
	}
}

func defaultDNSPort(transport model.DNSTransport) int {
	switch transport {
	case model.DNSTransportDoT, model.DNSTransportDoQ:
		return 853
	case model.DNSTransportDoH:
		return 443
	default:
		return 53
	}
}

func hostNeedsResolver(host string) bool {
	return host != "" && net.ParseIP(strings.Trim(host, "[]")) == nil
}

func defaultDNSConfigState(serverID int64) *DNSConfigState {
	return &DNSConfigState{
		Policy: &model.ServerDNSPolicy{ServerID: serverID, Strategy: "auto"},
		EncryptedList: &model.DNSList{Name: "default encrypted", Kind: model.DNSListEncrypted, Revision: 1, Candidates: []model.DNSCandidate{
			{Tag: "cloudflare-doh", Transport: model.DNSTransportDoH, Server: "cloudflare-dns.com", Port: 443, Path: "/dns-query", TLSName: "cloudflare-dns.com"},
			{Tag: "google-dot", Transport: model.DNSTransportDoT, Server: "dns.google", Port: 853, TLSName: "dns.google"},
		}},
		BootstrapList: &model.DNSList{Name: "default bootstrap", Kind: model.DNSListBootstrap, Revision: 1, Candidates: []model.DNSCandidate{
			{Tag: "cloudflare-udp", Transport: model.DNSTransportUDP, Server: "1.1.1.1", Port: 53},
			{Tag: "google-tcp", Transport: model.DNSTransportTCP, Server: "8.8.8.8", Port: 53},
		}},
	}
}
