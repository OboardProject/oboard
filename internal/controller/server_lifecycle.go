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
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

type serverNameConflictError struct {
	Name     string
	Existing []model.Server
}

func (e *serverNameConflictError) Error() string {
	labels := make([]string, 0, len(e.Existing))
	for _, item := range e.Existing {
		labels = append(labels, fmt.Sprintf("%s#%d", item.Name, item.ID))
	}
	return fmt.Sprintf("服务器名称 %q 已存在（%s）。请使用 servers.enrollment.issue 为已有记录重签发接入令牌，或先删除重复记录，不要再次创建", e.Name, strings.Join(labels, ", "))
}

func sameServerName(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func (s *Server) serversNamed(ctx context.Context, name string, excludeID int64) ([]model.Server, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	items, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	matches := make([]model.Server, 0)
	for _, item := range items {
		if item.ID == excludeID || !sameServerName(item.Name, name) {
			continue
		}
		matches = append(matches, item)
	}
	return matches, nil
}

func (s *Server) authorizedServersNamed(ctx context.Context, principal application.Principal, name string) ([]model.Server, error) {
	matches, err := s.serversNamed(ctx, name, 0)
	if err != nil {
		return nil, err
	}
	allowed := make([]model.Server, 0, len(matches))
	for _, item := range matches {
		if principal.AllowsInt64("server_ids", item.ID) {
			allowed = append(allowed, item)
		}
	}
	return allowed, nil
}

func serverDisplayLabel(item model.Server) string {
	return fmt.Sprintf("%s#%d", item.Name, item.ID)
}

func (s *Server) rejectDuplicateServerName(ctx context.Context, name string, excludeID int64) error {
	matches, err := s.serversNamed(ctx, name, excludeID)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return nil
	}
	return &serverNameConflictError{Name: strings.TrimSpace(name), Existing: matches}
}

func (s *Server) issueServerEnrollmentToken(ctx context.Context, serverID int64) (string, time.Time, *model.Server, error) {
	srv, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return "", time.Time{}, nil, err
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return "", time.Time{}, nil, err
	}
	expiresAt := time.Now().UTC().Add(enrollmentTokenTTL)
	if err := s.store.SetServerEnrollmentHash(ctx, srv.ID, security.HashSecret(token), expiresAt); err != nil {
		return "", time.Time{}, nil, err
	}
	updated, err := s.store.GetServer(ctx, srv.ID)
	if err != nil {
		return "", time.Time{}, nil, err
	}
	return token, expiresAt, updated, nil
}

func enrollmentServerView(srv model.Server) map[string]any {
	return map[string]any{
		"id": srv.ID, "name": srv.Name, "bbr_enabled": srv.BBREnabled,
		"agent_connected": srv.AgentID != "", "status": srv.Status,
	}
}

func agentInstallBBRValue(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

func agentInstallCommand(baseURL, bbrValue string) string {
	return "curl -fsSL " + shellSingleQuote(strings.TrimRight(baseURL, "/")+"/install/agent.sh") +
		` | env OBOARD_ENROLL_TOKEN="$OBOARD_ENROLL_TOKEN" OBOARD_INSTALL_BBR=` + shellSingleQuote(bbrValue) + " sh"
}

func (s *Server) registerServerLifecycleOperations() {
	s.automation.RegisterValidator("servers.enrollment.issue", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		srv, err := s.serverEnrollmentIssueCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"server_id": srv.ID, "name": srv.Name}, nil
	})
	s.automation.RegisterRevisionResolver("servers.enrollment.issue", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		srv, err := s.serverEnrollmentIssueCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"server:" + strconv.FormatInt(srv.ID, 10): srv.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("servers.enrollment.issue", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		current, err := s.serverEnrollmentIssueCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		token, expiresAt, srv, err := s.issueServerEnrollmentToken(ctx, current.ID)
		if err != nil {
			return nil, err
		}
		view := enrollmentServerView(*srv)
		public := map[string]any{"server": view, "enrollment_expires_at": expiresAt}
		oneTime := map[string]any{"server": view, "enrollment_expires_at": expiresAt, "enrollment_token": token}
		return automation.MutationResult{Public: public, OneTime: oneTime}, nil
	})

	s.automation.RegisterValidator("servers.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		srv, err := s.serverDeleteCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"server_id": srv.ID, "name": srv.Name, "agent_connected": srv.AgentID != ""}, nil
	})
	s.automation.RegisterRevisionResolver("servers.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		srv, err := s.serverDeleteCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"server:" + strconv.FormatInt(srv.ID, 10): srv.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("servers.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		srv, err := s.serverDeleteCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if _, err := s.deleteServerRecord(ctx, srv.ID, principal.UserID, "mcp"); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "server_id": srv.ID}, nil
	})
}

func (s *Server) serverEnrollmentIssueCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.Server, error) {
	var request struct {
		ServerID int64 `json:"server_id"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return model.Server{}, err
	}
	if request.ServerID <= 0 || !principal.AllowsInt64("server_ids", request.ServerID) {
		return model.Server{}, errors.New("authorized server_id is required")
	}
	srv, err := s.store.GetServer(ctx, request.ServerID)
	if err != nil {
		return model.Server{}, err
	}
	return *srv, nil
}

func (s *Server) serverDeleteCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.Server, error) {
	var request struct {
		ServerID int64 `json:"server_id"`
		Confirm  bool  `json:"confirm"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return model.Server{}, err
	}
	if request.ServerID <= 0 || !request.Confirm {
		return model.Server{}, errors.New("server_id and confirm=true are required")
	}
	if !principal.AllowsInt64("server_ids", request.ServerID) {
		return model.Server{}, errors.New("authorized server_id is required")
	}
	srv, err := s.store.GetServer(ctx, request.ServerID)
	if err != nil {
		return model.Server{}, err
	}
	return *srv, nil
}
