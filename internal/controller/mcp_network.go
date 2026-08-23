package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

// registerNetworkAutomationOperations wires the DNS list, DNS policy, DNS
// record, MTU detection, port-forward, and tunnel capability operations of the
// MCP automation layer.

func (s *Server) registerNetworkAutomationOperations() {
	s.registerDNSListOperations()
	s.registerNodePresetOperations()
	s.registerSnellProfileOperations()
	s.registerDNSPolicyOperations()
	s.registerDNSTaskOperations()
	s.registerDNSRecordOperations()
	s.registerPortForwardOperations()
	s.registerTunnelOperations()
}

// ---- DNS lists ----

var dnsListAutomationFields = map[string]bool{
	"name": true, "kind": true, "candidates": true, "enabled": true,
}

func (s *Server) registerDNSListOperations() {
	for _, name := range []string{"dns_lists.create", "dns_lists.update", "dns_lists.delete", "dns_lists.set_default"} {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.dnsListAutomationValidate(ctx, principal, input, name)
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			return s.dnsListAutomationRevisions(ctx, principal, input, name)
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.applyDNSListOperation(ctx, principal, input, name)
		})
	}
}

type dnsListOperationInput struct {
	DNSList   json.RawMessage `json:"dns_list,omitempty"`
	DNSListID int64           `json:"dns_list_id,omitempty"`
	Changes   json.RawMessage `json:"changes,omitempty"`
	Confirm   bool            `json:"confirm,omitempty"`
}

func (s *Server) dnsListAutomationValidate(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	var request dnsListOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	switch name {
	case "dns_lists.create":
		if len(request.DNSList) == 0 {
			return nil, errors.New("dns_list object is required")
		}
		fields, err := decodeClosedAutomationFields(request.DNSList, dnsListAutomationFields, "dns_list")
		if err != nil {
			return nil, err
		}
		if _, ok := fields["name"]; !ok {
			return nil, errors.New("dns_list.name is required")
		}
		if _, ok := fields["kind"]; !ok {
			return nil, errors.New("dns_list.kind is required")
		}
		var list model.DNSList
		if err := json.Unmarshal(request.DNSList, &list); err != nil {
			return nil, err
		}
		list.ID, list.Revision, list.Protected, list.UsageCount = 0, 1, false, 0
		list.Enabled = true
		if err := core.ValidateDNSList(list); err != nil {
			return nil, err
		}
		return map[string]any{"dns_list": automationDNSListView(list)}, nil
	case "dns_lists.update":
		if request.DNSListID <= 0 || len(request.Changes) == 0 {
			return nil, errors.New("dns_list_id and changes are required")
		}
		fields, err := decodeClosedAutomationFields(request.Changes, dnsListAutomationFields, "changes")
		if err != nil {
			return nil, err
		}
		if len(fields) == 0 {
			return nil, errors.New("changes must contain at least one DNS list field")
		}
		current, err := s.store.GetDNSList(ctx, request.DNSListID)
		if err != nil {
			return nil, err
		}
		var patch model.DNSList
		if err := json.Unmarshal(request.Changes, &patch); err != nil {
			return nil, err
		}
		merged := mergeDNSListPatch(*current, patch, fields)
		merged.ID = current.ID
		merged.Kind = current.Kind
		merged.Protected = current.Protected
		if current.Protected {
			merged.Enabled = true
		}
		if err := core.ValidateDNSList(merged); err != nil {
			return nil, err
		}
		return map[string]any{"dns_list": automationDNSListView(merged), "changed_fields": automationChangedFields(fields)}, nil
	case "dns_lists.delete":
		if request.DNSListID <= 0 || !request.Confirm {
			return nil, errors.New("dns_list_id and confirm=true are required")
		}
		if _, err := s.store.GetDNSList(ctx, request.DNSListID); err != nil {
			return nil, err
		}
		return map[string]any{"dns_list_id": request.DNSListID}, nil
	case "dns_lists.set_default":
		if request.DNSListID <= 0 {
			return nil, errors.New("dns_list_id is required")
		}
		if _, err := s.store.GetDNSList(ctx, request.DNSListID); err != nil {
			return nil, err
		}
		return map[string]any{"dns_list_id": request.DNSListID}, nil
	default:
		return nil, errors.New("unsupported DNS list operation")
	}
}

func mergeDNSListPatch(current model.DNSList, patch model.DNSList, fields map[string]json.RawMessage) model.DNSList {
	merged := current
	if _, ok := fields["name"]; ok {
		merged.Name = patch.Name
	}
	if _, ok := fields["candidates"]; ok {
		merged.Candidates = patch.Candidates
	}
	if _, ok := fields["enabled"]; ok {
		merged.Enabled = patch.Enabled
	}
	return merged
}

