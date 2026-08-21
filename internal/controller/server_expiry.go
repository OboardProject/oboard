package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

const (
	settingServerExpiryNotifyLeadDays = "server_expiry_notify_lead_days"
	settingServerExpiryNotifyTime     = "server_expiry_notify_time"
	defaultServerExpiryNotifyTime     = "00:00"
	serverExpiryAutoRenewGraceDays    = 3
	serverExpiryMaxExtendDays         = 3650
)

var defaultServerExpiryNotifyLeadDays = []int{7, 3}

func normalizeServerRenewalCycle(cycle model.ServerRenewalCycle) model.ServerRenewalCycle {
	switch cycle {
	case model.ServerRenewalCycleMonthly, model.ServerRenewalCycleQuarterly:
		return cycle
	default:
		return model.ServerRenewalCycleMonthly
	}
}

func normalizeExpiryNotifyLeadDays(values []int) ([]int, error) {
	if len(values) != 2 {
		return nil, errors.New("server_expiry_notify_lead_days must contain two values")
	}
	for _, value := range values {
		if value < 1 || value > 365 {
			return nil, errors.New("server_expiry_notify_lead_days must be between 1 and 365")
		}
	}
	if values[0] < values[1] {
		return nil, errors.New("server_expiry_notify_lead_days first value must be at least the daily reminder start")
	}
	return []int{values[0], values[1]}, nil
}

func parseExpiryNotifyLeadDays(raw string) ([]int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("expiry notify lead days are not configured")
	}
	var values []int
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	return normalizeExpiryNotifyLeadDays(values)
}

func normalizeExpiryNotifyTime(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return "", errors.New("server_expiry_notify_time must be in HH:mm format")
	}
	return parsed.Format("15:04"), nil
}

func expiryNotifySettings(settings map[string]string) ([]int, string) {
	leadDays, err := parseExpiryNotifyLeadDays(settings[settingServerExpiryNotifyLeadDays])
	if err != nil {
		leadDays = append([]int(nil), defaultServerExpiryNotifyLeadDays...)
	}
	notifyTime := defaultServerExpiryNotifyTime
	if normalized, err := normalizeExpiryNotifyTime(settings[settingServerExpiryNotifyTime]); err == nil {
		notifyTime = normalized
	}
	return leadDays, notifyTime
}

func startOfDay(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, value.Location())
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func addMonthsClamped(value time.Time, months int) time.Time {
	year, month, day := value.Date()
	targetMonth := int(month) + months
	year += (targetMonth - 1) / 12
	month = time.Month((targetMonth-1)%12 + 1)
	if day > daysInMonth(year, month) {
		day = daysInMonth(year, month)
	}
	return time.Date(year, month, day, 0, 0, 0, 0, value.Location())
}

func nextRenewalDate(expiry time.Time, cycle model.ServerRenewalCycle, loc *time.Location, today time.Time) time.Time {
	months := 1
	if normalizeServerRenewalCycle(cycle) == model.ServerRenewalCycleQuarterly {
		months = 3
	}
	next := startOfDay(expiry.In(loc))
	for i := 0; i < 120; i++ {
		next = addMonthsClamped(next, months)
		if !next.Before(today) {
			return next
		}
	}
	return addMonthsClamped(next, months)
}

func (s *Server) checkServerExpiry(ctx context.Context) {
	s.checkServerExpiryAt(ctx, time.Now().UTC())
}

func (s *Server) checkServerExpiryAt(ctx context.Context, now time.Time) {
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		log.Printf("list settings for server expiry: %v", err)
		return
	}
	s.runServerAutoRenewals(ctx, settings, now)
	s.scheduleServerExpiryNotifications(ctx, settings, now)
}

func (s *Server) runServerAutoRenewals(ctx context.Context, settings map[string]string, now time.Time) {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		log.Printf("list servers for auto renewal: %v", err)
		return
	}
	loc := trafficLocation(settings)
	today := startOfDay(now.In(loc))
	for _, server := range servers {
		if !server.AutoRenewEnabled || server.ExpiresAt == nil {
			continue
		}
		expiry := startOfDay(server.ExpiresAt.In(loc))
		due := expiry.AddDate(0, 0, serverExpiryAutoRenewGraceDays)
		if today.Before(due) {
			continue
		}
		next := nextRenewalDate(expiry, server.RenewalCycle, loc, today)
		if next.Equal(expiry) {
			continue
		}
		if err := s.store.MarkServerAutoRenewed(ctx, server.ID, next, now); err != nil {
			log.Printf("auto renew server %d: %v", server.ID, err)
			continue
		}
		_ = s.store.AddAudit(ctx, model.AuditLog{
			Action: "renew",
			Target: "server",
			Detail: fmt.Sprintf("%d:auto:%s", server.ID, next.Format(time.RFC3339Nano)),
			IP:     "controller",
		})
		log.Printf("server %d auto renewed from %s to %s", server.ID, expiry.Format(time.RFC3339Nano), next.Format(time.RFC3339Nano))
		if s.realtime != nil {
			s.realtime.publishServerPatch(realtimeServerPatch{
				ServerID: server.ID,
				Fields: map[string]any{
					"expires_at":           next,
					"last_auto_renewed_at": now,
				},
			})
		}
	}
}

