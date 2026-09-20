package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) queryDeviceRetirement(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
	var req struct {
		BatchID int64 `json:"batch_id"`
		Offset  int   `json:"offset,omitempty"`
	}
	if err := strictAutomationInput(raw, &req); err != nil {
		return nil, err
	}
	if p.Role != model.RoleAdmin {
		return nil, errors.New("administrator required")
	}
	var filter map[string]any
	if len(p.ResourceFilter) > 0 {
		if err := json.Unmarshal(p.ResourceFilter, &filter); err != nil {
			return nil, err
		}
		if len(filter) > 0 {
			return nil, errors.New("unrestricted administrator required")
		}
	}
	preview, err := s.store.PreviewDeviceRetirement(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"preflight": preview}
	batch, err := s.store.DeviceRetirementBatch(ctx, req.BatchID)
	if err != nil {
		return nil, err
	}
	if batch.ID > 0 {
		req.BatchID = batch.ID
		out["batch"] = batch
		accounts, err := s.store.DeviceRetirementAccounts(ctx, req.BatchID, req.Offset)
		if err != nil {
			return nil, err
		}
		out["accounts"] = accounts
		nodes, err := s.store.DeviceRetirementNodes(ctx, req.BatchID, req.Offset)
		if err != nil {
			return nil, err
		}
		out["nodes"] = nodes
	}
	return out, nil
}

func (s *Server) deviceRetirementRead(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("batch_id"), 10, 64)
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	raw, _ := json.Marshal(map[string]any{"batch_id": id, "offset": offset})
	principal, ok := apiPrincipal(r)
	if ok {
		if _, allowed := s.capabilities.Authorize(principal, "device_retirement.read"); !allowed {
			http.Error(w, "capability denied", http.StatusForbidden)
			return
		}
	}
	if !ok {
		user := currentUser(r)
		if user == nil || user.Role != model.RoleAdmin {
			http.Error(w, "administrator required", 403)
			return
		}
		principal = application.Principal{Role: user.Role}
	}
	result, err := s.queryDeviceRetirement(r.Context(), principal, raw)
	if err != nil {
		http.Error(w, "retirement query failed", 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

type deviceRetirementInput struct {
	Confirm  bool   `json:"confirm"`
	BatchID  int64  `json:"batch_id,omitempty"`
	UserID   int64  `json:"user_id,omitempty"`
	Decision string `json:"decision,omitempty"`
	Phase    string `json:"phase,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Deadline string `json:"deadline,omitempty"`
}

func (s *Server) validateDeviceRetirement(ctx context.Context, p application.Principal, raw json.RawMessage) (deviceRetirementInput, error) {
	var req deviceRetirementInput
	if err := strictAutomationInput(raw, &req); err != nil {
		return req, err
	}
	if !req.Confirm || p.Role != model.RoleAdmin {
		return req, errors.New("administrator confirmation required")
	}
	// This is a fleet-wide migration; a resource-filtered principal cannot use it
	// to indirectly revoke another account or enqueue work on an excluded node.
	var filter map[string]any
	if len(p.ResourceFilter) > 0 {
		if err := json.Unmarshal(p.ResourceFilter, &filter); err != nil {
			return req, err
		}
		if len(filter) > 0 {
			return req, errors.New("retirement requires an unrestricted administrator principal")
		}
	}
	if req.Deadline != "" {
		end, err := time.Parse(time.RFC3339Nano, req.Deadline)
		at := time.Now().UTC()
		if err != nil || !end.After(at) || end.After(at.Add(14*24*time.Hour)) {
			return req, errors.New("deadline must be within 14 days")
		}
	}
	if req.Deadline != "" || req.Decision != "" {
		if strings.TrimSpace(req.Reason) == "" || len(req.Reason) > 500 {
			return req, errors.New("reason required")
		}
	}
	if req.Decision != "" && req.Decision != "account_authorized" && req.Decision != "retain_restriction" {
		return req, errors.New("invalid review decision")
	}
	if req.Phase != "" && req.Phase != "transition" && req.Phase != "revoke" {
		return req, errors.New("invalid migration phase")
	}
	return req, nil
}

func (s *Server) registerDeviceRetirementOperations() {
	for _, action := range []string{"start", "review", "advance", "finalize", "contract"} {
		name := "device_retirement." + action
		s.automation.RegisterValidator(name, func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
			return s.validateDeviceRetirement(ctx, p, raw)
		})
		s.automation.Register(name, func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
			req, err := s.validateDeviceRetirement(ctx, p, raw)
			if err != nil {
				return nil, err
			}
			at := time.Now().UTC()
			if action == "start" {
				deadline, err := time.Parse(time.RFC3339Nano, req.Deadline)
				if err != nil {
					return nil, err
				}
				actor := p.ID
				if actor == "" && p.UserID != nil {
					actor = "user:" + strconv.FormatInt(*p.UserID, 10)
				}
				batch, err := s.store.StartDeviceRetirement(ctx, actor, req.Reason, deadline, at)
				return map[string]any{"batch": batch}, err
			}
			batch, err := s.store.DeviceRetirementBatch(ctx, req.BatchID)
			if err != nil {
				return nil, err
			}
			switch action {
			case "review":
				err = s.store.ReviewDeviceRetirement(ctx, req.BatchID, req.UserID, req.Decision, req.Reason)
			case "advance":
				if req.Phase == "transition" {
					if batch.State == "review" {
						err = s.store.BeginDeviceRetirementTransition(ctx, req.BatchID, at)
					} else if batch.State != "transition" {
						return nil, errors.New("transition cannot be restored after revocation")
					}
				} else if req.Phase == "revoke" {
					if batch.State == "complete" {
						return map[string]any{"batch": batch}, nil
					}
					err = s.store.RevokeDeviceRetirement(ctx, req.BatchID, at)
				} else {
					return nil, errors.New("explicit transition or revoke phase required")
				}
				if err != nil {
					return nil, err
				}
				s.proxyCredentialRevision.Store(0)
				s.invalidateRoutingSnapshot()
				s.invalidateAuthorizationProjection()
				s.wakeAuthorizationSync()
				s.wakeRuntimeUsersSync()
				if err = s.reconcileProxyCredentials(ctx); err != nil {
					return nil, err
				}
				// The normal signed deployment path owns config versions and all Agent work.
				result, deployErr := s.runDeploymentOperation(ctx, p, 0)
				if deployErr != nil {
					return nil, deployErr
				}
				if req.Phase == "revoke" {
					payload, ok := result.(map[string]any)
					if !ok {
						return nil, errors.New("invalid deployment result")
					}
					var version int64
					switch v := payload["config_version"].(type) {
					case int64:
						version = v
					case uint64:
						version = int64(v)
					case int:
						version = int64(v)
					default:
						return nil, fmt.Errorf("invalid configuration version type")
					}
					err = s.store.BindDeviceRetirementDeployment(ctx, req.BatchID, version)
				}
			case "finalize":
				err = s.store.FinalizeDeviceRetirement(ctx, req.BatchID)
			case "contract":
				err = s.store.ContractDeviceRetirement(ctx, req.BatchID)
			}
			if err != nil {
				return nil, err
			}
			batch, err = s.store.DeviceRetirementBatch(ctx, req.BatchID)
			return map[string]any{"batch": batch}, err
		})
	}
}