func (s *Server) dnsListAutomationRevisions(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (map[string]string, error) {
	var request dnsListOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	listID := request.DNSListID
	if name == "dns_lists.create" {
		return map[string]string{}, nil
	}
	if listID <= 0 {
		return nil, errors.New("dns_list_id is required")
	}
	current, err := s.store.GetDNSList(ctx, listID)
	if err != nil {
		return nil, err
	}
	return map[string]string{"dns_list:" + strconv.FormatInt(current.ID, 10): current.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
}

func (s *Server) applyDNSListOperation(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	validated, err := s.dnsListAutomationValidate(ctx, principal, input, name)
	if err != nil {
		return nil, err
	}
	var request dnsListOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	switch name {
	case "dns_lists.create":
		var list model.DNSList
		if err := json.Unmarshal(request.DNSList, &list); err != nil {
			return nil, err
		}
		list.ID, list.Revision, list.Protected, list.UsageCount = 0, 1, false, 0
		list.Enabled = true
		if err := core.ValidateDNSList(list); err != nil {
			return nil, err
		}
		if err := s.store.CreateDNSList(ctx, &list); err != nil {
			return nil, err
		}
		return map[string]any{"dns_list": automationDNSListView(list)}, nil
	case "dns_lists.update":
		current, err := s.store.GetDNSList(ctx, request.DNSListID)
		if err != nil {
			return nil, err
		}
		fields, err := decodeClosedAutomationFields(request.Changes, dnsListAutomationFields, "changes")
		if err != nil {
			return nil, err
		}
		var patch model.DNSList
		if err := json.Unmarshal(request.Changes, &patch); err != nil {
			return nil, err
		}
		merged := mergeDNSListPatch(*current, patch, fields)
		merged.ID = current.ID
		merged.Kind = current.Kind
		merged.Protected = current.Protected
		if current.Protected {
			merged.Enabled = true
		}
		if err := core.ValidateDNSList(merged); err != nil {
			return nil, err
		}
		changed, err := s.store.UpdateDNSList(ctx, &merged)
		if err != nil {
			return nil, err
		}
		if changed {
			s.queuePeriodicDNSBenchmarksForList(ctx, merged)
		}
		return map[string]any{"dns_list": automationDNSListView(merged), "changed_fields": automationChangedFields(fields)}, nil
	case "dns_lists.delete":
		if err := s.store.DeleteDNSList(ctx, request.DNSListID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "dns_list_id": request.DNSListID}, nil
	case "dns_lists.set_default":
		item, err := s.store.SetDefaultDNSList(ctx, request.DNSListID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"dns_list": automationDNSListView(*item)}, nil
	}
	_ = validated
	return nil, errors.New("unsupported DNS list operation")
}

func automationDNSListView(list model.DNSList) map[string]any {
	return map[string]any{
		"id": list.ID, "revision": list.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"name": list.Name, "kind": list.Kind, "revision_number": list.Revision,
		"candidates": list.Candidates, "enabled": list.Enabled, "protected": list.Protected,
		"usage_count": list.UsageCount, "created_at": list.CreatedAt, "updated_at": list.UpdatedAt,
	}
}

func automationChangedFields(fields map[string]json.RawMessage) []string {
	out := make([]string, 0, len(fields))
	for field := range fields {
		out = append(out, field)
	}
	return out
}

// ---- server DNS policy ----

var dnsPolicyAutomationFields = map[string]bool{
	"encrypted_list_id": true, "bootstrap_list_id": true, "strategy": true,
	"auto_test": true, "test_interval_seconds": true,
}

func (s *Server) registerDNSPolicyOperations() {
	s.automation.RegisterValidator("servers.dns_policy.set", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		return s.dnsPolicyAutomationCandidate(ctx, principal, input)
	})
	s.automation.RegisterRevisionResolver("servers.dns_policy.set", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		return s.dnsPolicyAutomationRevisions(ctx, principal, input)
	})
	s.automation.Register("servers.dns_policy.set", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		return s.applyDNSPolicyOperation(ctx, principal, input)
	})
}

type dnsPolicyOperationInput struct {
	ServerID int64           `json:"server_id"`
	Changes  json.RawMessage `json:"changes"`
}

func (s *Server) dnsPolicyAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
	var request dnsPolicyOperationInput
	if err := strictAutomationInput(input, &request); err != nil || request.ServerID <= 0 {
		return nil, errors.New("server_id must be a positive integer")
	}
	if !principal.AllowsInt64("server_ids", request.ServerID) {
		return nil, errors.New("server is outside the authorized server boundary")
	}
	fields, err := decodeClosedAutomationFields(request.Changes, dnsPolicyAutomationFields, "changes")
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, errors.New("changes must contain at least one DNS policy field")
	}
	current, err := s.store.EnsureServerDNSPolicy(ctx, request.ServerID)
	if err != nil {
		return nil, err
	}
	var patch model.ServerDNSPolicy
	if err := json.Unmarshal(request.Changes, &patch); err != nil {
		return nil, err
	}
	merged := mergeDNSPolicyPatch(*current, patch, fields)
	if _, err := s.store.GetDNSList(ctx, merged.EncryptedListID); err != nil {
		return nil, fmt.Errorf("encrypted_list_id: %w", err)
	}
	if _, err := s.store.GetDNSList(ctx, merged.BootstrapListID); err != nil {
		return nil, fmt.Errorf("bootstrap_list_id: %w", err)
	}
	if !validDNSStrategy(merged.Strategy) {
		return nil, errors.New("unsupported dns strategy")
	}
	if err := core.ValidateDNSAutoTest(merged.AutoTest); err != nil || merged.AutoTest == model.DNSAutoTestAlways {
		return nil, errors.New("auto_test must be never, first_apply, or periodic")
	}
	if merged.TestIntervalSeconds > 0 && merged.TestIntervalSeconds < 60 {
		return nil, errors.New("test_interval_seconds must be >= 60")
	}
	return map[string]any{"dns_policy": automationDNSPolicyView(merged), "changed_fields": automationChangedFields(fields)}, nil
}

func mergeDNSPolicyPatch(current model.ServerDNSPolicy, patch model.ServerDNSPolicy, fields map[string]json.RawMessage) model.ServerDNSPolicy {
	merged := current
	if _, ok := fields["encrypted_list_id"]; ok {
		merged.EncryptedListID = patch.EncryptedListID
	}
	if _, ok := fields["bootstrap_list_id"]; ok {
		merged.BootstrapListID = patch.BootstrapListID
	}
	if _, ok := fields["strategy"]; ok {
		merged.Strategy = patch.Strategy
	}
	if _, ok := fields["auto_test"]; ok {
		merged.AutoTest = patch.AutoTest
	}
	if _, ok := fields["test_interval_seconds"]; ok {
		merged.TestIntervalSeconds = patch.TestIntervalSeconds
	}
	return merged
}

func (s *Server) dnsPolicyAutomationRevisions(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
	var request dnsPolicyOperationInput
	if err := strictAutomationInput(input, &request); err != nil || request.ServerID <= 0 {
		return nil, errors.New("server_id must be a positive integer")
	}
	server, err := s.application.GetServer(ctx, principal, request.ServerID)
	if err != nil {
		return nil, err
	}
	return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.Revision}, nil
}

