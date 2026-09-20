package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

type dnsBatchInput struct {
	ServerIDs []int64 `json:"server_ids"`
}
type dnsBatchResult struct {
	Operation *model.TaskOperation `json:"operation"`
	TaskIDs   []int64              `json:"task_ids"`
}

func (s *Server) validateDNSBatch(ctx context.Context, principal application.Principal, input json.RawMessage) (dnsBatchInput, error) {
	var req dnsBatchInput
	if err := strictAutomationInput(input, &req); err != nil {
		return req, err
	}
	if len(req.ServerIDs) == 0 || len(req.ServerIDs) > 1000 {
		return req, errors.New("server_ids must contain 1..1000 unique servers")
	}
	seen := map[int64]bool{}
	for _, id := range req.ServerIDs {
		if id <= 0 || seen[id] {
			return req, errors.New("server_ids must contain unique positive IDs")
		}
		seen[id] = true
		if !principal.AllowsInt64("server_ids", id) {
			return req, errors.New("server is outside the authorized server boundary")
		}
		if _, err := s.store.GetServer(ctx, id); err != nil {
			return req, err
		}
	}
	return req, nil
}

func (s *Server) registerDNSBatchOperation() {
	s.automation.RegisterValidator("servers.dns_test_batch", func(ctx context.Context, p application.Principal, input json.RawMessage) (any, error) {
		return s.validateDNSBatch(ctx, p, input)
	})
	s.automation.RegisterRevisionResolver("servers.dns_test_batch", func(ctx context.Context, p application.Principal, input json.RawMessage) (map[string]string, error) {
		req, err := s.validateDNSBatch(ctx, p, input)
		if err != nil {
			return nil, err
		}
		revisions := map[string]string{}
		for _, id := range req.ServerIDs {
			server, err := s.application.GetServer(ctx, p, id)
			if err != nil {
				return nil, err
			}
			revisions["server:"+strconv.FormatInt(id, 10)] = server.Revision
		}
		return revisions, nil
	})
	s.automation.Register("servers.dns_test_batch", func(ctx context.Context, p application.Principal, input json.RawMessage) (any, error) {
		return s.applyDNSBatch(ctx, p, input)
	})
}

func (s *Server) prepareDNSBatchTask(ctx context.Context, id int64, actor *int64) (store.OperationTask, error) {
	item := store.OperationTask{Target: model.TaskOperationTarget{Type: "server", ID: strconv.FormatInt(id, 10), State: "failed"}, Task: model.AgentTask{ServerID: id}}
	item.Target.CauseCode = "dns_server_unavailable"
	server, err := s.store.GetServer(ctx, id)
	if err != nil {
		return item, err
	}
	item.Target.CauseCode = "dns_policy_unavailable"
	policy, err := s.store.EnsureServerDNSPolicy(ctx, id)
	if err != nil {
		return item, err
	}
	item.Target.CauseCode = "dns_lists_unavailable"
	encrypted, bootstrap, err := s.dnsPolicyLists(ctx, *policy)
	if err != nil {
		return item, err
	}
	item.Target.CauseCode = "dns_request_identity_failed"
	requestID, err := security.RandomToken(18)
	if err != nil {
		return item, err
	}
	version := time.Now().UnixNano()
	item.Target.CauseCode = "dns_plan_invalid"
	plan, err := core.DNSBenchmarkPlanForPolicy(version, *policy, encrypted, *bootstrap, core.EffectiveIPStack(*server), model.DNSAutoTestAlways, requestID)
	if err != nil {
		return item, err
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		return item, err
	}
	item.Target.CauseCode = "dns_task_identity_failed"
	nonce, err := security.RandomToken(12)
	if err != nil {
		return item, err
	}
	item.Target.CauseCode = ""
	item.Task = model.AgentTask{ServerID: id, Type: model.AgentTaskTypeBenchmarkDNS, PayloadJSON: string(payload), Status: "pending", ResultJSON: "{}", ConfigVersion: version, Nonce: nonce}
	item.DNSRun = &model.DNSBenchmarkRun{RequestID: requestID, ServerID: id, PolicyRevision: policy.Revision, EncryptedListID: plan.EncryptedListID, EncryptedListRevision: plan.EncryptedListRevision, BootstrapListID: bootstrap.ID, BootstrapListRevision: bootstrap.Revision, Trigger: "manual", RequestedBy: actor}
	if reason := agentTaskImmediateFailure(server); reason != "" {
		item.Task.Status = "failed"
		result, _ := json.Marshal(map[string]any{"error": reason, "message": reason, "offline": true})
		item.Task.ResultJSON = string(result)
		item.DNSRun.Error = reason
	}
	return item, nil
}

func (s *Server) applyDNSBatch(ctx context.Context, p application.Principal, input json.RawMessage) (*dnsBatchResult, error) {
	req, err := s.validateDNSBatch(ctx, p, input)
	if err != nil {
		return nil, err
	}
	items := make([]store.OperationTask, 0, len(req.ServerIDs))
	for _, id := range req.ServerIDs {
		item, prepareErr := s.prepareDNSBatchTask(ctx, id, p.UserID)
		if prepareErr != nil {
			// Only a fixed preparation-stage code crosses the public boundary.
			item.Task = model.AgentTask{ServerID: id}
			item.DNSRun = nil
		}
		items = append(items, item)
	}
	op, ids, err := s.store.CreateTaskOperation(ctx, model.TaskOperation{Kind: "servers.dns_test_batch", Source: string(p.Type), ActorPrincipal: p.ID, ActorUserID: p.UserID}, items)
	if err != nil {
		return nil, err
	}
	for i, id := range ids {
		if id == 0 {
			continue
		}
		items[i].Task.ID = id
		if items[i].Task.Status == "pending" {
			s.tasks.wake(items[i].Task.ServerID)
		} else {
			s.notifyTaskFailure(ctx, items[i].Task)
		}
	}
	s.publishRealtime("tasks", "dns")
	return &dnsBatchResult{Operation: op, TaskIDs: ids}, nil
}

func (s *Server) dnsTestBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("authentication required"), 401)
		return
	}
	var req dnsBatchInput
	if !decode(w, r, &req) {
		return
	}
	raw, _ := json.Marshal(req)
	p := application.HumanPrincipal(*user, currentRole(r), netip.Addr{})
	if _, allowed := s.capabilities.Authorize(p, "servers.dns_test_batch"); !allowed {
		fail(w, errors.New("capability denied"), 403)
		return
	}
	result, err := s.applyDNSBatch(r.Context(), p, raw)
	if err != nil {
		fail(w, errors.New("无法创建批量 DNS 测试"), 400)
		return
	}
	auditReq(s, r, "test", "task_operation", result.Operation.ID)
	write(w, 202, result)
}
