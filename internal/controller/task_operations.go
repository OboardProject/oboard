package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/OboardProject/oboard/internal/application"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

type taskOperationReadInput struct {
	ID         string `json:"id"`
	BeforeTime string `json:"before_time"`
	BeforeID   string `json:"before_id"`
	Limit      int    `json:"limit"`
}

func taskOperationScope(p application.Principal) ([]int64, bool) {
	var boundary map[string]json.RawMessage
	if len(p.ResourceFilter) == 0 || (json.Unmarshal(p.ResourceFilter, &boundary) == nil && len(boundary) == 0) {
		return nil, p.AllowsInt64("server_ids", 1)
	}
	var nested application.ResourceFilter
	var flat struct {
		IDs []int64 `json:"server_ids"`
	}
	if json.Unmarshal(p.ResourceFilter, &nested) != nil || json.Unmarshal(p.ResourceFilter, &flat) != nil {
		return nil, false
	}
	if nested.Servers != nil && strings.EqualFold(strings.TrimSpace(nested.Servers.Mode), "all") {
		return nil, p.AllowsInt64("server_ids", 1)
	}
	ids := flat.IDs
	if nested.Servers != nil {
		ids = nested.Servers.IDs
	}
	allowed := []int64{}
	for _, id := range ids {
		if id > 0 && p.AllowsInt64("server_ids", id) {
			allowed = append(allowed, id)
		}
	}
	return allowed, false
}

func (s *Server) queryTaskOperations(ctx context.Context, p application.Principal, name string, raw json.RawMessage) (any, error) {
	var input taskOperationReadInput
	if err := strictAutomationInput(raw, &input); err != nil {
		return nil, err
	}
	if input.Limit == 0 {
		input.Limit = 25
	}
	if name == "task_operations.get" && strings.TrimSpace(input.ID) == "" {
		return nil, errors.New("id is required")
	}
	ids, all := taskOperationScope(p)
	items, err := s.store.ListTaskOperationRecords(ctx, ids, all, input.ID, input.BeforeTime, input.BeforeID, input.Limit)
	if err != nil {
		return nil, err
	}
	if name == "task_operations.get" {
		if len(items) == 0 {
			return nil, sql.ErrNoRows
		}
		return items[0], nil
	}
	return map[string]any{"operations": items}, nil
}

func (s *Server) taskOperations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("authentication required"), 401)
		return
	}
	p := application.HumanPrincipal(*user, currentRole(r), netip.Addr{})
	input := taskOperationReadInput{ID: strings.TrimPrefix(r.URL.Path, "/api/v1/task-operations/")}
	name := "task_operations.get"
	if r.URL.Path == "/api/v1/task-operations" {
		name = "task_operations.list"
		input.ID = ""
	}
	input.BeforeTime = r.URL.Query().Get("before_time")
	input.BeforeID = r.URL.Query().Get("before_id")
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			fail(w, errors.New("invalid limit"), 400)
			return
		}
		input.Limit = n
	}
	if _, ok := s.capabilities.Authorize(p, name); !ok {
		fail(w, errors.New("capability denied"), 403)
		return
	}
	raw, _ := json.Marshal(input)
	result, err := s.queryTaskOperations(r.Context(), p, name, raw)
	if err != nil {
		fail(w, errors.New("operation not found or invalid query"), 404)
		return
	}
	write(w, 200, result)
}