func (s *Server) applyDNSPolicyOperation(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
	validated, err := s.dnsPolicyAutomationCandidate(ctx, principal, input)
	if err != nil {
		return nil, err
	}
	var request dnsPolicyOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	fields, err := decodeClosedAutomationFields(request.Changes, dnsPolicyAutomationFields, "changes")
	if err != nil {
		return nil, err
	}
	current, err := s.store.EnsureServerDNSPolicy(ctx, request.ServerID)
	if err != nil {
		return nil, err
	}
	var patch model.ServerDNSPolicy
	if err := json.Unmarshal(request.Changes, &patch); err != nil {
		return nil, err
	}
	merged := mergeDNSPolicyPatch(*current, patch, fields)
	if err := s.store.UpdateServerDNSPolicy(ctx, &merged); err != nil {
		return nil, err
	}
	if current.AutoTest == model.DNSAutoTestPeriodic && merged.AutoTest != model.DNSAutoTestPeriodic {
		plan := model.DNSBenchmarkPlan{ServerID: request.ServerID, PolicyRevision: merged.Revision, EncryptedListID: merged.EncryptedListID, BootstrapListID: merged.BootstrapListID, Mode: model.DNSAutoTestNever}
		_, _ = s.queueAgentTask(ctx, request.ServerID, model.AgentTaskTypeBenchmarkDNS, plan, time.Now().UnixNano())
	}
	_ = validated
	return map[string]any{"dns_policy": automationDNSPolicyView(merged), "changed_fields": automationChangedFields(fields)}, nil
}

func automationDNSPolicyView(policy model.ServerDNSPolicy) map[string]any {
	return map[string]any{
		"server_id": policy.ServerID, "revision": policy.Revision,
		"encrypted_list_id": policy.EncryptedListID, "bootstrap_list_id": policy.BootstrapListID,
		"strategy": policy.Strategy, "auto_test": policy.AutoTest, "test_interval_seconds": policy.TestIntervalSeconds,
		"last_attempt_at": policy.LastAttemptAt, "last_success_at": policy.LastSuccessAt,
		"last_error": policy.LastError, "needs_benchmark": policy.NeedsBenchmark,
		"updated_at": policy.UpdatedAt,
	}
}

// ---- DNS and MTU task triggers ----

func (s *Server) registerDNSTaskOperations() {
	s.automation.RegisterValidator("servers.dns_test", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		return s.dnsTestAutomationValidate(ctx, principal, input)
	})
	s.automation.RegisterRevisionResolver("servers.dns_test", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		return s.serverTaskAutomationRevision(ctx, principal, input)
	})
	s.automation.Register("servers.dns_test", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		return s.applyDNSTestOperation(ctx, principal, input)
	})
	s.automation.RegisterValidator("servers.mtu_detect", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		return s.mtuDetectAutomationValidate(ctx, principal, input)
	})
	s.automation.RegisterRevisionResolver("servers.mtu_detect", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		return s.serverTaskAutomationRevision(ctx, principal, input)
	})
	s.automation.Register("servers.mtu_detect", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		return s.applyMTUDetectOperation(ctx, principal, input)
	})
}

type serverTaskInput struct {
	ServerID     int64  `json:"server_id"`
	Action       string `json:"action"`
	TargetHost   string `json:"target_host"`
	TargetPort   int    `json:"target_port"`
	InterfaceName string `json:"interface_name"`
	OverheadBytes int    `json:"overhead_bytes"`
	DesiredMTU   int    `json:"desired_mtu"`
}

func (s *Server) serverTaskAutomationRevision(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
	var request serverTaskInput
	if err := strictAutomationInput(input, &request); err != nil || request.ServerID <= 0 {
		return nil, errors.New("server_id must be a positive integer")
	}
	server, err := s.application.GetServer(ctx, principal, request.ServerID)
	if err != nil {
		return nil, err
	}
	return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.Revision}, nil
}

func (s *Server) dnsTestAutomationValidate(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
	var request serverTaskInput
	if err := strictAutomationInput(input, &request); err != nil || request.ServerID <= 0 {
		return nil, errors.New("server_id must be a positive integer")
	}
	if !principal.AllowsInt64("server_ids", request.ServerID) {
		return nil, errors.New("server is outside the authorized server boundary")
	}
	if request.Action != "" && request.Action != "test" && request.Action != "test_and_apply" {
		return nil, errors.New("action must be test or test_and_apply")
	}
	if _, err := s.store.GetServer(ctx, request.ServerID); err != nil {
		return nil, err
	}
	return map[string]any{"server_id": request.ServerID, "action": firstNonEmptyString(request.Action, "test")}, nil
}

func (s *Server) applyDNSTestOperation(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
	var request serverTaskInput
	if err := strictAutomationInput(input, &request); err != nil || request.ServerID <= 0 {
		return nil, errors.New("server_id must be a positive integer")
	}
	action := firstNonEmptyString(request.Action, "test")
	if action != "test" && action != "test_and_apply" {
		return nil, errors.New("action must be test or test_and_apply")
	}
	server, err := s.store.GetServer(ctx, request.ServerID)
	if err != nil {
		return nil, err
	}
	policy, err := s.store.EnsureServerDNSPolicy(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	encrypted, err := s.store.GetDNSList(ctx, policy.EncryptedListID)
	if err != nil {
		return nil, err
	}
	bootstrap, err := s.store.GetDNSList(ctx, policy.BootstrapListID)
	if err != nil {
		return nil, err
	}
	requestID, err := security.RandomToken(18)
	if err != nil {
		return nil, err
	}
	version := time.Now().UnixNano()
	plan, err := core.DNSBenchmarkPlanForPolicy(version, *policy, *encrypted, *bootstrap, core.EffectiveIPStack(*server), model.DNSAutoTestAlways, requestID)
	if err != nil {
		return nil, err
	}
	run := model.DNSBenchmarkRun{
		RequestID: requestID, ServerID: server.ID, PolicyRevision: policy.Revision,
		EncryptedListID: encrypted.ID, EncryptedListRevision: encrypted.Revision,
		BootstrapListID: bootstrap.ID, BootstrapListRevision: bootstrap.Revision,
		Trigger: "manual", ApplyOnSuccess: action == "test_and_apply", Status: "pending",
	}
	if principal.UserID != nil {
		run.RequestedBy = principal.UserID
	}
	if err := s.store.CreateDNSBenchmarkRun(ctx, &run); err != nil {
		return nil, err
	}
	task, err := s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeBenchmarkDNS, plan, version)
	if err != nil {
		return nil, err
	}
	if err := s.store.AttachDNSBenchmarkTask(ctx, requestID, task.ID); err != nil {
		return nil, err
	}
	return map[string]any{"task": map[string]any{"id": task.ID, "type": task.Type, "status": task.Status}, "run": map[string]any{"request_id": run.RequestID, "status": run.Status}}, nil
}

