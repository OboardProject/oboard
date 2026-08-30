package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

type dnsCredentialRequest struct {
	Name     string                    `json:"name"`
	Provider model.DNSProvider         `json:"provider"`
	ZoneName string                    `json:"zone_name"`
	ZoneID   string                    `json:"zone_id"`
	Zones    []model.DNSCredentialZone `json:"zones"`
	Config   map[string]string         `json:"config"`
	Enabled  *bool                     `json:"enabled"`
}

func (s *Server) dnsCredentials(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.ListDNSCredentials(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"dns_credentials": items})
	case http.MethodPost:
		var req dnsCredentialRequest
		if !decode(w, r, &req) {
			return
		}
		credential, err := s.buildDNSCredential(r.Context(), req, nil)
		if err != nil {
			fail(w, err, 400)
			return
		}
		if err := s.store.CreateDNSCredential(r.Context(), credential); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "dns_credential", strconv.FormatInt(credential.ID, 10))
		write(w, 201, map[string]any{"dns_credential": credential})
	default:
		method(w)
	}
}

func (s *Server) dnsCredentialSubroutes(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/dns-credentials/")
	if len(parts) == 0 {
		fail(w, errors.New("missing DNS credential id"), 400)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		fail(w, errors.New("invalid DNS credential id"), 400)
		return
	}
	if len(parts) == 2 && parts[1] == "verify" {
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		s.verifyDNSCredential(w, r, id)
		return
	}
	if len(parts) != 1 {
		fail(w, errors.New("unknown DNS credential route"), 404)
		return
	}
	current, err := s.store.GetDNSCredential(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	switch r.Method {
	case http.MethodGet:
		write(w, 200, map[string]any{"dns_credential": current})
	case http.MethodPatch:
		var req dnsCredentialRequest
		if !decode(w, r, &req) {
			return
		}
		credential, err := s.buildDNSCredential(r.Context(), req, current)
		if err != nil {
			fail(w, err, 400)
			return
		}
		credential.ID = id
		if err := s.store.UpdateDNSCredential(r.Context(), credential); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "update", "dns_credential", strconv.FormatInt(id, 10))
		write(w, 200, map[string]any{"dns_credential": credential})
	case http.MethodDelete:
		if err := s.ensureDNSCredentialUnused(r.Context(), id); err != nil {
			fail(w, err, http.StatusConflict)
			return
		}
		if err := s.store.DeleteDNSCredential(r.Context(), id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "dns_credential", strconv.FormatInt(id, 10))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) buildDNSCredential(ctx context.Context, req dnsCredentialRequest, current *model.DNSCredential) (*model.DNSCredential, error) {
	credential := &model.DNSCredential{Enabled: true}
	if current != nil {
		*credential = *current
	}
	if strings.TrimSpace(req.Name) != "" || current == nil {
		credential.Name = strings.TrimSpace(req.Name)
	}
	if req.Provider != "" || current == nil {
		if current != nil && req.Provider != "" && req.Provider != current.Provider && req.Config == nil {
			return nil, errors.New("changing DNS provider requires a new config")
		}
		credential.Provider = req.Provider
	}
	if req.Zones != nil {
		credential.Zones = append([]model.DNSCredentialZone(nil), req.Zones...)
	} else if current == nil && strings.TrimSpace(req.ZoneName) != "" {
		credential.Zones = []model.DNSCredentialZone{{ZoneName: req.ZoneName, ProviderZoneID: req.ZoneID}}
	}
	if req.Enabled != nil {
		credential.Enabled = *req.Enabled
	}
	if credential.Name == "" {
		return nil, errors.New("name required")
	}
	if len(credential.Zones) == 0 {
		return nil, errors.New("at least one DNS zone is required")
	}
	seenZones := map[string]bool{}
	for i := range credential.Zones {
		zone := &credential.Zones[i]
		zone.ZoneName = normalizeDomainName(zone.ZoneName)
		zone.ProviderZoneID = strings.TrimSpace(zone.ProviderZoneID)
		if !isDNSDomainName(zone.ZoneName) {
			return nil, fmt.Errorf("invalid DNS zone %q", zone.ZoneName)
		}
		key := zone.ZoneName + ":"
		if zone.ServerID != nil {
			if *zone.ServerID <= 0 {
				return nil, errors.New("invalid DNS zone server")
			}
			if _, err := s.store.GetServer(ctx, *zone.ServerID); err != nil {
				return nil, errors.New("DNS zone server is unavailable")
			}
			key += strconv.FormatInt(*zone.ServerID, 10)
		}
		if seenZones[key] {
			return nil, fmt.Errorf("duplicate DNS zone binding %s", zone.ZoneName)
		}
		seenZones[key] = true
		if credential.Provider == model.DNSProviderTencentESA && zone.ProviderZoneID == "" {
			return nil, fmt.Errorf("tencent ESA zone %s requires provider_zone_id", zone.ZoneName)
		}
	}
	credential.ZoneName = credential.Zones[0].ZoneName
	credential.ZoneID = credential.Zones[0].ProviderZoneID
	if req.Config != nil {
		if err := validateDNSProviderConfig(credential.Provider, req.Config); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(req.Config)
		if err != nil {
			return nil, err
		}
		encrypted, err := security.EncryptSecret(s.sessionSecret, "dns-credential", string(encoded))
		if err != nil {
			return nil, err
		}
		credential.ConfigEncrypted = encrypted
		credential.Configured = true
		credential.VerifiedAt = nil
		credential.LastError = ""
	} else if current == nil {
		return nil, errors.New("config required")
	}
	if err := validateDNSProviderConfigName(credential.Provider); err != nil {
		return nil, err
	}
	return credential, nil
}

func credentialForDNSZone(credential model.DNSCredential, zone model.DNSCredentialZone) model.DNSCredential {
	credential.ZoneName = zone.ZoneName
	credential.ZoneID = zone.ProviderZoneID
	return credential
}

func selectDNSCredentialZone(credential model.DNSCredential, domain string, serverID int64) (*model.DNSCredentialZone, error) {
	domain = normalizeDomainName(strings.TrimPrefix(domain, "*."))
	bestScore := -1
	var best *model.DNSCredentialZone
	for i := range credential.Zones {
		zone := &credential.Zones[i]
		if domain != zone.ZoneName && !strings.HasSuffix(domain, "."+zone.ZoneName) {
			continue
		}
		score := len(zone.ZoneName) * 10
		if zone.ServerID == nil {
			score++
		} else if *zone.ServerID == serverID && serverID > 0 {
			score += 2
		}
		if score > bestScore {
			bestScore, best = score, zone
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no configured DNS zone contains %s", domain)
	}
	return best, nil
}

func validateDNSProviderConfigName(provider model.DNSProvider) error {
	switch provider {
	case model.DNSProviderCloudflare, model.DNSProviderAliDNS, model.DNSProviderTencentDNS, model.DNSProviderTencentESA, model.DNSProviderHuaweiCloud:
		return nil
	default:
		return fmt.Errorf("unsupported DNS provider %q", provider)
	}
}

func (s *Server) verifyDNSCredential(w http.ResponseWriter, r *http.Request, id int64) {
	credential, err := s.store.GetDNSCredential(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	for _, zone := range credential.Zones {
		client, clientErr := s.dnsProviderClient(credentialForDNSZone(*credential, zone))
		if clientErr != nil {
			err = clientErr
			break
		}
		if clientErr = client.Verify(r.Context()); clientErr != nil {
			err = fmt.Errorf("%s: %w", zone.ZoneName, clientErr)
			break
		}
	}
	if err != nil {
		_ = s.store.SetDNSCredentialVerification(r.Context(), id, nil, err.Error())
		fail(w, fmt.Errorf("verify DNS credential: %w", err), 400)
		return
	}
	now := time.Now().UTC()
	if err := s.store.SetDNSCredentialVerification(r.Context(), id, &now, ""); err != nil {
		fail(w, err, 500)
		return
	}
	credential.VerifiedAt = &now
	credential.LastError = ""
	auditReq(s, r, "verify", "dns_credential", strconv.FormatInt(id, 10))
	write(w, 200, map[string]any{"dns_credential": credential})
}

func (s *Server) ensureDNSCredentialUnused(ctx context.Context, id int64) error {
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return err
	}
	for _, inbound := range inbounds {
		if inbound.DNSCredentialID != nil && *inbound.DNSCredentialID == id {
			return fmt.Errorf("DNS credential is used by inbound %d", inbound.ID)
		}
	}
	certificates, err := s.store.ListCertificates(ctx)
	if err != nil {
		return err
	}
	for _, certificate := range certificates {
		if certificate.DNSCredentialID != nil && *certificate.DNSCredentialID == id {
			return fmt.Errorf("DNS credential is used by certificate %d", certificate.ID)
		}
	}
	return nil
}

func (s *Server) dnsRecords(w http.ResponseWriter, r *http.Request) {
	zoneID, err := dnsZoneIDFromRequest(r)
	if err != nil {
		fail(w, err, 400)
		return
	}
	zone, err := s.store.GetDNSCredentialZone(r.Context(), zoneID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	credential, err := s.store.GetDNSCredential(r.Context(), zone.CredentialID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if !credential.Enabled {
		fail(w, errors.New("DNS credential is disabled"), 409)
		return
	}
	scopedCredential := credentialForDNSZone(*credential, *zone)
	client, err := s.dnsProviderClient(scopedCredential)
	if err != nil {
		fail(w, err, 400)
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := client.ListRecords(r.Context())
		if err != nil {
			fail(w, err, 502)
			return
		}
		metadata, err := s.store.ListDNSRecordMetadata(r.Context(), zoneID)
		if err != nil {
			fail(w, err, 500)
			return
		}
		for i := range items {
			items[i].CredentialZoneID = zoneID
			if local, ok := metadata[items[i].ID]; ok {
				items[i].Comment, items[i].ServerID, items[i].InboundID = local.Comment, local.ServerID, local.InboundID
			}
		}
		write(w, 200, map[string]any{"dns_records": items, "dns_zone": zone})
	case http.MethodPost, http.MethodPatch:
		var record model.DNSRecord
		if !decode(w, r, &record) {
			return
		}
		record.CredentialID = credential.ID
		record.CredentialZoneID = zoneID
		record.Comment = normalizeDNSComment(record.Comment)
		if err := validateDNSRecord(scopedCredential, record); err != nil {
			fail(w, err, 400)
			return
		}
		saved, err := client.UpsertRecord(r.Context(), record)
		if err != nil {
			fail(w, err, 502)
			return
		}
		saved.CredentialZoneID, saved.Comment, saved.ServerID, saved.InboundID = zoneID, record.Comment, record.ServerID, record.InboundID
		if err := s.store.UpsertDNSRecordMetadata(r.Context(), zoneID, saved); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "upsert", "dns_record", saved.ID)
		write(w, 200, map[string]any{"dns_record": saved})
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			fail(w, errors.New("DNS record id required"), 400)
			return
		}
		if err := client.DeleteRecord(r.Context(), id); err != nil {
			fail(w, err, 502)
			return
		}
		if err := s.store.DeleteDNSRecordMetadata(r.Context(), zoneID, id); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "delete", "dns_record", id)
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func dnsZoneIDFromRequest(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("dns_zone_id"))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("dns_zone_id required")
	}
	return id, nil
}

func validateDNSRecord(credential model.DNSCredential, record model.DNSRecord) error {
	recordType := strings.ToUpper(strings.TrimSpace(record.Type))
	switch recordType {
	case "A", "AAAA", "CNAME", "TXT":
	default:
		return fmt.Errorf("unsupported DNS record type %q", record.Type)
	}
	if _, err := relativeDNSName(record.Name, credential.ZoneName); err != nil {
		return err
	}
	if strings.TrimSpace(record.Content) == "" {
		return errors.New("DNS record content required")
	}
	if len([]rune(record.Comment)) > 100 {
		return errors.New("DNS record comment must not exceed 100 characters")
	}
	if record.Proxied && credential.Provider != model.DNSProviderCloudflare {
		return errors.New("DNS proxy is only supported by Cloudflare")
	}
	return nil
}

func normalizeDNSComment(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 100 {
		value = string(runes[:100])
	}
	return value
}

func (s *Server) dnsSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var req struct {
		InboundID int64 `json:"inbound_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	servers, err := s.store.ListServers(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	inbounds, err := s.store.ListInbounds(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	if req.InboundID > 0 {
		filtered := inbounds[:0]
		for _, inbound := range inbounds {
			if inbound.ID == req.InboundID {
				filtered = append(filtered, inbound)
			}
		}
		if len(filtered) == 0 {
			fail(w, sql.ErrNoRows, 404)
			return
		}
		inbounds = filtered
	}
	items, err := s.syncDNSInbounds(r.Context(), servers, inbounds)
	if err != nil {
		fail(w, err, 502)
		return
	}
	auditReq(s, r, "sync", "dns", strconv.FormatInt(req.InboundID, 10))
	write(w, 200, map[string]any{"items": items, "success_count": len(items)})
}

type dnsRecordTarget struct {
	Type    string
	Content string
}

type dnsSyncResult struct {
	InboundID int64  `json:"inbound_id"`
	Domain    string `json:"domain"`
	Status    string `json:"status"`
}

func dnsInboundTargets(server model.Server, inbound model.Inbound) ([]dnsRecordTarget, error) {
	target := strings.TrimSpace(inbound.ExternalIP)
	if target != "" {
		if ip := net.ParseIP(strings.Trim(target, "[]")); ip == nil {
			if !isDNSDomainName(target) {
				return nil, errors.New("custom DNS target must be an IP address or domain")
			}
			return nil, errors.New("自定义入口域名无法验证监听家族对应的 A/AAAA；请改用自定义 IP，或关闭该入口的 DNS 同步")
		}
	}
	ipv4, ipv6 := strings.TrimSpace(server.PublicIPv4), core.ServerEntryIPv6(server)
	if target != "" {
		ip := net.ParseIP(strings.Trim(target, "[]"))
		if ip.To4() != nil {
			ipv4, ipv6 = ip.String(), ""
		} else {
			ipv4, ipv6 = "", ip.String()
		}
	}
	mode := strings.ToLower(strings.TrimSpace(inbound.DNSRecordTypes))
	if mode == "" || mode == "auto" {
		switch inbound.EntryIPMode {
		case model.EntryIPModeIPv6:
			mode = "aaaa"
		case model.EntryIPModeIPv4:
			mode = "a"
		default:
			if ipv4 != "" {
				mode = "a"
			} else {
				mode = "aaaa"
			}
		}
	}
	listenIP := core.EffectiveListenIP(server, inbound.ListenIP)
	var out []dnsRecordTarget
	if (mode == "a" || mode == "both") && core.ListenIPSupportsFamily(listenIP, "ipv4") && net.ParseIP(ipv4) != nil && net.ParseIP(ipv4).To4() != nil {
		out = append(out, dnsRecordTarget{Type: "A", Content: ipv4})
	}
	if (mode == "aaaa" || mode == "both") && core.ListenIPSupportsFamily(listenIP, "ipv6") && net.ParseIP(strings.Trim(ipv6, "[]")) != nil && net.ParseIP(strings.Trim(ipv6, "[]")).To4() == nil {
		out = append(out, dnsRecordTarget{Type: "AAAA", Content: strings.Trim(ipv6, "[]")})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("server %d has no address for DNS record mode %s", server.ID, mode)
	}
	return out, nil
}

func (s *Server) syncDNSInbounds(ctx context.Context, servers []model.Server, inbounds []model.Inbound) ([]dnsSyncResult, error) {
	familySplitInbounds, err := s.familySplitInboundIDs(ctx)
	if err != nil {
		return nil, err
	}
	serverByID := make(map[int64]model.Server, len(servers))
	for _, server := range servers {
		serverByID[server.ID] = server
	}
	credentials, err := s.store.ListDNSCredentials(ctx)
	if err != nil {
		return nil, err
	}
	credentialByID := make(map[int64]model.DNSCredential, len(credentials))
	for _, credential := range credentials {
		credentialByID[credential.ID] = credential
	}
	results := make([]dnsSyncResult, 0)
	for _, inbound := range inbounds {
		if !inbound.Enabled || !inbound.DNSSyncEnabled {
			continue
		}
		if familySplitInbounds[inbound.ID] {
			mode := strings.ToLower(strings.TrimSpace(inbound.DNSRecordTypes))
			if mode == "" || mode == "auto" {
				inbound.DNSRecordTypes = "both"
			}
		}
		status, syncErr := s.syncDNSInbound(ctx, serverByID, credentialByID, inbound)
		now := time.Now().UTC()
		if syncErr != nil {
			_ = s.store.UpdateInboundDNSSyncResult(ctx, inbound.ID, "同步失败", syncErr.Error(), nil)
			serverName := ""
			if server, ok := serverByID[inbound.ServerID]; ok {
				serverName = server.Name
			}
			s.notifyDNSSyncFailure(ctx, inbound, serverName, syncErr)
			return results, fmt.Errorf("DNS sync for inbound %d: %w", inbound.ID, syncErr)
		}
		if err := s.store.UpdateInboundDNSSyncResult(ctx, inbound.ID, status, "", &now); err != nil {
			return results, err
		}
		results = append(results, dnsSyncResult{InboundID: inbound.ID, Domain: normalizeDomainName(inbound.DNSDomain), Status: status})
	}
	return results, nil
}

func (s *Server) familySplitInboundIDs(ctx context.Context) (map[int64]bool, error) {
	paths, err := s.store.ListProxyPaths(ctx)
	if err != nil {
		return nil, err
	}
	pathInbound := make(map[int64]int64, len(paths))
	for _, path := range paths {
		pathInbound[path.ID] = path.InboundID
	}
	rules, err := s.store.ListRoutingRules(ctx)
	if err != nil {
		return nil, err
	}
	result := map[int64]bool{}
	for _, rule := range rules {
		if rule.Enabled && rule.Action == model.RouteActionFamilySplit && rule.ProxyPathID != nil {
			if inboundID := pathInbound[*rule.ProxyPathID]; inboundID > 0 {
				result[inboundID] = true
			}
		}
	}
	return result, nil
}

func (s *Server) syncDNSInbound(ctx context.Context, servers map[int64]model.Server, credentials map[int64]model.DNSCredential, inbound model.Inbound) (string, error) {
	if inbound.DNSCredentialID == nil {
		return "", errors.New("DNS credential is not selected")
	}
	credential, ok := credentials[*inbound.DNSCredentialID]
	if !ok || !credential.Enabled {
		return "", errors.New("DNS credential is unavailable")
	}
	if credential.VerifiedAt == nil {
		return "", errors.New("DNS credential is not verified")
	}
	if inbound.DNSProxyEnabled && credential.Provider != model.DNSProviderCloudflare {
		return "", errors.New("DNS proxy is only supported by Cloudflare")
	}
	server, ok := servers[inbound.ServerID]
	if !ok {
		return "", errors.New("inbound server not found")
	}
	targets, err := dnsInboundTargets(server, inbound)
	if err != nil {
		return "", err
	}
	domain := normalizeDomainName(inbound.DNSDomain)
	zone, err := selectDNSCredentialZone(credential, domain, inbound.ServerID)
	if err != nil {
		return "", err
	}
	credential = credentialForDNSZone(credential, *zone)
	client, err := s.dnsProviderClient(credential)
	if err != nil {
		return "", err
	}
	records, err := client.ListRecords(ctx)
	if err != nil {
		return "", err
	}
	metadata, err := s.store.ListDNSRecordMetadata(ctx, zone.ID)
	if err != nil {
		return "", err
	}
	existing := map[string]model.DNSRecord{}
	for _, record := range records {
		if local, ok := metadata[record.ID]; ok {
			record.Comment, record.ServerID, record.InboundID = local.Comment, local.ServerID, local.InboundID
		}
		if normalizeDomainName(record.Name) == domain {
			existing[strings.ToUpper(record.Type)] = record
		}
	}
	desired := map[string]bool{}
	actions := make([]string, 0, len(targets))
	for _, target := range targets {
		desired[target.Type] = true
		record := existing[target.Type]
		record.CredentialID = credential.ID
		record.Name = domain
		record.Type = target.Type
		record.Content = target.Content
		record.TTL = 300
		record.Proxied = inbound.DNSProxyEnabled
		record.CredentialZoneID = zone.ID
		record.Comment = normalizeDNSComment(fmt.Sprintf("OBoard: 入口 %s / 服务器 %s", inbound.Name, server.Name))
		record.ServerID = &inbound.ServerID
		record.InboundID = &inbound.ID
		if old, found := existing[target.Type]; found && strings.EqualFold(strings.TrimSuffix(old.Content, "."), strings.TrimSuffix(target.Content, ".")) && old.Proxied == inbound.DNSProxyEnabled && old.Comment == record.Comment {
			actions = append(actions, target.Type+" 无变化")
			continue
		}
		saved, err := client.UpsertRecord(ctx, record)
		if err != nil {
			return "", err
		}
		saved.CredentialZoneID, saved.Comment, saved.ServerID, saved.InboundID = zone.ID, record.Comment, record.ServerID, record.InboundID
		if err := s.store.UpsertDNSRecordMetadata(ctx, zone.ID, saved); err != nil {
			return "", err
		}
		if record.ID == "" {
			actions = append(actions, target.Type+" 已新建")
		} else {
			actions = append(actions, target.Type+" 已更新")
		}
	}
	for _, recordType := range []string{"A", "AAAA", "CNAME"} {
		if record, found := existing[recordType]; found && !desired[recordType] {
			if err := client.DeleteRecord(ctx, record.ID); err != nil {
				return "", err
			}
			if err := s.store.DeleteDNSRecordMetadata(ctx, zone.ID, record.ID); err != nil {
				return "", err
			}
			actions = append(actions, recordType+" 已删除")
		}
	}
	sort.Strings(actions)
	return strings.Join(actions, "，"), nil
}

func inboundDNSIdentityChanged(current, inbound model.Inbound) bool {
	return normalizeDomainName(current.DNSDomain) != normalizeDomainName(inbound.DNSDomain) ||
		inboundDNSCredentialID(current) != inboundDNSCredentialID(inbound) ||
		current.DNSSyncEnabled != inbound.DNSSyncEnabled ||
		current.DNSRecordTypes != inbound.DNSRecordTypes ||
		current.DNSProxyEnabled != inbound.DNSProxyEnabled ||
		current.EntryIPMode != inbound.EntryIPMode ||
		current.ExternalIP != inbound.ExternalIP ||
		current.ServerID != inbound.ServerID
}

func (s *Server) deleteStaleInboundDNS(ctx context.Context, current *model.Inbound, inbound model.Inbound) error {
	if current == nil || !current.DNSSyncEnabled || (inbound.DNSSyncEnabled && !inboundDNSIdentityChanged(*current, inbound)) {
		return nil
	}
	return s.deleteDNSInboundRecords(ctx, *current)
}

func (s *Server) syncInboundDNSIfChanged(ctx context.Context, current *model.Inbound, inbound model.Inbound) error {
	if current == nil || !inbound.DNSSyncEnabled || inbound.ID <= 0 || !inboundDNSIdentityChanged(*current, inbound) {
		return nil
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return err
	}
	_, err = s.syncDNSInbounds(ctx, servers, []model.Inbound{inbound})
	return err
}

func (s *Server) applyInboundDomainSideEffects(ctx context.Context, current *model.Inbound, inbound *model.Inbound) error {
	if inbound == nil {
		return errors.New("inbound required")
	}
	followInboundCertificateDomain(inbound, current)
	if err := s.clearStaleInboundCertificateBinding(ctx, inbound); err != nil {
		return err
	}
	return s.rematchInboundCertificateIfCovered(ctx, inbound)
}

func (s *Server) deleteDNSInboundRecords(ctx context.Context, inbound model.Inbound) error {
	if !inbound.DNSSyncEnabled || inbound.DNSCredentialID == nil || !isDNSDomainName(inbound.DNSDomain) {
		return nil
	}
	credential, err := s.store.GetDNSCredential(ctx, *inbound.DNSCredentialID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	zone, err := selectDNSCredentialZone(*credential, inbound.DNSDomain, inbound.ServerID)
	if err != nil {
		return err
	}
	client, err := s.dnsProviderClient(credentialForDNSZone(*credential, *zone))
	if err != nil {
		return err
	}
	records, err := client.ListRecords(ctx)
	if err != nil {
		return err
	}
	domain := normalizeDomainName(inbound.DNSDomain)
	for _, record := range records {
		if normalizeDomainName(record.Name) != domain {
			continue
		}
		switch strings.ToUpper(record.Type) {
		case "A", "AAAA", "CNAME":
			if err := client.DeleteRecord(ctx, record.ID); err != nil {
				return err
			}
			if err := s.store.DeleteDNSRecordMetadata(ctx, zone.ID, record.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeDNSZones(raw string) ([]string, error) {
	seen := map[string]bool{}
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		zone := normalizeDomainName(item)
		if !isDNSDomainName(zone) {
			return nil, fmt.Errorf("invalid DNS zone %q", strings.TrimSpace(item))
		}
		seen[zone] = true
	}
	out := make([]string, 0, len(seen))
	for zone := range seen {
		out = append(out, zone)
	}
	sort.Strings(out)
	return out, nil
}

func (s *Server) StartDNSDDNS(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	s.runDNSDDNS(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runDNSDDNS(ctx)
		}
	}
}

func (s *Server) runDNSDDNS(ctx context.Context) {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		log.Printf("dns ddns: list servers: %v", err)
		return
	}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		log.Printf("dns ddns: list inbounds: %v", err)
		return
	}
	now := time.Now()
	for _, inbound := range inbounds {
		if !inbound.Enabled || !inbound.DNSSyncEnabled || !inbound.DDNSEnabled {
			continue
		}
		interval := time.Duration(inbound.DDNSInterval) * time.Second
		if interval < 5*time.Minute {
			interval = 5 * time.Minute
		}
		if inbound.DNSLastSyncedAt != nil && now.Sub(*inbound.DNSLastSyncedAt) < interval {
			continue
		}
		if _, err := s.syncDNSInbounds(ctx, servers, []model.Inbound{inbound}); err != nil {
			log.Printf("dns ddns: inbound=%d error=%v", inbound.ID, err)
		}
	}
}
