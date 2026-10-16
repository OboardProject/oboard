package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type probeDDNSInput struct {
	ID               int64  `json:"id,omitempty"`
	ServerID         int64  `json:"server_id"`
	Revision         int64  `json:"revision,omitempty"`
	Name             string `json:"name,omitempty"`
	DNSCredentialID  *int64 `json:"dns_credential_id,omitempty"`
	Domain           string `json:"domain,omitempty"`
	RecordTypes      string `json:"record_types,omitempty"`
	ProxyEnabled     bool   `json:"proxy_enabled"`
	IntervalSeconds  int    `json:"interval_seconds,omitempty"`
	Enabled          bool   `json:"enabled"`
	InterfaceName    string `json:"interface_name,omitempty"`
	InterfacePrefix  string `json:"interface_prefix,omitempty"`
	InterfaceAddress string `json:"interface_address,omitempty"`
	AddressFamily    string `json:"address_family,omitempty"`
	ExcludePrivate   bool   `json:"exclude_private"`
}

func (v probeDDNSInput) model() model.ProbeDDNSRecord {
	return model.ProbeDDNSRecord{
		ID: v.ID, ServerID: v.ServerID, Revision: v.Revision, Name: v.Name, DNSCredentialID: v.DNSCredentialID,
		Domain: v.Domain, RecordTypes: v.RecordTypes, ProxyEnabled: v.ProxyEnabled, IntervalSeconds: v.IntervalSeconds,
		Enabled: v.Enabled, InterfaceName: v.InterfaceName, InterfaceAddress: v.InterfaceAddress, InterfacePrefix: v.InterfacePrefix,
		AddressFamily: v.AddressFamily, ExcludePrivate: v.ExcludePrivate,
	}
}

func (s *Server) probeDDNSBoundary(ctx context.Context, p application.Principal, input probeDDNSInput) (*model.ProbeDDNSRecord, *model.Server, error) {
	if input.ServerID <= 0 || !p.AllowsInt64("server_ids", input.ServerID) {
		return nil, nil, errors.New("服务器超出授权范围")
	}
	server, err := s.store.GetServer(ctx, input.ServerID)
	if err != nil {
		return nil, nil, err
	}
	if input.ID == 0 {
		return nil, server, nil
	}
	record, err := s.store.GetProbeDDNS(ctx, input.ID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && record.ServerID != input.ServerID) {
		return nil, nil, errors.New("DDNS 记录不存在")
	}
	if err != nil {
		return nil, nil, err
	}
	return &record, server, nil
}

func (s *Server) queryProbeDDNS(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
	if err := strictAutomationInput(raw, &struct{}{}); err != nil {
		return nil, err
	}
	records, err := s.store.ListProbeDDNS(ctx)
	if err != nil {
		return nil, err
	}
	visible := []model.ProbeDDNSRecord{}
	for _, record := range records {
		if p.AllowsInt64("server_ids", record.ServerID) {
			visible = append(visible, record)
		}
	}
	return map[string]any{"records": visible}, nil
}