func (s *Server) mtuDetectAutomationValidate(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
	var request serverTaskInput
	if err := strictAutomationInput(input, &request); err != nil || request.ServerID <= 0 {
		return nil, errors.New("server_id must be a positive integer")
	}
	if !principal.AllowsInt64("server_ids", request.ServerID) {
		return nil, errors.New("server is outside the authorized server boundary")
	}
	srv, err := s.store.GetServer(ctx, request.ServerID)
	if err != nil {
		return nil, err
	}
	version := time.Now().Unix()
	plan := mtuPlanFromServer(version, *srv, model.MTUModeDetect)
	if request.TargetHost != "" {
		plan.TargetHost = request.TargetHost
	}
	if request.TargetPort != 0 {
		plan.TargetPort = request.TargetPort
	}
	if request.InterfaceName != "" {
		plan.InterfaceName = request.InterfaceName
	}
	if request.OverheadBytes >= 0 {
		plan.OverheadBytes = request.OverheadBytes
	}
	if request.DesiredMTU > 0 {
		plan.DesiredMTU = request.DesiredMTU
	}
	if plan.TargetHost == "" {
		return nil, errors.New("target_host required")
	}
	if err := core.ValidateSafeHost(plan.TargetHost); err != nil {
		return nil, fmt.Errorf("target_host: %w", err)
	}
	if err := core.ValidateNetworkInterfaceName(plan.InterfaceName); err != nil {
		return nil, fmt.Errorf("interface_name: %w", err)
	}
	if err := core.ValidatePort(plan.TargetPort); err != nil {
		return nil, err
	}
	return map[string]any{"server_id": request.ServerID}, nil
}

func (s *Server) applyMTUDetectOperation(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
	var request serverTaskInput
	if err := strictAutomationInput(input, &request); err != nil || request.ServerID <= 0 {
		return nil, errors.New("server_id must be a positive integer")
	}
	srv, err := s.store.GetServer(ctx, request.ServerID)
	if err != nil {
		return nil, err
	}
	version := time.Now().Unix()
	plan := mtuPlanFromServer(version, *srv, model.MTUModeDetect)
	if request.TargetHost != "" {
		plan.TargetHost = request.TargetHost
	}
	if request.TargetPort != 0 {
		plan.TargetPort = request.TargetPort
	}
	if request.InterfaceName != "" {
		plan.InterfaceName = request.InterfaceName
	}
	if request.OverheadBytes >= 0 {
		plan.OverheadBytes = request.OverheadBytes
	}
	if request.DesiredMTU > 0 {
		plan.DesiredMTU = request.DesiredMTU
	}
	if plan.TargetHost == "" {
		return nil, errors.New("target_host required")
	}
	if err := core.ValidateSafeHost(plan.TargetHost); err != nil {
		return nil, fmt.Errorf("target_host: %w", err)
	}
	if err := core.ValidateNetworkInterfaceName(plan.InterfaceName); err != nil {
		return nil, fmt.Errorf("interface_name: %w", err)
	}
	if err := core.ValidatePort(plan.TargetPort); err != nil {
		return nil, err
	}
	task, err := s.queueAgentTask(ctx, srv.ID, model.AgentTaskTypeDetectMTU, plan, version)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task": map[string]any{"id": task.ID, "type": task.Type, "status": task.Status}}, nil
}

// ---- DNS records (real-time provider operations) ----

var dnsRecordAutomationFields = map[string]bool{
	"dns_zone_id": true, "type": true, "name": true, "content": true, "proxied": true,
	"ttl": true, "comment": true, "server_id": true, "inbound_id": true,
}

type dnsRecordOperationInput struct {
	Record   json.RawMessage `json:"record,omitempty"`
	ZoneID   int64           `json:"dns_zone_id,omitempty"`
	RecordID string          `json:"record_id,omitempty"`
	Changes  json.RawMessage `json:"changes,omitempty"`
	Confirm  bool            `json:"confirm,omitempty"`
}

func (s *Server) registerDNSRecordOperations() {
	for _, name := range []string{"dns_records.create", "dns_records.update", "dns_records.delete"} {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			record, err := s.dnsRecordAutomationValidate(ctx, principal, input, name)
			if err != nil {
				return nil, err
			}
			return map[string]any{"dns_record": record}, nil
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			return s.dnsRecordAutomationRevisions(ctx, principal, input, name)
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.applyDNSRecordOperation(ctx, principal, input, name)
		})
	}
}

