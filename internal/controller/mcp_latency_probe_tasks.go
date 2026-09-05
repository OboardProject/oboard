package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

// latencyProbeTaskCreateInput is the machine contract for latency_probe_tasks.create.
type latencyProbeTaskCreateInput struct {
	Name            string  `json:"name,omitempty"`
	Province        string  `json:"province"`
	Carrier         string  `json:"carrier"`
	IntervalSeconds int     `json:"interval_seconds,omitempty"`
	Enabled         *bool   `json:"enabled,omitempty"`
	ServerIDs       []int64 `json:"server_ids,omitempty"`
}

// latencyProbeTaskUpdateInput is the machine contract for latency_probe_tasks.update.
type latencyProbeTaskUpdateInput struct {
	ID              int64    `json:"id"`
	Name            *string  `json:"name,omitempty"`
	Province        *string  `json:"province,omitempty"`
	Carrier         *string  `json:"carrier,omitempty"`
	IntervalSeconds *int     `json:"interval_seconds,omitempty"`
	Enabled         *bool    `json:"enabled,omitempty"`
	ServerIDs       *[]int64 `json:"server_ids,omitempty"`
}

type latencyProbeTaskIDInput struct {
	ID int64 `json:"id"`
}

// latencyProbeTaskServerBoundary rejects any server outside the principal boundary
// and confirms every referenced server still exists.
func (s *Server) latencyProbeTaskServerBoundary(ctx context.Context, principal application.Principal, serverIDs []int64) error {
	for _, id := range serverIDs {
		if id <= 0 {
			return errors.New("server_ids must contain positive integers")
		}
		if !principal.AllowsInt64("server_ids", id) {
			return errors.New("server is outside the authorized resource boundary")
		}
		if _, err := s.store.GetServer(ctx, id); err != nil {
			return errors.New("服务器不存在: " + strconv.FormatInt(id, 10))
		}
	}
	return nil
}

// latencyProbeTaskBoundary loads a task and enforces the principal boundary over
// every server the task is already assigned to.
func (s *Server) latencyProbeTaskBoundary(ctx context.Context, principal application.Principal, id int64) (*model.LatencyProbeTask, error) {
	if id <= 0 {
		return nil, errors.New("id must be a positive integer")
	}
	task, err := s.store.GetLatencyProbeTask(ctx, id)
	if err != nil {
		return nil, errors.New("延迟探测任务不存在")
	}
	for _, serverID := range task.ServerIDs {
		if !principal.AllowsInt64("server_ids", serverID) {
			return nil, errors.New("latency probe task is outside the authorized resource boundary")
		}
	}
	return task, nil
}

// latencyProbeTaskFromUpdate folds a partial update onto the stored task.
func latencyProbeTaskFromUpdate(current model.LatencyProbeTask, request latencyProbeTaskUpdateInput) model.LatencyProbeTask {
	next := current
	if request.Name != nil {
		next.Name = *request.Name
	}
	if request.Province != nil {
		next.Province = *request.Province
	}
	if request.Carrier != nil {
		next.Carrier = *request.Carrier
	}
	if request.IntervalSeconds != nil {
		next.IntervalSeconds = *request.IntervalSeconds
	}
	if request.Enabled != nil {
		next.Enabled = *request.Enabled
	}
	if request.ServerIDs != nil {
		next.ServerIDs = *request.ServerIDs
	}
	return next
}