func (s *Server) registerProbeDDNSOperations() {
	for _, action := range []string{"save", "delete", "run"} {
		name := "probe_ddns." + action
		validate := func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
			var input probeDDNSInput
			if err := strictAutomationInput(raw, &input); err != nil {
				return nil, err
			}
			current, server, err := s.probeDDNSBoundary(ctx, p, input)
			if err != nil {
				return nil, err
			}
			if action != "save" && current == nil {
				return nil, errors.New("DDNS 记录不存在")
			}
			if current != nil && input.Revision != current.Revision {
				return nil, errors.New("DDNS 记录已改变，请刷新")
			}
			if action == "save" {
				record := input.model()
				if err = s.prepareProbeDDNS(ctx, *server, &record); err != nil {
					return nil, err
				}
			}
			return map[string]any{"server_id": input.ServerID, "id": input.ID}, nil
		}
		s.automation.RegisterValidator(name, validate)
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, p application.Principal, raw json.RawMessage) (map[string]string, error) {
			var input probeDDNSInput
			if err := strictAutomationInput(raw, &input); err != nil {
				return nil, err
			}
			record, server, err := s.probeDDNSBoundary(ctx, p, input)
			if err != nil {
				return nil, err
			}
			refs := map[string]string{"probe_server:" + strconv.FormatInt(server.ID, 10): server.CreatedAt.UTC().Format(time.RFC3339Nano) + ":" + server.AgentID}
			if record != nil {
				refs["probe_ddns:"+strconv.FormatInt(record.ID, 10)] = strconv.FormatInt(record.Revision, 10)
			}
			return refs, nil
		})
		s.automation.Register(name, func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
			s.dnsDDNSMu.Lock()
			defer s.dnsDDNSMu.Unlock()
			if _, err := validate(ctx, p, raw); err != nil {
				return nil, err
			}
			var input probeDDNSInput
			_ = json.Unmarshal(raw, &input)
			current, server, err := s.probeDDNSBoundary(ctx, p, input)
			if err != nil {
				return nil, err
			}
			if approved, ok := automation.ApprovedResourceRevision(ctx, "probe_server:"+strconv.FormatInt(server.ID, 10)); ok && approved != server.CreatedAt.UTC().Format(time.RFC3339Nano)+":"+server.AgentID {
				return nil, errors.New("服务器身份已改变")
			}
			if action == "save" {
				record := input.model()
				if err = s.prepareProbeDDNS(ctx, *server, &record); err != nil {
					return nil, err
				}
				if current != nil && (current.Domain != record.Domain || credentialID(current.DNSCredentialID) != credentialID(record.DNSCredentialID)) {
					if err = s.deleteProbeDDNSNames(ctx, *current); err != nil {
						return nil, err
					}
				}
				if err = s.store.SaveProbeDDNS(ctx, *server, &record); err != nil {
					return nil, err
				}
				return map[string]any{"id": record.ID, "revision": record.Revision}, nil
			}
			if action == "delete" && current != nil {
				if err = s.deleteProbeDDNSNames(ctx, *current); err != nil {
					return nil, err
				}
				err = s.store.DeleteProbeDDNS(ctx, input.ID, input.Revision)
				return map[string]any{"id": input.ID, "revision": input.Revision}, err
			}
			err = s.store.TouchProbeDDNS(ctx, input.ID, input.Revision)
			return map[string]any{"id": input.ID, "revision": input.Revision}, err
		})
	}
}

func credentialID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

func (s *Server) prepareProbeDDNS(ctx context.Context, server model.Server, record *model.ProbeDDNSRecord) error {
	if err := store.ValidateProbeDDNS(record); err != nil {
		return err
	}
	credential, err := s.store.GetDNSCredential(ctx, *record.DNSCredentialID)
	if errors.Is(err, sql.ErrNoRows) || credential == nil || !credential.Enabled {
		return errors.New("域名服务账号不可用")
	}
	if err != nil {
		return err
	}
	if credential.VerifiedAt == nil {
		return errors.New("域名服务账号尚未验证")
	}
	if record.ProxyEnabled && credential.Provider != model.DNSProviderCloudflare {
		return errors.New("代理访问只适用于 Cloudflare")
	}
	if !record.InterfaceBound() {
		return nil
	}
	interfaces, observed, err := s.probeDDNSInventory(ctx, server.ID)
	if err != nil {
		return err
	}
	_, err = core.ResolveProbeDDNSTargets(core.ProbeDDNSAddressRequest{
		Record: *record, Server: server, Interfaces: interfaces, InventoryAt: observed, Now: time.Now().UTC(),
	})
	return err
}