func (s *Server) dnsRecordAutomationValidate(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (model.DNSRecord, error) {
	var request dnsRecordOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return model.DNSRecord{}, err
	}
	switch name {
	case "dns_records.create":
		if len(request.Record) == 0 {
			return model.DNSRecord{}, errors.New("record object is required")
		}
		fields, err := decodeClosedAutomationFields(request.Record, dnsRecordAutomationFields, "record")
		if err != nil {
			return model.DNSRecord{}, err
		}
		if _, ok := fields["dns_zone_id"]; !ok {
			return model.DNSRecord{}, errors.New("record.dns_zone_id is required")
		}
		var record model.DNSRecord
		if err := json.Unmarshal(request.Record, &record); err != nil {
			return model.DNSRecord{}, err
		}
		if err := s.validateDNSRecordAutomationCandidate(ctx, principal, record); err != nil {
			return model.DNSRecord{}, err
		}
		return record, nil
	case "dns_records.update":
		if request.ZoneID <= 0 || strings.TrimSpace(request.RecordID) == "" || len(request.Changes) == 0 {
			return model.DNSRecord{}, errors.New("dns_zone_id, record_id and changes are required")
		}
		fields, err := decodeClosedAutomationFields(request.Changes, dnsRecordAutomationFields, "changes")
		if err != nil {
			return model.DNSRecord{}, err
		}
		if len(fields) == 0 {
			return model.DNSRecord{}, errors.New("changes must contain at least one DNS record field")
		}
		var patch model.DNSRecord
		if err := json.Unmarshal(request.Changes, &patch); err != nil {
			return model.DNSRecord{}, err
		}
		patch.CredentialZoneID = request.ZoneID
		patch.ID = request.RecordID
		if err := s.validateDNSRecordAutomationCandidate(ctx, principal, patch); err != nil {
			return model.DNSRecord{}, err
		}
		return patch, nil
	case "dns_records.delete":
		if request.ZoneID <= 0 || strings.TrimSpace(request.RecordID) == "" || !request.Confirm {
			return model.DNSRecord{}, errors.New("dns_zone_id, record_id and confirm=true are required")
		}
		if err := s.authorizeDNSZone(ctx, principal, request.ZoneID); err != nil {
			return model.DNSRecord{}, err
		}
		return model.DNSRecord{CredentialZoneID: request.ZoneID, ID: request.RecordID}, nil
	default:
		return model.DNSRecord{}, errors.New("unsupported DNS record operation")
	}
}

func (s *Server) authorizeDNSZone(ctx context.Context, principal application.Principal, zoneID int64) error {
	zone, err := s.store.GetDNSCredentialZone(ctx, zoneID)
	if err != nil {
		return err
	}
	if zone.ServerID != nil && !principal.AllowsInt64("server_ids", *zone.ServerID) {
		return errors.New("DNS zone is outside the authorized server boundary")
	}
	return nil
}

func (s *Server) validateDNSRecordAutomationCandidate(ctx context.Context, principal application.Principal, record model.DNSRecord) error {
	if err := s.authorizeDNSZone(ctx, principal, record.CredentialZoneID); err != nil {
		return err
	}
	zone, err := s.store.GetDNSCredentialZone(ctx, record.CredentialZoneID)
	if err != nil {
		return err
	}
	credential, err := s.store.GetDNSCredential(ctx, zone.CredentialID)
	if err != nil {
		return err
	}
	if !credential.Enabled {
		return errors.New("DNS credential is disabled")
	}
	if record.ServerID != nil && !principal.AllowsInt64("server_ids", *record.ServerID) {
		return errors.New("record server is outside the authorized server boundary")
	}
	return validateDNSRecord(*credential, record)
}

