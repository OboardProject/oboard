package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

type serverResetTrafficOperation struct {
	ServerID int64 `json:"server_id"`
}

func decodeServerResetTrafficOperation(input json.RawMessage) (serverResetTrafficOperation, error) {
	var request serverResetTrafficOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return request, err
	}
	if request.ServerID <= 0 {
		return request, errors.New("positive server_id is required")
	}
	return request, nil
}

func (s *Server) resetServerTraffic(ctx context.Context, serverID int64) (*model.Server, error) {
	current, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, err
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return nil, err
	}
	location := trafficLocation(settings)
	key, start, end := trafficWindow(time.Now(), current.TrafficResetMode, current.TrafficResetDay, time.Time{}, location)
	window := model.ServerTrafficWindow{Key: key, Start: start, End: end}
	if err := s.store.SetServerTrafficUsed(ctx, current.ID, 0, window); err != nil {
		return nil, err
	}
	updated, err := s.store.GetServer(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	if s.realtime != nil {
		s.realtime.publishServerPatch(realtimeServerPatch{
			ServerID: updated.ID,
			Fields: map[string]any{
				"traffic_upload_bytes":   updated.TrafficUploadBytes,
				"traffic_download_bytes": updated.TrafficDownloadBytes,
				"traffic_period_start":   updated.TrafficPeriodStart,
				"traffic_period_end":     updated.TrafficPeriodEnd,
				"telemetry_updated_at":   updated.TelemetryUpdatedAt,
			},
		})
	}
	return updated, nil
}

func serverTrafficResetResult(updated *model.Server) map[string]any {
	used := updated.TrafficUploadBytes + updated.TrafficDownloadBytes
	return map[string]any{
		"server_id":              updated.ID,
		"traffic_used_bytes":     used,
		"traffic_upload_bytes":   updated.TrafficUploadBytes,
		"traffic_download_bytes": updated.TrafficDownloadBytes,
		"traffic_period_start":   updated.TrafficPeriodStart,
		"traffic_period_end":     updated.TrafficPeriodEnd,
	}
}

func (s *Server) resetServerTrafficHandler(w http.ResponseWriter, r *http.Request, serverID int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	updated, err := s.resetServerTraffic(r.Context(), serverID)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	auditReq(s, r, "reset_traffic", "server", fmt.Sprint(serverID))
	body := serverTrafficResetResult(updated)
	body["server"] = updated
	write(w, http.StatusOK, body)
}

func (s *Server) registerServerTrafficResetOperation() {
	s.automation.RegisterValidator("servers.reset_traffic", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeServerResetTrafficOperation(input)
		if err != nil {
			return nil, err
		}
		if !principal.AllowsInt64("server_ids", request.ServerID) {
			return nil, errors.New("authorized server_id is required")
		}
		if _, err := s.store.GetServer(ctx, request.ServerID); err != nil {
			return nil, err
		}
		return map[string]any{"server_id": request.ServerID}, nil
	})
	s.automation.RegisterRevisionResolver("servers.reset_traffic", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		request, err := decodeServerResetTrafficOperation(input)
		if err != nil || !principal.AllowsInt64("server_ids", request.ServerID) {
			return nil, errors.New("authorized server_id is required")
		}
		server, err := s.store.GetServer(ctx, request.ServerID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("servers.reset_traffic", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeServerResetTrafficOperation(input)
		if err != nil {
			return nil, err
		}
		if !principal.AllowsInt64("server_ids", request.ServerID) {
			return nil, errors.New("authorized server_id is required")
		}
		updated, err := s.resetServerTraffic(ctx, request.ServerID)
		if err != nil {
			return nil, err
		}
		return serverTrafficResetResult(updated), nil
	})
}
