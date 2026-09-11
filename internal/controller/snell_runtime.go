package controller

import (
	"context"
	"encoding/json"
	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) recordSnellRuntime(ctx context.Context, task model.AgentTask, resultJSON string) {
	var result map[string]any
	if json.Unmarshal([]byte(resultJSON), &result) != nil {
		return
	}
	if steps, ok := result["steps"].([]any); ok {
		for _, raw := range steps {
			step, _ := raw.(map[string]any)
			if step["key"] == "config" {
				result, _ = step["result"].(map[string]any)
				break
			}
		}
	}
	verified, _ := result["runtime_verified"].(bool)
	desired, _ := result["runtime_desired_digest"].(string)
	loaded, _ := result["runtime_loaded_digest"].(string)
	if !verified || desired == "" || desired != loaded {
		return
	}
	var cfg string
	if task.Type == model.AgentTaskTypeApplyDeployment {
		var payload model.DeploymentTaskPayload
		if json.Unmarshal([]byte(task.PayloadJSON), &payload) != nil {
			return
		}
		cfg = payload.Config.Config
	} else {
		var payload model.ApplyCoreConfigTaskPayload
		if json.Unmarshal([]byte(task.PayloadJSON), &payload) != nil {
			return
		}
		cfg = payload.Config
	}
	var decoded struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	if json.Unmarshal([]byte(cfg), &decoded) != nil {
		return
	}
	listeners := map[string]map[string]any{}
	for _, item := range decoded.Inbounds {
		if item["type"] == "snell" {
			tag, _ := item["tag"].(string)
			listeners[tag] = item
		}
	}
	if err := s.store.ConfirmSnellRuntime(ctx, task.ServerID, task.ConfigVersion, listeners); err != nil {
		logConfigurationError("confirm Snell listeners", err)
	}
}
