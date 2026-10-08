package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

type nodeIncidentIsolationOperation struct {
	EventID         int64   `json:"event_id"`
	EventVersion    int64   `json:"event_version"`
	InboundIDs      []int64 `json:"inbound_ids"`
	RecoveryPolicy  string  `json:"recovery_policy"`
	DurationMinutes int     `json:"duration_minutes,omitempty"`
}

func isolationRestoreAt(policy string, minutes int, now time.Time) (*time.Time, error) {
	if minutes == 0 {
		return nil, nil
	}
	if policy != "manual" {
		return nil, errors.New("指定剔除时长时恢复方式必须是手动计时")
	}
	if minutes < 15 || minutes > 7*24*60 {
		return nil, errors.New("剔除时长需在 15 分钟到 7 天之间")
	}
	restoreAt := now.UTC().Add(time.Duration(minutes) * time.Minute)
	return &restoreAt, nil
}

func (s *Server) registerNodeIncidentAutomationOperations() {
	s.registerNotificationBroadcastAutomationOperations()
	s.automation.RegisterValidator("node_incidents.isolate", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, event, err := s.validateNodeIncidentIsolationOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		preview, err := s.nodeIncidentImpactPreview(ctx, *event, request.InboundIDs, "isolate", request.RecoveryPolicy)
		if err != nil {
			return nil, err
		}
		return preview, nil
	})
	s.automation.RegisterRevisionResolver("node_incidents.isolate", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		request, event, err := s.validateNodeIncidentIsolationOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"node_incident:" + strconv.FormatInt(request.EventID, 10): fmt.Sprintf("%d:%s", event.Version, event.UpdatedAt.UTC().Format(time.RFC3339Nano))}, nil
	})
	s.automation.Register("node_incidents.isolate", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, event, err := s.validateNodeIncidentIsolationOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if principal.UserID == nil {
			return nil, errors.New("node publication isolation requires a user actor")
		}
		restoreAt, err := isolationRestoreAt(request.RecoveryPolicy, request.DurationMinutes, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		items, err := s.store.CreateNodePublicationIsolations(ctx, event.ID, *principal.UserID, request.InboundIDs, request.RecoveryPolicy, restoreAt)
		if err != nil {
			return nil, err
		}
		_ = s.store.AddAudit(ctx, model.AuditLog{ActorID: principal.UserID, Action: "node_publication_isolate", Target: "node_incident", Detail: fmt.Sprintf("%d:%v:%s", event.ID, request.InboundIDs, request.RecoveryPolicy), IP: "automation"})
		s.publishRealtime("node_incidents", "subscriptions")
		return map[string]any{"isolations": items, "deployment_required": false}, nil
	})

	s.automation.RegisterValidator("node_incidents.restore", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		eventID, isolationID, err := s.validateNodeIncidentRestoreOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"event_id": eventID, "isolation_id": isolationID, "deployment_required": false}, nil
	})
	s.automation.RegisterRevisionResolver("node_incidents.restore", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		eventID, isolationID, err := s.validateNodeIncidentRestoreOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"node_incident:" + strconv.FormatInt(eventID, 10): strconv.FormatInt(isolationID, 10)}, nil
	})
	s.automation.Register("node_incidents.restore", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		eventID, isolationID, err := s.validateNodeIncidentRestoreOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if principal.UserID == nil {
			return nil, errors.New("node publication restore requires a user actor")
		}
		if err := s.store.RestoreNodePublicationIsolation(ctx, isolationID, *principal.UserID); err != nil {
			return nil, err
		}
		_ = s.store.AddAudit(ctx, model.AuditLog{ActorID: principal.UserID, Action: "node_publication_restore", Target: "node_incident", Detail: fmt.Sprintf("%d:%d", eventID, isolationID), IP: "automation"})
		s.publishRealtime("node_incidents", "subscriptions")
		return map[string]any{"restored": true, "deployment_required": false}, nil
	})
}

func (s *Server) validateNodeIncidentIsolationOperation(ctx context.Context, principal application.Principal, input json.RawMessage) (nodeIncidentIsolationOperation, *model.NodeIncident, error) {
	var request nodeIncidentIsolationOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return request, nil, err
	}
	if request.EventID <= 0 || request.EventVersion <= 0 || len(request.InboundIDs) == 0 || len(request.InboundIDs) > 256 {
		return request, nil, errors.New("event_id, event_version and inbound_ids are required")
	}
	if request.RecoveryPolicy != "manual" && request.RecoveryPolicy != "auto" {
		return request, nil, errors.New("recovery_policy must be manual or auto")
	}
	if _, err := isolationRestoreAt(request.RecoveryPolicy, request.DurationMinutes, time.Now().UTC()); err != nil {
		return request, nil, err
	}
	event, err := s.store.GetNodeIncident(ctx, request.EventID)
	if err != nil {
		return request, nil, err
	}
	if event.Version != request.EventVersion || event.Status == model.NodeIncidentResolved {
		return request, nil, errors.New("node incident is resolved or its version changed")
	}
	if !principal.AllowsInt64("server_ids", event.ServerID) {
		return request, nil, errors.New("node incident is outside the authorized server boundary")
	}
	return request, event, nil
}

func (s *Server) validateNodeIncidentRestoreOperation(ctx context.Context, principal application.Principal, input json.RawMessage) (int64, int64, error) {
	var request struct {
		EventID     int64 `json:"event_id"`
		IsolationID int64 `json:"isolation_id"`
	}
	if err := strictAutomationInput(input, &request); err != nil || request.EventID <= 0 || request.IsolationID <= 0 {
		return 0, 0, errors.New("event_id and isolation_id are required")
	}
	event, err := s.store.GetNodeIncident(ctx, request.EventID)
	if err != nil || !principal.AllowsInt64("server_ids", event.ServerID) {
		return 0, 0, errors.New("authorized node incident is required")
	}
	items, err := s.store.ListNodePublicationIsolations(ctx, request.EventID)
	if err != nil {
		return 0, 0, err
	}
	for _, item := range items {
		if item.ID == request.IsolationID && item.Status == "hidden" {
			return request.EventID, request.IsolationID, nil
		}
	}
	return 0, 0, errors.New("active isolation does not belong to this event")
}