func (s *Server) probeDDNSInventory(ctx context.Context, serverID int64) ([]model.NetworkInterfaceInfo, time.Time, error) {
	task, err := s.store.LastSuccessfulTaskByServerType(ctx, serverID, model.AgentTaskTypeListNetworkInterfaces)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	var result struct {
		Interfaces []model.NetworkInterfaceInfo `json:"interfaces"`
	}
	if err := json.Unmarshal([]byte(task.ResultJSON), &result); err != nil {
		return nil, time.Time{}, errors.New("网卡读取结果无法识别，请重新读取")
	}
	observed := task.UpdatedAt
	if task.CompletedAt != nil && !task.CompletedAt.IsZero() {
		observed = task.CompletedAt.UTC()
	}
	return result.Interfaces, observed, nil
}

func (s *Server) deleteProbeDDNSNames(ctx context.Context, record model.ProbeDDNSRecord) error {
	if record.DNSCredentialID == nil || strings.TrimSpace(record.Domain) == "" {
		return nil
	}
	credential, err := s.store.GetDNSCredential(ctx, *record.DNSCredentialID)
	if err != nil {
		return err
	}
	zone, err := selectDNSCredentialZone(*credential, record.Domain, record.ServerID)
	if err != nil {
		return err
	}
	client, err := s.dnsProviderClient(credentialForDNSZone(*credential, *zone))
	if err != nil {
		return err
	}
	_, err = s.publishDNSTargets(ctx, client, credential.ID, *zone, normalizeDomainName(record.Domain), false, "", &record.ServerID, nil, nil, record.ID)
	return err
}

func (s *Server) syncProbeDDNS(ctx context.Context, record model.ProbeDDNSRecord) error {
	server, err := s.store.GetServer(ctx, record.ServerID)
	if err != nil {
		return s.failProbeDDNS(ctx, record, nil, err)
	}
	if record.InterfaceBound() {
		if err := s.refreshProbeDDNSInventory(ctx, *server); err != nil {
			return s.failProbeDDNS(ctx, record, server, err)
		}
	}
	targets, err := s.probeDDNSPublishTargets(ctx, *server, record)
	if err != nil {
		return s.failProbeDDNS(ctx, record, server, err)
	}
	status, err := s.publishProbeDDNS(ctx, *server, record, targets)
	if err != nil {
		return s.failProbeDDNS(ctx, record, server, err)
	}
	now := time.Now().UTC()
	if err := s.store.FinishProbeDDNS(ctx, record, "ok", "", probeDDNSAddressText(targets), &now); err != nil {
		return err
	}
	if status != "" {
		return nil
	}
	return nil
}

func (s *Server) failProbeDDNS(ctx context.Context, record model.ProbeDDNSRecord, server *model.Server, syncErr error) error {
	_ = s.store.FinishProbeDDNS(ctx, record, "failed", syncErr.Error(), record.CurrentAddress, record.LastSyncedAt)
	name := ""
	if server != nil {
		name = server.Name
	}
	s.notifyProbeDDNSFailure(ctx, record, name, syncErr)
	return syncErr
}

func (s *Server) probeDDNSPublishTargets(ctx context.Context, server model.Server, record model.ProbeDDNSRecord) ([]dnsRecordTarget, error) {
	req := core.ProbeDDNSAddressRequest{Record: record, Server: server, Now: time.Now().UTC()}
	if record.InterfaceBound() {
		interfaces, observed, err := s.probeDDNSInventory(ctx, server.ID)
		if err != nil {
			return nil, err
		}
		req.Interfaces = interfaces
		req.InventoryAt = observed
	}
	resolved, err := core.ResolveProbeDDNSTargets(req)
	if err != nil {
		return nil, err
	}
	out := make([]dnsRecordTarget, len(resolved))
	for i, target := range resolved {
		out[i] = dnsRecordTarget{Type: target.Type, Content: target.Content}
	}
	return out, nil
}