func (s *Server) dnsRecordAutomationRevisions(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (map[string]string, error) {
	var request dnsRecordOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	zoneID := request.ZoneID
	if name == "dns_records.create" {
		var record model.DNSRecord
		if err := json.Unmarshal(request.Record, &record); err != nil {
			return nil, err
		}
		zoneID = record.CredentialZoneID
	}
	if zoneID <= 0 {
		return nil, errors.New("dns_zone_id is required")
	}
	if err := s.authorizeDNSZone(ctx, principal, zoneID); err != nil {
		return nil, err
	}
	zone, err := s.store.GetDNSCredentialZone(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	return map[string]string{"dns_zone:" + strconv.FormatInt(zone.ID, 10): zone.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
}

func (s *Server) applyDNSRecordOperation(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	record, err := s.dnsRecordAutomationValidate(ctx, principal, input, name)
	if err != nil {
		return nil, err
	}
	zone, err := s.store.GetDNSCredentialZone(ctx, record.CredentialZoneID)
	if err != nil {
		return nil, err
	}
	credential, err := s.store.GetDNSCredential(ctx, zone.CredentialID)
	if err != nil {
		return nil, err
	}
	scopedCredential := credentialForDNSZone(*credential, *zone)
	client, err := s.dnsProviderClient(scopedCredential)
	if err != nil {
		return nil, err
	}
	switch name {
	case "dns_records.create":
		saved, err := client.UpsertRecord(ctx, record)
		if err != nil {
			return nil, err
		}
		saved.CredentialZoneID, saved.Comment, saved.ServerID, saved.InboundID = record.CredentialZoneID, record.Comment, record.ServerID, record.InboundID
		if err := s.store.UpsertDNSRecordMetadata(ctx, record.CredentialZoneID, saved); err != nil {
			return nil, err
		}
		return map[string]any{"dns_record": automationDNSRecordView(saved, zone.ZoneName)}, nil
	case "dns_records.update":
		saved, err := client.UpsertRecord(ctx, record)
		if err != nil {
			return nil, err
		}
		saved.CredentialZoneID, saved.Comment, saved.ServerID, saved.InboundID = record.CredentialZoneID, record.Comment, record.ServerID, record.InboundID
		if err := s.store.UpsertDNSRecordMetadata(ctx, record.CredentialZoneID, saved); err != nil {
			return nil, err
		}
		return map[string]any{"dns_record": automationDNSRecordView(saved, zone.ZoneName)}, nil
	case "dns_records.delete":
		if err := client.DeleteRecord(ctx, record.ID); err != nil {
			return nil, err
		}
		if err := s.store.DeleteDNSRecordMetadata(ctx, record.CredentialZoneID, record.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "record_id": record.ID}, nil
	default:
		return nil, errors.New("unsupported DNS record operation")
	}
}

func automationDNSRecordView(record model.DNSRecord, zoneName string) map[string]any {
	return map[string]any{
		"id": record.ID, "dns_zone_id": record.CredentialZoneID, "zone_name": zoneName,
		"type": record.Type, "name": record.Name, "content": record.Content,
		"proxied": record.Proxied, "ttl": record.TTL, "enabled": record.Enabled,
		"comment": record.Comment, "server_id": record.ServerID, "inbound_id": record.InboundID,
	}
}

// ---- port forwards ----

var portForwardAutomationFields = map[string]bool{
	"name": true, "source_server_id": true, "target_server_id": true, "listen_ip": true,
	"listen_port": true, "target_address": true, "target_port": true, "protocol": true,
	"backend": true, "probe_mode": true, "probe_interval_seconds": true, "sample_rate": true,
	"priority": true, "config_json": true, "enabled": true,
}

type portForwardOperationInput struct {
	PortForward   json.RawMessage `json:"port_forward,omitempty"`
	PortForwardID int64           `json:"port_forward_id,omitempty"`
	Changes       json.RawMessage `json:"changes,omitempty"`
	Confirm       bool            `json:"confirm,omitempty"`
}

func (s *Server) registerPortForwardOperations() {
	for _, name := range []string{"port_forwards.create", "port_forwards.update", "port_forwards.delete"} {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			forward, changed, err := s.portForwardAutomationCandidate(ctx, principal, input, name)
			if err != nil {
				return nil, err
			}
			return automationPortForwardResult(forward, changed)
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			return s.portForwardAutomationRevisions(ctx, principal, input, name)
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.applyPortForwardOperation(ctx, principal, input, name)
		})
	}
}

func (s *Server) portForwardAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (model.PortForward, []string, error) {
	var request portForwardOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return model.PortForward{}, nil, err
	}
	switch name {
	case "port_forwards.create":
		if len(request.PortForward) == 0 {
			return model.PortForward{}, nil, errors.New("port_forward object is required")
		}
		var forward model.PortForward
		if err := json.Unmarshal(request.PortForward, &forward); err != nil {
			return model.PortForward{}, nil, err
		}
		forward.Enabled = true
		normalizePortForward(&forward)
		if err := s.validatePortForwardAutomationCandidate(ctx, principal, forward); err != nil {
			return model.PortForward{}, nil, err
		}
		return forward, nil, nil
	case "port_forwards.update":
		if request.PortForwardID <= 0 || len(request.Changes) == 0 {
			return model.PortForward{}, nil, errors.New("port_forward_id and changes are required")
		}
		fields, err := decodeClosedAutomationFields(request.Changes, portForwardAutomationFields, "changes")
		if err != nil {
			return model.PortForward{}, nil, err
		}
		if len(fields) == 0 {
			return model.PortForward{}, nil, errors.New("changes must contain at least one port forward field")
		}
		current, err := s.store.GetPortForward(ctx, request.PortForwardID)
		if err != nil {
			return model.PortForward{}, nil, err
		}
		if !principal.AllowsInt64("server_ids", current.SourceServerID) || !principal.AllowsInt64("server_ids", current.TargetServerID) {
			return model.PortForward{}, nil, errors.New("port forward is outside the authorized server boundary")
		}
		var patch model.PortForward
		if err := json.Unmarshal(request.Changes, &patch); err != nil {
			return model.PortForward{}, nil, err
		}
		merged := mergePortForwardPatch(*current, patch, fields)
		merged.ID = current.ID
		changed := automationChangedFields(fields)
		if err := s.validatePortForwardAutomationCandidate(ctx, principal, merged); err != nil {
			return model.PortForward{}, nil, err
		}
		return merged, changed, nil
	case "port_forwards.delete":
		if request.PortForwardID <= 0 || !request.Confirm {
			return model.PortForward{}, nil, errors.New("port_forward_id and confirm=true are required")
		}
		current, err := s.store.GetPortForward(ctx, request.PortForwardID)
		if err != nil {
			return model.PortForward{}, nil, err
		}
		if !principal.AllowsInt64("server_ids", current.SourceServerID) || !principal.AllowsInt64("server_ids", current.TargetServerID) {
			return model.PortForward{}, nil, errors.New("port forward is outside the authorized server boundary")
		}
		return *current, nil, nil
	default:
		return model.PortForward{}, nil, errors.New("unsupported port forward operation")
	}
}

func (s *Server) validatePortForwardAutomationCandidate(ctx context.Context, principal application.Principal, forward model.PortForward) error {
	if !principal.AllowsInt64("server_ids", forward.SourceServerID) || !principal.AllowsInt64("server_ids", forward.TargetServerID) {
		return errors.New("port forward server is outside the authorized server boundary")
	}
	if err := validatePortForward(forward); err != nil {
		return err
	}
	return s.store.ValidateServerExists(ctx, forward.SourceServerID, forward.TargetServerID)
}

func mergePortForwardPatch(current model.PortForward, patch model.PortForward, fields map[string]json.RawMessage) model.PortForward {
	merged := current
	if _, ok := fields["name"]; ok {
		merged.Name = patch.Name
	}
	if _, ok := fields["source_server_id"]; ok {
		merged.SourceServerID = patch.SourceServerID
	}
	if _, ok := fields["target_server_id"]; ok {
		merged.TargetServerID = patch.TargetServerID
	}
	if _, ok := fields["listen_ip"]; ok {
		merged.ListenIP = patch.ListenIP
	}
	if _, ok := fields["listen_port"]; ok {
		merged.ListenPort = patch.ListenPort
	}
	if _, ok := fields["target_address"]; ok {
		merged.TargetAddress = patch.TargetAddress
	}
	if _, ok := fields["target_port"]; ok {
		merged.TargetPort = patch.TargetPort
	}
	if _, ok := fields["protocol"]; ok {
		merged.Protocol = patch.Protocol
	}
	if _, ok := fields["backend"]; ok {
		merged.Backend = patch.Backend
	}
	if _, ok := fields["probe_mode"]; ok {
		merged.ProbeMode = patch.ProbeMode
	}
	if _, ok := fields["probe_interval_seconds"]; ok {
		merged.ProbeIntervalSeconds = patch.ProbeIntervalSeconds
	}
	if _, ok := fields["sample_rate"]; ok {
		merged.SampleRate = patch.SampleRate
	}
	if _, ok := fields["priority"]; ok {
		merged.Priority = patch.Priority
	}
	if _, ok := fields["config_json"]; ok {
		merged.ConfigJSON = patch.ConfigJSON
	}
	if _, ok := fields["enabled"]; ok {
		merged.Enabled = patch.Enabled
	}
	return merged
}