func (s *Server) scheduleServerExpiryNotifications(ctx context.Context, settings map[string]string, now time.Time) {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		log.Printf("list servers for expiry notifications: %v", err)
		return
	}
	leadDays, notifyTime := expiryNotifySettings(settings)
	if len(leadDays) != 2 {
		return
	}
	loc := trafficLocation(settings)
	nowLocal := now.In(loc)
	if nowLocal.Format("15:04") < notifyTime {
		return
	}
	today := startOfDay(nowLocal)
	for _, server := range servers {
		if !server.ExpiryNotifyEnabled || server.ExpiresAt == nil {
			continue
		}
		expiry := startOfDay(server.ExpiresAt.In(loc))
		remaining := int(expiry.Sub(today).Hours() / 24)
		shouldSend := false
		status := ""
		if remaining == leadDays[0] {
			shouldSend = true
			status = fmt.Sprintf("还剩 %d 天", remaining)
		} else if remaining > 0 && remaining <= leadDays[1] {
			shouldSend = true
			status = fmt.Sprintf("还剩 %d 天", remaining)
		} else if remaining == 0 {
			shouldSend = true
			status = "今天到期"
		}
		if !shouldSend {
			continue
		}
		dateKey := today.Format("2006-01-02")
		key := fmt.Sprintf("server:%d:expiry:%s:%d", server.ID, dateKey, remaining)
		s.enqueueNotificationEvent(ctx, notificationEvent{
			Name: notificationServerExpiry,
			Key:  key,
			Data: map[string]string{
				"ServerName":    server.Name,
				"ServerID":      strconv.FormatInt(server.ID, 10),
				"ExpiresAt":     expiry.Format("2006-01-02"),
				"RemainingDays": strconv.Itoa(remaining),
				"Status":        status,
				"Time":          nowLocal.Format("2006-01-02 15:04:05 MST"),
			},
		})
	}
}

func (s *Server) extendServerExpiry(ctx context.Context, serverID int64, days int) (*model.Server, time.Time, error) {
	if days < 1 || days > serverExpiryMaxExtendDays {
		return nil, time.Time{}, errors.New("days must be between 1 and 3650")
	}
	current, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, time.Time{}, err
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	loc := trafficLocation(settings)
	base := startOfDay(time.Now().In(loc))
	if current.ExpiresAt != nil {
		base = startOfDay(current.ExpiresAt.In(loc))
	}
	next := base.AddDate(0, 0, days)
	if err := s.store.ExtendServerExpiry(ctx, serverID, next); err != nil {
		return nil, time.Time{}, err
	}
	updated, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, time.Time{}, err
	}
	return updated, next, nil
}

func (s *Server) extendServerExpiryHandler(w http.ResponseWriter, r *http.Request, serverID int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var request struct {
		Days int `json:"days"`
	}
	if !decode(w, r, &request) {
		return
	}
	updated, next, err := s.extendServerExpiry(r.Context(), serverID, request.Days)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	auditReq(s, r, "extend_expiry", "server", fmt.Sprint(serverID))
	write(w, http.StatusOK, map[string]any{
		"server":    updated,
		"server_id": serverID,
		"days":      request.Days,
		"expires_at": next.Format(time.RFC3339Nano),
	})
}

type serverExtendExpiryOperation struct {
	ServerID int64 `json:"server_id"`
	Days     int   `json:"days"`
}

func decodeServerExtendExpiryOperation(input json.RawMessage) (serverExtendExpiryOperation, error) {
	var request serverExtendExpiryOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return request, err
	}
	if request.ServerID <= 0 {
		return request, errors.New("positive server_id is required")
	}
	if request.Days < 1 || request.Days > serverExpiryMaxExtendDays {
		return request, errors.New("days must be between 1 and 3650")
	}
	return request, nil
}

func (s *Server) registerServerExpiryOperation() {
	s.automation.RegisterValidator("servers.extend_expiry", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeServerExtendExpiryOperation(input)
		if err != nil {
			return nil, err
		}
		if !principal.AllowsInt64("server_ids", request.ServerID) {
			return nil, errors.New("authorized server_id is required")
		}
		if _, err := s.store.GetServer(ctx, request.ServerID); err != nil {
			return nil, err
		}
		return map[string]any{"server_id": request.ServerID, "days": request.Days}, nil
	})
	s.automation.RegisterRevisionResolver("servers.extend_expiry", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		request, err := decodeServerExtendExpiryOperation(input)
		if err != nil || !principal.AllowsInt64("server_ids", request.ServerID) {
			return nil, errors.New("authorized server_id is required")
		}
		server, err := s.store.GetServer(ctx, request.ServerID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("servers.extend_expiry", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeServerExtendExpiryOperation(input)
		if err != nil {
			return nil, err
		}
		if !principal.AllowsInt64("server_ids", request.ServerID) {
			return nil, errors.New("authorized server_id is required")
		}
		_, next, err := s.extendServerExpiry(ctx, request.ServerID, request.Days)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"server_id": request.ServerID,
			"days":      request.Days,
			"expires_at": next.Format(time.RFC3339Nano),
		}, nil
	})
}
