package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const timeCheckInterval = 24 * time.Hour

func (s *Server) queueTimeCheck(ctx context.Context, server model.Server, force bool) (model.AgentTask, error) {
	if !force {
		active, err := s.store.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeCheckTime)
		if err == nil {
			return *active, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return model.AgentTask{}, err
		}
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return model.AgentTask{}, err
	}
	plan := model.TimeCheckPlan{
		Version:          time.Now().UnixNano(),
		CorrectionMode:   normalizeControllerTimeCorrectionMode(server.TimeCorrectionMode),
		ThresholdSeconds: timeCheckThresholdSeconds,
		NTPServers:       timeCheckNTPServers(settings),
		Force:            force,
	}
	return s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeCheckTime, plan, plan.Version)
}

func (s *Server) scheduleDailyTimeChecks(ctx context.Context) {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		log.Printf("list servers for time checks: %v", err)
		return
	}
	cutoff := time.Now().UTC().Add(-timeCheckInterval)
	for _, server := range servers {
		if server.Status != model.ServerOnline || strings.TrimSpace(server.AgentID) == "" {
			continue
		}
		if server.TimeCheckedAt != nil && server.TimeCheckedAt.After(cutoff) {
			continue
		}
		if _, err := s.queueTimeCheck(ctx, server, false); err != nil {
			log.Printf("queue time check for server %d: %v", server.ID, err)
		}
	}
}

func (s *Server) applyTimeCheckTaskResult(ctx context.Context, task model.AgentTask, taskStatus, resultJSON string) error {
	if task.Type != model.AgentTaskTypeCheckTime && task.Type != model.AgentTaskTypeApplyDeployment {
		return nil
	}
	result, found := timeCheckResultFromTask(task.Type, resultJSON)
	if !found && task.Type == model.AgentTaskTypeApplyDeployment {
		return nil
	}
	server, err := s.store.GetServer(ctx, task.ServerID)
	if err != nil {
		return err
	}
	if !found || taskStatus != "succeeded" {
		result = model.TimeCheckResult{
			Status:         "unavailable",
			CorrectionMode: server.TimeCorrectionMode,
			Error:          taskResultError(resultJSON, "时间检测任务未完成"),
		}
	}
	result.CheckedAt = time.Now().UTC()
	if result.CorrectionMode == "" {
		result.CorrectionMode = server.TimeCorrectionMode
	}
	if strings.TrimSpace(result.Status) == "" {
		result.Status = "unavailable"
	}
	if err := s.store.UpdateServerTimeCheck(ctx, task.ServerID, result); err != nil {
		return err
	}
	if server.TimeCorrectionMode == model.TimeCorrectionOff && result.CorrectionMode == model.TimeCorrectionOff && absInt64(result.RawOffsetMS) >= int64(timeCheckThresholdSeconds*1000) && result.Status != "unavailable" {
		s.notifyServerClockSkew(ctx, *server, result)
	}
	return nil
}

func timeCheckResultFromTask(taskType, raw string) (model.TimeCheckResult, bool) {
	if taskType == model.AgentTaskTypeCheckTime {
		var result model.TimeCheckResult
		if json.Unmarshal([]byte(raw), &result) != nil {
			return model.TimeCheckResult{}, false
		}
		return result, strings.TrimSpace(result.Status) != ""
	}
	var deployment struct {
		Steps []struct {
			Key    string          `json:"key"`
			Result json.RawMessage `json:"result"`
		} `json:"steps"`
	}
	if json.Unmarshal([]byte(raw), &deployment) != nil {
		return model.TimeCheckResult{}, false
	}
	for _, step := range deployment.Steps {
		if step.Key != "time_check" || len(step.Result) == 0 || string(step.Result) == "null" {
			continue
		}
		var result model.TimeCheckResult
		if json.Unmarshal(step.Result, &result) == nil && strings.TrimSpace(result.Status) != "" {
			return result, true
		}
	}
	return model.TimeCheckResult{}, false
}

func taskResultError(raw, fallback string) string {
	var value map[string]any
	if json.Unmarshal([]byte(raw), &value) == nil {
		for _, key := range []string{"error", "message"} {
			if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return fallback
}

func absInt64(value int64) int64 {
	if value == math.MinInt64 {
		return math.MaxInt64
	}
	if value < 0 {
		return -value
	}
	return value
}

func (s *Server) notifyServerClockSkew(ctx context.Context, server model.Server, result model.TimeCheckResult) {
	event := notificationEvent{
		Name: notificationServerClockSkew,
		Key:  fmt.Sprintf("server:clock-skew:%d:%s", server.ID, time.Now().UTC().Format("2006-01-02")),
		Data: map[string]string{
			"ServerName": server.Name,
			"ServerID":   fmt.Sprint(server.ID),
			"Offset":     fmt.Sprintf("%.1f 秒", float64(result.RawOffsetMS)/1000),
			"Source":     result.Source,
			"Time":       s.notificationNow(ctx),
		},
	}
	s.enqueueForcedAdminNotification(ctx, event)
}