func (s *Server) portForwardAutomationRevisions(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (map[string]string, error) {
	forward, _, err := s.portForwardAutomationCandidate(ctx, principal, input, name)
	if err != nil {
		return nil, err
	}
	revisions := map[string]string{}
	for _, serverID := range []int64{forward.SourceServerID, forward.TargetServerID} {
		server, err := s.application.GetServer(ctx, principal, serverID)
		if err != nil {
			return nil, err
		}
		revisions["server:"+strconv.FormatInt(server.ID, 10)] = server.Revision
	}
	if name != "port_forwards.create" {
		revisions["port_forward:"+strconv.FormatInt(forward.ID, 10)] = forward.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return revisions, nil
}

func (s *Server) applyPortForwardOperation(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	forward, changed, err := s.portForwardAutomationCandidate(ctx, principal, input, name)
	if err != nil {
		return nil, err
	}
	switch name {
	case "port_forwards.create":
		if err := s.store.CreatePortForward(ctx, &forward); err != nil {
			return nil, err
		}
		if err := s.validateAllForwards(ctx); err != nil {
			_ = s.store.Delete(ctx, "port_forwards", forward.ID)
			return nil, err
		}
	case "port_forwards.update":
		previous, err := s.store.GetPortForward(ctx, forward.ID)
		if err != nil {
			return nil, err
		}
		if err := s.store.UpdatePortForward(ctx, &forward); err != nil {
			return nil, err
		}
		if err := s.validateAllForwards(ctx); err != nil {
			_ = s.store.UpdatePortForward(ctx, previous)
			return nil, err
		}
	case "port_forwards.delete":
		if err := s.store.DeletePortForwardProbeResults(ctx, forward.ID); err != nil {
			return nil, err
		}
		if err := s.store.Delete(ctx, "port_forwards", forward.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "port_forward_id": forward.ID}, nil
	}
	return automationPortForwardResult(forward, changed)
}

func automationPortForwardResult(forward model.PortForward, changed []string) (any, error) {
	view := map[string]any{
		"id": forward.ID, "revision": forward.UpdatedAt.UTC().Format(time.RFC3339Nano), "name": forward.Name,
		"source_server_id": forward.SourceServerID, "target_server_id": forward.TargetServerID,
		"listen_ip": forward.ListenIP, "listen_port": forward.ListenPort,
		"target_address": forward.TargetAddress, "target_port": forward.TargetPort,
		"protocol": forward.Protocol, "backend": forward.Backend, "probe_mode": forward.ProbeMode,
		"probe_interval_seconds": forward.ProbeIntervalSeconds, "sample_rate": forward.SampleRate,
		"priority": forward.Priority, "enabled": forward.Enabled,
		"created_at": forward.CreatedAt, "updated_at": forward.UpdatedAt,
	}
	if len(changed) == 0 {
		return map[string]any{"port_forward": view}, nil
	}
	return map[string]any{"port_forward": view, "changed_fields": changed}, nil
}

// ---- tunnels ----

var tunnelAutomationFields = map[string]bool{
	"name": true, "source_server_id": true, "target_server_id": true, "type": true,
	"local_address": true, "peer_address": true, "listen_port": true, "target_endpoint": true,
	"target_port": true, "priority": true, "config_json": true, "enabled": true,
}

type tunnelOperationInput struct {
	Tunnel   json.RawMessage `json:"tunnel,omitempty"`
	TunnelID int64           `json:"tunnel_id,omitempty"`
	Changes  json.RawMessage `json:"changes,omitempty"`
	Confirm  bool            `json:"confirm,omitempty"`
}

func (s *Server) registerTunnelOperations() {
	for _, name := range []string{"tunnels.create", "tunnels.update", "tunnels.delete"} {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			tunnel, changed, err := s.tunnelAutomationCandidate(ctx, principal, input, name)
			if err != nil {
				return nil, err
			}
			return automationTunnelResult(tunnel, changed)
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			return s.tunnelAutomationRevisions(ctx, principal, input, name)
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.applyTunnelOperation(ctx, principal, input, name)
		})
	}
}

func (s *Server) tunnelAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (model.Tunnel, []string, error) {
	var request tunnelOperationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return model.Tunnel{}, nil, err
	}
	switch name {
	case "tunnels.create":
		if len(request.Tunnel) == 0 {
			return model.Tunnel{}, nil, errors.New("tunnel object is required")
		}
		var tunnel model.Tunnel
		if err := json.Unmarshal(request.Tunnel, &tunnel); err != nil {
			return model.Tunnel{}, nil, err
		}
		tunnel.Enabled = true
		normalizeTunnel(&tunnel)
		if err := s.validateTunnelAutomationCandidate(ctx, principal, tunnel); err != nil {
			return model.Tunnel{}, nil, err
		}
		return tunnel, nil, nil
	case "tunnels.update":
		if request.TunnelID <= 0 || len(request.Changes) == 0 {
			return model.Tunnel{}, nil, errors.New("tunnel_id and changes are required")
		}
		fields, err := decodeClosedAutomationFields(request.Changes, tunnelAutomationFields, "changes")
		if err != nil {
			return model.Tunnel{}, nil, err
		}
		if len(fields) == 0 {
			return model.Tunnel{}, nil, errors.New("changes must contain at least one tunnel field")
		}
		current, err := s.store.GetTunnel(ctx, request.TunnelID)
		if err != nil {
			return model.Tunnel{}, nil, err
		}
		if !principal.AllowsInt64("server_ids", current.SourceServerID) || !principal.AllowsInt64("server_ids", current.TargetServerID) {
			return model.Tunnel{}, nil, errors.New("tunnel is outside the authorized server boundary")
		}
		var patch model.Tunnel
		if err := json.Unmarshal(request.Changes, &patch); err != nil {
			return model.Tunnel{}, nil, err
		}
		merged := mergeTunnelPatch(*current, patch, fields)
		merged.ID = current.ID
		changed := automationChangedFields(fields)
		if err := s.validateTunnelAutomationCandidate(ctx, principal, merged); err != nil {
			return model.Tunnel{}, nil, err
		}
		return merged, changed, nil
	case "tunnels.delete":
		if request.TunnelID <= 0 || !request.Confirm {
			return model.Tunnel{}, nil, errors.New("tunnel_id and confirm=true are required")
		}
		current, err := s.store.GetTunnel(ctx, request.TunnelID)
		if err != nil {
			return model.Tunnel{}, nil, err
		}
		if !principal.AllowsInt64("server_ids", current.SourceServerID) || !principal.AllowsInt64("server_ids", current.TargetServerID) {
			return model.Tunnel{}, nil, errors.New("tunnel is outside the authorized server boundary")
		}
		return *current, nil, nil
	default:
		return model.Tunnel{}, nil, errors.New("unsupported tunnel operation")
	}
}