func (s *Server) publishProbeDDNS(ctx context.Context, server model.Server, record model.ProbeDDNSRecord, targets []dnsRecordTarget) (string, error) {
	if record.DNSCredentialID == nil {
		return "", errors.New("域名服务账号不可用")
	}
	credential, err := s.store.GetDNSCredential(ctx, *record.DNSCredentialID)
	if err != nil || credential == nil || !credential.Enabled {
		return "", errors.New("域名服务账号不可用")
	}
	if credential.VerifiedAt == nil {
		return "", errors.New("域名服务账号尚未验证")
	}
	domain := normalizeDomainName(record.Domain)
	zone, err := selectDNSCredentialZone(*credential, domain, record.ServerID)
	if err != nil {
		return "", err
	}
	client, err := s.dnsProviderClient(credentialForDNSZone(*credential, *zone))
	if err != nil {
		return "", err
	}
	comment := normalizeDNSComment(fmt.Sprintf("OBoard: DDNS #%d %s / 服务器 %s", record.ID, record.Name, server.Name))
	return s.publishDNSTargets(ctx, client, credential.ID, *zone, domain, record.ProxyEnabled, comment, &record.ServerID, nil, targets, record.ID)
}

func probeDDNSAddressText(targets []dnsRecordTarget) string {
	parts := make([]string, len(targets))
	for i, target := range targets {
		parts[i] = target.Type + " " + target.Content
	}
	return strings.Join(parts, "，")
}

func (s *Server) notifyProbeDDNSFailure(ctx context.Context, record model.ProbeDDNSRecord, serverName string, syncErr error) {
	lastSuccess := "never"
	if record.LastSyncedAt != nil {
		lastSuccess = record.LastSyncedAt.UTC().Format(time.RFC3339Nano)
	}
	errorText := "域名记录未能更新"
	if syncErr != nil && strings.TrimSpace(syncErr.Error()) != "" {
		errorText = notificationDNSErrorText(syncErr.Error())
	}
	if strings.TrimSpace(serverName) == "" {
		serverName = fmt.Sprintf("服务器 #%d", record.ServerID)
	}
	s.enqueueNotificationEvent(ctx, notificationEvent{
		Name:    notificationDNSSyncFailed,
		Key:     fmt.Sprintf("ddns:%d:%s:%s", record.ID, lastSuccess, notificationValueKey(normalizeDomainName(record.Domain))),
		Context: notificationContext{ServerID: record.ServerID},
		Data: map[string]string{
			"InboundName": record.Name,
			"Domain":      normalizeDomainName(record.Domain),
			"ServerName":  serverName,
			"Error":       notificationErrorText(errorText),
			"Time":        s.notificationNow(ctx),
		},
	})
}

func (s *Server) validateInboundDDNSRecord(ctx context.Context, inbound model.Inbound) error {
	if inbound.DDNSRecordID == nil || *inbound.DDNSRecordID <= 0 {
		return nil
	}
	record, err := s.store.GetProbeDDNS(ctx, *inbound.DDNSRecordID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("所选 DDNS 记录不存在")
	}
	if err != nil {
		return err
	}
	if record.ServerID != inbound.ServerID {
		return errors.New("DDNS 记录必须属于该入口的服务器")
	}
	return nil
}

func (s *Server) apiProbeDDNS(w http.ResponseWriter, r *http.Request) {
	p, _ := apiPrincipal(r)
	if r.Method == http.MethodGet {
		if _, ok := s.capabilities.Authorize(p, "probe_ddns.list"); !ok {
			v2Error(w, r, 403, "capability_denied", "没有读取权限")
			return
		}
		value, err := s.queryProbeDDNS(r.Context(), p, json.RawMessage(`{}`))
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, 200, value, nil)
		return
	}
	if r.Method != http.MethodPost {
		v2Error(w, r, 405, "method_not_allowed", "不支持该方法")
		return
	}
	if !p.Interactive {
		v2Error(w, r, 403, "changeset_required", "请通过能力与 Changeset 修改 DDNS 记录")
		return
	}
	var req struct {
		Action    string          `json:"action"`
		RequestID string          `json:"request_id"`
		Input     json.RawMessage `json:"input"`
	}
	if !decodeV2(w, r, &req) {
		return
	}
	if (req.Action != "save" && req.Action != "run" && req.Action != "delete") || len(req.RequestID) < 8 || len(req.RequestID) > 80 {
		v2Error(w, r, 400, "invalid_input", "操作或请求标识无效")
		return
	}
	name := "probe_ddns." + req.Action
	if _, ok := s.capabilities.Authorize(p, name); !ok {
		v2Error(w, r, 403, "capability_denied", "没有管理权限")
		return
	}
	result, err := s.applyConfirmedChangeset(r.Context(), p, []automation.OperationRequest{{Capability: name, Input: req.Input, ResourceRefs: json.RawMessage(`{}`)}}, fmt.Sprintf("probe-ddns:%s", req.RequestID), "拨测 DDNS")
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	if result == nil || result.Status != model.ChangesetSucceeded {
		v2Error(w, r, 409, "changeset_not_applied", "操作未应用，请查看审批记录")
		return
	}
	v2Write(w, r, 200, map[string]any{"applied": true}, nil)
}