// registerLatencyProbeTaskOperations wires the latency probe task capabilities of
// the MCP automation layer. A task owns exactly one province+carrier target, its
// own probe interval, and the set of servers that execute it.
func (s *Server) registerLatencyProbeTaskOperations() {
	s.automation.RegisterValidator("latency_probe_tasks.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request latencyProbeTaskCreateInput
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		task := model.LatencyProbeTask{Name: request.Name, Province: request.Province, Carrier: request.Carrier, IntervalSeconds: request.IntervalSeconds, Enabled: true, ServerIDs: request.ServerIDs}
		if request.Enabled != nil {
			task.Enabled = *request.Enabled
		}
		if err := s.latencyProbeTaskServerBoundary(ctx, principal, task.ServerIDs); err != nil {
			return nil, err
		}
		if err := s.store.ValidateLatencyProbeTask(ctx, &task); err != nil {
			return nil, err
		}
		return map[string]any{"name": task.Name, "province": task.Province, "carrier": task.Carrier, "interval_seconds": task.IntervalSeconds, "server_count": len(task.ServerIDs)}, nil
	})
	s.automation.Register("latency_probe_tasks.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request latencyProbeTaskCreateInput
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		task := model.LatencyProbeTask{Name: request.Name, Province: request.Province, Carrier: request.Carrier, IntervalSeconds: request.IntervalSeconds, Enabled: true, ServerIDs: request.ServerIDs}
		if request.Enabled != nil {
			task.Enabled = *request.Enabled
		}
		if err := s.latencyProbeTaskServerBoundary(ctx, principal, task.ServerIDs); err != nil {
			return nil, err
		}
		if err := s.store.SaveLatencyProbeTask(ctx, &task); err != nil {
			return nil, err
		}
		s.publishRealtime("server_metrics", "latency_probes")
		return map[string]any{"latency_probe_task": task}, nil
	})

	s.automation.RegisterValidator("latency_probe_tasks.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request latencyProbeTaskUpdateInput
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		current, err := s.latencyProbeTaskBoundary(ctx, principal, request.ID)
		if err != nil {
			return nil, err
		}
		next := latencyProbeTaskFromUpdate(*current, request)
		if err := s.latencyProbeTaskServerBoundary(ctx, principal, next.ServerIDs); err != nil {
			return nil, err
		}
		if err := s.store.ValidateLatencyProbeTask(ctx, &next); err != nil {
			return nil, err
		}
		return map[string]any{"id": next.ID, "name": next.Name, "province": next.Province, "carrier": next.Carrier, "interval_seconds": next.IntervalSeconds, "server_count": len(next.ServerIDs)}, nil
	})
	s.automation.RegisterRevisionResolver("latency_probe_tasks.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		var request latencyProbeTaskUpdateInput
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		current, err := s.latencyProbeTaskBoundary(ctx, principal, request.ID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"latency_probe_task:" + strconv.FormatInt(current.ID, 10): current.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("latency_probe_tasks.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request latencyProbeTaskUpdateInput
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		current, err := s.latencyProbeTaskBoundary(ctx, principal, request.ID)
		if err != nil {
			return nil, err
		}
		next := latencyProbeTaskFromUpdate(*current, request)
		if err := s.latencyProbeTaskServerBoundary(ctx, principal, next.ServerIDs); err != nil {
			return nil, err
		}
		if err := s.store.SaveLatencyProbeTask(ctx, &next); err != nil {
			return nil, err
		}
		s.publishRealtime("server_metrics", "latency_probes")
		return map[string]any{"latency_probe_task": next}, nil
	})

	s.automation.RegisterValidator("latency_probe_tasks.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request latencyProbeTaskIDInput
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		task, err := s.latencyProbeTaskBoundary(ctx, principal, request.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": task.ID, "name": task.Name}, nil
	})
	s.automation.RegisterRevisionResolver("latency_probe_tasks.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		var request latencyProbeTaskIDInput
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		task, err := s.latencyProbeTaskBoundary(ctx, principal, request.ID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"latency_probe_task:" + strconv.FormatInt(task.ID, 10): task.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("latency_probe_tasks.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request latencyProbeTaskIDInput
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		task, err := s.latencyProbeTaskBoundary(ctx, principal, request.ID)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteLatencyProbeTask(ctx, task.ID); err != nil {
			return nil, err
		}
		s.publishRealtime("server_metrics", "latency_probes")
		return map[string]any{"deleted": true}, nil
	})
}

// latencyProbeTasksRead lists probe tasks narrowed to the principal boundary.
func (s *Server) latencyProbeTasksRead(ctx context.Context, principal application.Principal) (any, error) {
	tasks, err := s.store.ListLatencyProbeTasks(ctx)
	if err != nil {
		return nil, err
	}
	visible := make([]model.LatencyProbeTask, 0, len(tasks))
	for _, task := range tasks {
		allowed := make([]int64, 0, len(task.ServerIDs))
		for _, serverID := range task.ServerIDs {
			if principal.AllowsInt64("server_ids", serverID) {
				allowed = append(allowed, serverID)
			}
		}
		if len(task.ServerIDs) > 0 && len(allowed) == 0 {
			continue
		}
		task.ServerIDs = allowed
		visible = append(visible, task)
	}
	return map[string]any{"latency_probe_tasks": visible}, nil
}