func (s *Server) validateTunnelAutomationCandidate(ctx context.Context, principal application.Principal, tunnel model.Tunnel) error {
	if !principal.AllowsInt64("server_ids", tunnel.SourceServerID) || !principal.AllowsInt64("server_ids", tunnel.TargetServerID) {
		return errors.New("tunnel server is outside the authorized server boundary")
	}
	if err := validateTunnel(tunnel); err != nil {
		return err
	}
	return s.store.ValidateServerExists(ctx, tunnel.SourceServerID, tunnel.TargetServerID)
}

func mergeTunnelPatch(current model.Tunnel, patch model.Tunnel, fields map[string]json.RawMessage) model.Tunnel {
	merged := current
	if _, ok := fields["name"]; ok {
		merged.Name = patch.Name
	}
	if _, ok := fields["source_server_id"]; ok {
		merged.SourceServerID = patch.SourceServerID
	}
	if _, ok := fields["target_server_id"]; ok {
		merged.TargetServerID = patch.TargetServerID
	}
	if _, ok := fields["type"]; ok {
		merged.Type = patch.Type
	}
	if _, ok := fields["local_address"]; ok {
		merged.LocalAddress = patch.LocalAddress
	}
	if _, ok := fields["peer_address"]; ok {
		merged.PeerAddress = patch.PeerAddress
	}
	if _, ok := fields["listen_port"]; ok {
		merged.ListenPort = patch.ListenPort
	}
	if _, ok := fields["target_endpoint"]; ok {
		merged.TargetEndpoint = patch.TargetEndpoint
	}
	if _, ok := fields["target_port"]; ok {
		merged.TargetPort = patch.TargetPort
	}
	if _, ok := fields["priority"]; ok {
		merged.Priority = patch.Priority
	}
	if _, ok := fields["config_json"]; ok {
		merged.ConfigJSON = patch.ConfigJSON
	}
	if _, ok := fields["enabled"]; ok {
		merged.Enabled = patch.Enabled
	}
	return merged
}

func (s *Server) tunnelAutomationRevisions(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (map[string]string, error) {
	tunnel, _, err := s.tunnelAutomationCandidate(ctx, principal, input, name)
	if err != nil {
		return nil, err
	}
	revisions := map[string]string{}
	for _, serverID := range []int64{tunnel.SourceServerID, tunnel.TargetServerID} {
		server, err := s.application.GetServer(ctx, principal, serverID)
		if err != nil {
			return nil, err
		}
		revisions["server:"+strconv.FormatInt(server.ID, 10)] = server.Revision
	}
	if name != "tunnels.create" {
		revisions["tunnel:"+strconv.FormatInt(tunnel.ID, 10)] = tunnel.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return revisions, nil
}

func (s *Server) applyTunnelOperation(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	tunnel, changed, err := s.tunnelAutomationCandidate(ctx, principal, input, name)
	if err != nil {
		return nil, err
	}
	switch name {
	case "tunnels.create":
		if err := s.store.CreateTunnel(ctx, &tunnel); err != nil {
			return nil, err
		}
		if err := s.validateAllForwards(ctx); err != nil {
			_ = s.store.Delete(ctx, "tunnels", tunnel.ID)
			return nil, err
		}
	case "tunnels.update":
		previous, err := s.store.GetTunnel(ctx, tunnel.ID)
		if err != nil {
			return nil, err
		}
		if err := s.store.UpdateTunnel(ctx, &tunnel); err != nil {
			return nil, err
		}
		if err := s.validateAllForwards(ctx); err != nil {
			_ = s.store.UpdateTunnel(ctx, previous)
			return nil, err
		}
	case "tunnels.delete":
		if err := s.store.Delete(ctx, "tunnels", tunnel.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "tunnel_id": tunnel.ID}, nil
	}
	return automationTunnelResult(tunnel, changed)
}

func automationTunnelResult(tunnel model.Tunnel, changed []string) (any, error) {
	view := map[string]any{
		"id": tunnel.ID, "revision": tunnel.UpdatedAt.UTC().Format(time.RFC3339Nano), "name": tunnel.Name,
		"source_server_id": tunnel.SourceServerID, "target_server_id": tunnel.TargetServerID,
		"type": tunnel.Type, "local_address": tunnel.LocalAddress, "peer_address": tunnel.PeerAddress,
		"listen_port": tunnel.ListenPort, "target_endpoint": tunnel.TargetEndpoint, "target_port": tunnel.TargetPort,
		"priority": tunnel.Priority, "enabled": tunnel.Enabled,
		"created_at": tunnel.CreatedAt, "updated_at": tunnel.UpdatedAt,
	}
	if len(changed) == 0 {
		return map[string]any{"tunnel": view}, nil
	}
	return map[string]any{"tunnel": view, "changed_fields": changed}, nil
}