type ddnsCredentialZoneOption struct {
	ZoneName string `json:"zone_name"`
}

type ddnsCredentialOption struct {
	ID         int64                      `json:"id"`
	Name       string                     `json:"name"`
	Provider   model.DNSProvider          `json:"provider"`
	Enabled    bool                       `json:"enabled"`
	VerifiedAt *time.Time                 `json:"verified_at,omitempty"`
	Zones      []ddnsCredentialZoneOption `json:"zones"`
}

// ddnsCredentialOptions lists the DNS accounts a DDNS record may reference.
// Operators manage DDNS records but not DNS accounts, so only enabled,
// verified accounts are listed and only the fields the picker needs.
func (s *Server) ddnsCredentialOptions(ctx context.Context) ([]ddnsCredentialOption, error) {
	credentials, err := s.store.ListDNSCredentials(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ddnsCredentialOption, 0, len(credentials))
	for _, credential := range credentials {
		if !credential.Enabled || credential.VerifiedAt == nil {
			continue
		}
		zones := make([]ddnsCredentialZoneOption, 0, len(credential.Zones))
		for _, zone := range credential.Zones {
			zones = append(zones, ddnsCredentialZoneOption{ZoneName: zone.ZoneName})
		}
		out = append(out, ddnsCredentialOption{ID: credential.ID, Name: credential.Name, Provider: credential.Provider, Enabled: true, VerifiedAt: credential.VerifiedAt, Zones: zones})
	}
	return out, nil
}

func (s *Server) refreshProbeDDNSInventory(ctx context.Context, server model.Server) error {
	if reason := agentTaskImmediateFailure(&server); reason != "" {
		return errors.New(reason)
	}
	if server.AgentBuild != "" && !agentBuildSupportsTask(server.AgentBuild, agentBuildMinNetworkInterfaces) {
		return errors.New("请更新 Agent 后再读取 DDNS 网卡地址")
	}
	_, observed, err := s.probeDDNSInventory(ctx, server.ID)
	if err != nil {
		return err
	}
	if !observed.IsZero() && time.Since(observed) < time.Minute {
		return nil
	}
	tasks, err := s.store.ListTasksByServer(ctx, server.ID, 100)
	if err != nil {
		return err
	}
	var task model.AgentTask
	for _, existing := range tasks {
		if existing.Type == model.AgentTaskTypeListNetworkInterfaces && (existing.Status == "pending" || existing.Status == "running") {
			task = existing
			break
		}
	}
	if task.ID == 0 {
		task, err = s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeListNetworkInterfaces, map[string]any{}, time.Now().Unix())
		if err != nil {
			return err
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := s.store.GetTask(waitCtx, task.ID)
		if err != nil {
			return err
		}
		switch current.Status {
		case "succeeded":
			return nil
		case "failed", "rollback_failed", "cancelled":
			return errors.New("DDNS 网卡读取失败，保留原解析")
		}
		select {
		case <-waitCtx.Done():
			return errors.New("DDNS 网卡读取超时，保留原解析")
		case <-ticker.C:
		}
	}
}
