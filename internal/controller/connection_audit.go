package controller

import (
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

type connectionAuditReportItem struct {
	ReportID        string `json:"report_id"`
	UserID          int64  `json:"user_id"`
	InboundID       *int64 `json:"inbound_id"`
	PathID          *int64 `json:"path_id"`
	SourceIP        string `json:"source_ip"`
	SourceGeoCode   string `json:"source_geo_code"`
	Network         string `json:"network"`
	Destination     string `json:"destination"`
	DestinationPort int    `json:"destination_port"`
	OutboundTag     string `json:"outbound_tag"`
	OutboundType    string `json:"outbound_type"`
	ConnectionCount int64  `json:"connection_count"`
	ActivePeak      int64  `json:"active_peak"`
	ActiveAtEnd     int64  `json:"active_at_end"`
	StartedAt       string `json:"started_at"`
	EndedAt         string `json:"ended_at"`
}

func (s *Server) agentConnectionReports(w http.ResponseWriter, r *http.Request) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !server.ConnectionAuditEnabled {
		fail(w, errors.New("connection audit is disabled for this server"), http.StatusConflict)
		return
	}
	if !s.allowRate(w, r, "agent-connection-audit:"+server.AgentID, 120, time.Minute) {
		return
	}
	var req struct {
		Items []connectionAuditReportItem `json:"items"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Items) > 500 {
		fail(w, errors.New("too many connection audit items in one report"), http.StatusBadRequest)
		return
	}
	access, err := s.loadAccessData(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	userByID := make(map[int64]model.User, len(access.Users))
	for _, user := range access.Users {
		userByID[user.ID] = user
	}
	inboundByID := make(map[int64]model.Inbound, len(access.Inbounds))
	for _, inbound := range access.Inbounds {
		inboundByID[inbound.ID] = inbound
	}
	type accessPair struct{ inboundID, userID int64 }
	allowed := map[accessPair]struct{}{}
	for _, binding := range core.EffectiveInboundUsers(access.Inbounds, access.Users, access.InboundUsers, access.Groups, access.Members, access.Grants) {
		if binding.Enabled {
			allowed[accessPair{inboundID: binding.InboundID, userID: binding.UserID}] = struct{}{}
		}
	}
	paths, err := s.store.ListProxyPaths(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	steps, err := s.store.ListProxyPathSteps(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	reports := make([]model.ConnectionAuditReport, 0, len(req.Items))
	accepted := make([]string, 0, len(req.Items))
	for _, item := range req.Items {
		report, err := validateConnectionAuditItem(item, server.ID)
		if err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
		if item.InboundID == nil {
			fail(w, errors.New("connection audit must identify an inbound"), http.StatusBadRequest)
			return
		}
		inbound, exists := inboundByID[*item.InboundID]
		if !exists {
			accepted = append(accepted, report.ReportID)
			continue
		}
		accountingLocation := inbound.ServerID == server.ID
		if item.PathID != nil {
			if *item.PathID <= 0 {
				fail(w, errors.New("connection audit path_id must be positive"), http.StatusBadRequest)
				return
			}
			var path model.ProxyPath
			pathExists := false
			for _, candidate := range paths {
				if candidate.ID == *item.PathID {
					path = candidate
					pathExists = true
					break
				}
			}
			if !pathExists {
				if inbound.ServerID != server.ID {
					fail(w, errors.New("connection audit path does not belong to this agent"), http.StatusForbidden)
					return
				}
				accepted = append(accepted, report.ReportID)
				continue
			}
			if path.InboundID != inbound.ID {
				fail(w, errors.New("connection audit path does not belong to the inbound"), http.StatusForbidden)
				return
			}
			if !path.Enabled {
				if inbound.ServerID != server.ID {
					fail(w, errors.New("connection audit path does not belong to this agent"), http.StatusForbidden)
					return
				}
				accepted = append(accepted, report.ReportID)
				continue
			}
			accountingLocation = core.IsProxyPathAccountingLocation(server.ID, inbound.ID, path.ID, paths, steps, access.Inbounds)
		} else if core.ProxyPathRequiresAccountingPathID(inbound.ID, paths, steps, access.Inbounds) {
			if inbound.ServerID != server.ID {
				fail(w, errors.New("connection audit inbound does not belong to this agent"), http.StatusForbidden)
				return
			}
			accepted = append(accepted, report.ReportID)
			continue
		}
		if !accountingLocation {
			fail(w, errors.New("connection audit inbound does not belong to this agent"), http.StatusForbidden)
			return
		}
		if !inbound.Enabled {
			accepted = append(accepted, report.ReportID)
			continue
		}
		user, exists := userByID[item.UserID]
		if !exists || user.Status != "active" {
			accepted = append(accepted, report.ReportID)
			continue
		}
		if _, exists := allowed[accessPair{inboundID: inbound.ID, userID: item.UserID}]; !exists {
			accepted = append(accepted, report.ReportID)
			continue
		}
		report.InboundID = item.InboundID
		report.PathID = item.PathID
		reports = append(reports, report)
	}
	stored, err := s.store.AddConnectionAuditReports(r.Context(), reports)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	accepted = append(accepted, stored...)
	write(w, http.StatusOK, map[string]any{"ok": true, "accepted_report_ids": accepted})
}

func validateConnectionAuditItem(item connectionAuditReportItem, serverID int64) (model.ConnectionAuditReport, error) {
	reportID := strings.TrimSpace(item.ReportID)
	if reportID == "" || len(reportID) > 200 || item.UserID <= 0 {
		return model.ConnectionAuditReport{}, errors.New("connection audit identity is invalid")
	}
	sourceIP, err := netip.ParseAddr(strings.TrimSpace(item.SourceIP))
	if err != nil || !sourceIP.IsValid() {
		return model.ConnectionAuditReport{}, errors.New("connection audit source_ip is invalid")
	}
	geo := strings.ToUpper(strings.TrimSpace(item.SourceGeoCode))
	if geo != "" && (len(geo) != 2 || geo[0] < 'A' || geo[0] > 'Z' || geo[1] < 'A' || geo[1] > 'Z') {
		return model.ConnectionAuditReport{}, errors.New("connection audit source_geo_code is invalid")
	}
	network := strings.ToLower(strings.TrimSpace(item.Network))
	if network != "tcp" && network != "udp" {
		return model.ConnectionAuditReport{}, errors.New("connection audit network must be tcp or udp")
	}
	destination := strings.TrimSpace(item.Destination)
	outboundTag := strings.TrimSpace(item.OutboundTag)
	outboundType := strings.TrimSpace(item.OutboundType)
	if len(destination) > 255 || len(outboundTag) > 128 || len(outboundType) > 64 {
		return model.ConnectionAuditReport{}, errors.New("connection audit destination or outbound is too long")
	}
	if item.DestinationPort < 0 || item.DestinationPort > 65535 {
		return model.ConnectionAuditReport{}, errors.New("connection audit destination_port is invalid")
	}
	if item.ConnectionCount < 0 || item.ActivePeak < 0 || item.ActiveAtEnd < 0 || item.ActiveAtEnd > item.ActivePeak || item.ConnectionCount > 1_000_000_000 || item.ActivePeak > 1_000_000 {
		return model.ConnectionAuditReport{}, errors.New("connection audit counters are invalid")
	}
	if item.ConnectionCount == 0 && item.ActiveAtEnd == 0 {
		return model.ConnectionAuditReport{}, errors.New("connection audit report is empty")
	}
	startedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.StartedAt))
	if err != nil {
		return model.ConnectionAuditReport{}, errors.New("connection audit started_at is invalid")
	}
	endedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.EndedAt))
	if err != nil || endedAt.Before(startedAt) {
		return model.ConnectionAuditReport{}, errors.New("connection audit ended_at is invalid")
	}
	nowTime := time.Now().UTC()
	if endedAt.After(nowTime.Add(5*time.Minute)) || startedAt.Before(nowTime.Add(-31*24*time.Hour)) {
		return model.ConnectionAuditReport{}, errors.New("connection audit time is outside the accepted range")
	}
	return model.ConnectionAuditReport{
		ReportID: reportID, ServerID: serverID, UserID: item.UserID,
		SourceIP: sourceIP.Unmap().String(), SourceGeoCode: geo, Network: network,
		Destination: destination, DestinationPort: item.DestinationPort, OutboundTag: outboundTag, OutboundType: outboundType,
		ConnectionCount: item.ConnectionCount, ActivePeak: item.ActivePeak, ActiveAtEnd: item.ActiveAtEnd,
		StartedAt: startedAt.UTC(), EndedAt: endedAt.UTC(),
	}, nil
}

func (s *Server) connectionAuditOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	overview, err := s.store.ConnectionAuditOverview(r.Context(), intQuery(r, "window_hours", 24))
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"connection_audit": overview})
}

func (s *Server) connectionAuditUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	userID, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/audit/users/"), "/"), 10, 64)
	if err != nil || userID <= 0 {
		fail(w, errors.New("invalid audit user id"), http.StatusBadRequest)
		return
	}
	detail, err := s.store.ConnectionAuditUserDetail(r.Context(), userID, intQuery(r, "window_hours", 24))
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	write(w, http.StatusOK, map[string]any{"connection_audit_user": detail})
}
