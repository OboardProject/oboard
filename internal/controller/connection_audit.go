package controller

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// connectionAuditDiscardedReport tells the Agent that a specific report will
// never be accepted. Its ID is also returned in accepted_report_ids so an Agent
// that does not understand this field still drops the pending record instead of
// retrying it forever.
type connectionAuditDiscardedReport struct {
	ReportID string `json:"report_id"`
	Reason   string `json:"reason"`
}

type connectionAuditReportItem struct {
	ReportID             string `json:"report_id"`
	UserID               int64  `json:"user_id"`
	InboundID            *int64 `json:"inbound_id"`
	PathID               *int64 `json:"path_id"`
	DeviceIDHash         string `json:"device_id_hash"`
	CredentialEpoch      int64  `json:"credential_epoch"`
	ClientInstanceIDHash string `json:"client_instance_id_hash"`
	SourceIP             string `json:"source_ip"`
	SourceGeoCode        string `json:"source_geo_code"`
	Network              string `json:"network"`
	Destination          string `json:"destination"`
	DestinationPort      int    `json:"destination_port"`
	OutboundTag          string `json:"outbound_tag"`
	OutboundType         string `json:"outbound_type"`
	ConnectionCount      int64  `json:"connection_count"`
	ClosedCount          int64  `json:"closed_count"`
	DurationTotalMS      int64  `json:"duration_total_ms"`
	DurationMaxMS        int64  `json:"duration_max_ms"`
	UploadBytes          int64  `json:"upload_bytes"`
	DownloadBytes        int64  `json:"download_bytes"`
	PayloadFirstAt       string `json:"payload_first_at"`
	PayloadLastAt        string `json:"payload_last_at"`
	DurationLE1SCount    int64  `json:"duration_le_1s_count"`
	DurationLE5SCount    int64  `json:"duration_le_5s_count"`
	DurationLE20SCount   int64  `json:"duration_le_20s_count"`
	DurationGT20SCount   int64  `json:"duration_gt_20s_count"`
	ProbeState           string `json:"probe_state"`
	InternalProbe        bool   `json:"internal_probe"`
	PresenceSequence     uint64 `json:"presence_sequence"`
	ActivePeak           int64  `json:"active_peak"`
	ActiveAtEnd          int64  `json:"active_at_end"`
	CollectionGeneration uint64 `json:"collection_generation"`
	BucketCapacity       int    `json:"bucket_capacity"`
	DroppedBucketCount   int64  `json:"dropped_bucket_count"`
	CollectionStartedAt  string `json:"collection_started_at"`
	CollectionEndedAt    string `json:"collection_ended_at"`
	StartedAt            string `json:"started_at"`
	EndedAt              string `json:"ended_at"`
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
	if !s.effectiveConnectionAuditEnabled(r.Context(), server) {
		fail(w, errors.New("connection audit is disabled for this server"), http.StatusConflict)
		return
	}
	if !s.allowAgentRate(w, "agent-connection-audit:"+server.AgentID, 120, time.Minute) {
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
	// One immutable routing snapshot per report path instead of a full routing
	// rebuild per batch; the store revision invalidates it synchronously on
	// any authorization-relevant mutation.
	routing, err := s.routingSnapshot(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	data := routing.data
	userByID := routing.usersByID
	inboundByID := routing.inboundsByID
	paths := data.ProxyPaths
	steps := data.ProxyPathSteps
	allowed := routing.allowedAccessPairs()
	reports := make([]model.ConnectionAuditReport, 0, len(req.Items))
	accepted := make([]string, 0, len(req.Items))
	discarded := make([]connectionAuditDiscardedReport, 0)
	// A single malformed bucket must not poison the batch. Data-shape failures
	// are terminal for that one report: it is acknowledged so the Agent stops
	// retrying it forever, and reported back with a reason. Only genuine
	// security-boundary violations still fail the whole request.
	discard := func(reportID, reason string) {
		discarded = append(discarded, connectionAuditDiscardedReport{ReportID: reportID, Reason: reason})
		accepted = append(accepted, reportID)
	}
	for _, item := range req.Items {
		reportID := strings.TrimSpace(item.ReportID)
		if reportID == "" || len(reportID) > 200 {
			fail(w, errors.New("connection audit report_id is invalid"), http.StatusBadRequest)
			return
		}
		report, err := validateConnectionAuditItem(item, server.ID)
		if err != nil {
			discard(reportID, connectionAuditDiscardReason(err))
			continue
		}
		if item.InboundID == nil {
			discard(reportID, "missing_inbound")
			continue
		}
		inbound, exists := inboundByID[*item.InboundID]
		if !exists {
			accepted = append(accepted, report.ReportID)
			continue
		}
		accountingLocation := inbound.ServerID == server.ID
		if item.PathID != nil {
			if *item.PathID <= 0 {
				discard(reportID, "invalid_path_id")
				continue
			}
			path, pathExists := routing.pathsByID[*item.PathID]
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
			accountingLocation = core.IsProxyPathAccountingLocation(server.ID, inbound.ID, path.ID, paths, steps, data.Inbounds)
		} else if core.ProxyPathRequiresAccountingPathID(inbound.ID, paths, steps, data.Inbounds) {
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
		pathID := int64(0)
		if item.PathID != nil {
			pathID = *item.PathID
		}
		if _, exists := allowed[accessPair{inboundID: inbound.ID, userID: item.UserID, pathID: pathID}]; !exists {
			accepted = append(accepted, report.ReportID)
			continue
		}
		report.InboundID = item.InboundID
		report.PathID = item.PathID
		s.enrichConnectionAuditReport(&report)
		report.RouteID = s.auditRouteID(report.SourceIP, report.SourceCountryCode, report.SourceISP)
		reports = append(reports, report)
	}
	addResult, err := s.store.AddConnectionAuditReportsResult(r.Context(), reports)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	accepted = append(accepted, addResult.AcceptedReportIDs...)
	for _, id := range addResult.DiscardedDetailIDs {
		discarded = append(discarded, connectionAuditDiscardedReport{ReportID: id, Reason: "detail_collection_disabled_or_capacity"})
	}
	response := map[string]any{"ok": true, "accepted_report_ids": accepted}
	if len(discarded) > 0 {
		s.connectionAuditDiscardedTotal.Add(uint64(len(discarded)))
		log.Printf("connection audit discarded %d report(s) from agent=%s first_reason=%s", len(discarded), server.AgentID, discarded[0].Reason)
		response["discarded_reports"] = discarded
	}
	write(w, http.StatusOK, response)
}

// connectionAuditRejection marks a report that can never become valid. The
// stable reason code lets Agent and operators tell a poisoned record apart from
// a transient Controller failure.
type connectionAuditRejection struct {
	Reason  string
	Message string
}

func (e *connectionAuditRejection) Error() string { return e.Message }

func auditReject(reason, message string) error {
	return &connectionAuditRejection{Reason: reason, Message: message}
}

func connectionAuditDiscardReason(err error) string {
	var rejection *connectionAuditRejection
	if errors.As(err, &rejection) && rejection.Reason != "" {
		return rejection.Reason
	}
	return "invalid_report"
}

func validateConnectionAuditItem(item connectionAuditReportItem, serverID int64) (model.ConnectionAuditReport, error) {
	reportID := strings.TrimSpace(item.ReportID)
	if reportID == "" || len(reportID) > 200 || item.UserID <= 0 {
		return model.ConnectionAuditReport{}, auditReject("invalid_identity", "connection audit identity is invalid")
	}
	deviceIDHash := strings.TrimSpace(item.DeviceIDHash)
	clientInstanceIDHash := strings.TrimSpace(item.ClientInstanceIDHash)
	if len(deviceIDHash) > 128 || len(clientInstanceIDHash) > 128 || (deviceIDHash == "") != (item.CredentialEpoch == 0) || item.CredentialEpoch < 0 {
		return model.ConnectionAuditReport{}, auditReject("invalid_device_identity", "connection audit device identity is invalid")
	}
	sourceIP, err := netip.ParseAddr(strings.TrimSpace(item.SourceIP))
	if err != nil || !sourceIP.IsValid() {
		return model.ConnectionAuditReport{}, auditReject("invalid_source_ip", "connection audit source_ip is invalid")
	}
	geo := strings.ToUpper(strings.TrimSpace(item.SourceGeoCode))
	if geo != "" && (len(geo) != 2 || geo[0] < 'A' || geo[0] > 'Z' || geo[1] < 'A' || geo[1] > 'Z') {
		return model.ConnectionAuditReport{}, auditReject("invalid_source_geo_code", "connection audit source_geo_code is invalid")
	}
	network := strings.ToLower(strings.TrimSpace(item.Network))
	if network != "tcp" && network != "udp" {
		return model.ConnectionAuditReport{}, auditReject("invalid_network", "connection audit network must be tcp or udp")
	}
	destination := strings.TrimSpace(item.Destination)
	outboundTag := strings.TrimSpace(item.OutboundTag)
	outboundType := strings.TrimSpace(item.OutboundType)
	if len(destination) > 255 || len(outboundTag) > 128 || len(outboundType) > 64 {
		return model.ConnectionAuditReport{}, auditReject("invalid_destination", "connection audit destination or outbound is too long")
	}
	if item.DestinationPort < 0 || item.DestinationPort > 65535 {
		return model.ConnectionAuditReport{}, auditReject("invalid_destination_port", "connection audit destination_port is invalid")
	}
	maxDurationMS := int64((31 * 24 * time.Hour) / time.Millisecond)
	durationBucketTotal := item.DurationLE1SCount + item.DurationLE5SCount + item.DurationLE20SCount + item.DurationGT20SCount
	if item.ConnectionCount < 0 || item.ClosedCount < 0 || item.DurationTotalMS < 0 || item.DurationMaxMS < 0 || item.DurationMaxMS > item.DurationTotalMS || item.DurationMaxMS > maxDurationMS || item.UploadBytes < 0 || item.DownloadBytes < 0 || item.UploadBytes > 1<<60 || item.DownloadBytes > 1<<60 || item.DurationLE1SCount < 0 || item.DurationLE5SCount < 0 || item.DurationLE20SCount < 0 || item.DurationGT20SCount < 0 || durationBucketTotal != item.ClosedCount || item.ActivePeak < 0 || item.ActiveAtEnd < 0 || item.ActiveAtEnd > item.ActivePeak || item.ConnectionCount > 1_000_000_000 || item.ClosedCount > 1_000_000_000 || item.DurationTotalMS > maxDurationMS*1_000_000 || item.ActivePeak > 1_000_000 || item.ClosedCount+item.ActiveAtEnd > item.ConnectionCount+item.ActivePeak {
		return model.ConnectionAuditReport{}, auditReject("invalid_counters", "connection audit counters are invalid")
	}
	probeState := strings.ToLower(strings.TrimSpace(item.ProbeState))
	switch probeState {
	case "", "normal", "candidate", "confirmed", "normal_traffic":
	default:
		return model.ConnectionAuditReport{}, auditReject("invalid_probe_state", "connection audit probe_state is invalid")
	}
	if item.CollectionGeneration > math.MaxInt64 || item.BucketCapacity < 1 || item.BucketCapacity > 1_000_000 || item.DroppedBucketCount < 0 || item.DroppedBucketCount > 1_000_000_000 || item.PresenceSequence == 0 {
		return model.ConnectionAuditReport{}, auditReject("invalid_collection_coverage", "connection audit collection coverage is invalid")
	}
	if item.ConnectionCount == 0 && item.ActiveAtEnd == 0 {
		return model.ConnectionAuditReport{}, auditReject("empty_report", "connection audit report is empty")
	}
	startedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.StartedAt))
	if err != nil {
		return model.ConnectionAuditReport{}, auditReject("invalid_started_at", "connection audit started_at is invalid")
	}
	endedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.EndedAt))
	if err != nil || endedAt.Before(startedAt) {
		return model.ConnectionAuditReport{}, auditReject("invalid_ended_at", "connection audit ended_at is invalid")
	}
	nowTime := time.Now().UTC()
	if endedAt.After(nowTime.Add(5*time.Minute)) || startedAt.Before(nowTime.Add(-31*24*time.Hour)) {
		return model.ConnectionAuditReport{}, auditReject("time_outside_accepted_range", "connection audit time is outside the accepted range")
	}
	collectionStartedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.CollectionStartedAt))
	if err != nil {
		return model.ConnectionAuditReport{}, auditReject("invalid_collection_started_at", "connection audit collection_started_at is invalid")
	}
	collectionEndedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.CollectionEndedAt))
	if err != nil || collectionEndedAt.Before(collectionStartedAt) || collectionEndedAt.After(nowTime.Add(5*time.Minute)) || collectionStartedAt.Before(nowTime.Add(-31*24*time.Hour)) {
		return model.ConnectionAuditReport{}, auditReject("invalid_collection_ended_at", "connection audit collection_ended_at is invalid")
	}
	if startedAt.Before(collectionStartedAt) || endedAt.After(collectionEndedAt) {
		return model.ConnectionAuditReport{}, auditReject("event_outside_collection_window", "connection audit event is outside its collection window")
	}
	var payloadFirstAt, payloadLastAt time.Time
	if strings.TrimSpace(item.PayloadFirstAt) != "" || strings.TrimSpace(item.PayloadLastAt) != "" {
		payloadFirstAt, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(item.PayloadFirstAt))
		if err != nil {
			return model.ConnectionAuditReport{}, auditReject("invalid_payload_window", "connection audit payload_first_at is invalid")
		}
		payloadLastAt, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(item.PayloadLastAt))
		if err != nil || payloadLastAt.Before(payloadFirstAt) || payloadFirstAt.Before(startedAt) || payloadLastAt.After(endedAt) {
			return model.ConnectionAuditReport{}, auditReject("invalid_payload_window", "connection audit payload_last_at is invalid")
		}
	}
	if (item.UploadBytes+item.DownloadBytes > 0) != !payloadFirstAt.IsZero() {
		return model.ConnectionAuditReport{}, auditReject("inconsistent_payload_coverage", "connection audit payload coverage is inconsistent")
	}
	return model.ConnectionAuditReport{
		ReportID: reportID, ServerID: serverID, UserID: item.UserID,
		DeviceIDHash: deviceIDHash, CredentialEpoch: item.CredentialEpoch, ClientInstanceIDHash: clientInstanceIDHash,
		SourceIP: sourceIP.Unmap().String(), SourceGeoCode: geo, Network: network,
		Destination: destination, DestinationPort: item.DestinationPort, OutboundTag: outboundTag, OutboundType: outboundType,
		ConnectionCount: item.ConnectionCount, ClosedCount: item.ClosedCount, DurationTotalMS: item.DurationTotalMS, DurationMaxMS: item.DurationMaxMS,
		UploadBytes: item.UploadBytes, DownloadBytes: item.DownloadBytes, PayloadFirstAt: payloadFirstAt.UTC(), PayloadLastAt: payloadLastAt.UTC(),
		DurationLE1SCount: item.DurationLE1SCount, DurationLE5SCount: item.DurationLE5SCount, DurationLE20SCount: item.DurationLE20SCount, DurationGT20SCount: item.DurationGT20SCount,
		ProbeState: probeState, InternalProbe: item.InternalProbe, PresenceSequence: item.PresenceSequence, ActivePeak: item.ActivePeak, ActiveAtEnd: item.ActiveAtEnd,
		CollectionGeneration: item.CollectionGeneration, BucketCapacity: item.BucketCapacity, DroppedBucketCount: item.DroppedBucketCount, CollectionStartedAt: collectionStartedAt.UTC(), CollectionEndedAt: collectionEndedAt.UTC(),
		StartedAt: startedAt.UTC(), EndedAt: endedAt.UTC(),
	}, nil
}

func (s *Server) connectionAuditOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	retiredAuditRiskEndpoint(w)
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
	retiredAuditRiskEndpoint(w)
}

func (s *Server) enrichConnectionAuditReport(report *model.ConnectionAuditReport) {
	if report == nil || s.geoIP == nil {
		return
	}
	geo, err := s.geoIP.Lookup(report.SourceIP)
	if err != nil {
		return
	}
	report.SourceCountryCode = geo.CountryCode
	report.SourceCountry = geo.Country
	report.SourceProvince = geo.Province
	report.SourceCity = geo.City
	report.SourceISP = geo.ISP
	report.GeoDatabaseRevision = geo.Revision
}

func (s *Server) refreshConnectionAuditGeography(ctx context.Context) error {
	if s.geoIP == nil || !s.geoIPStatus.Available || s.geoIPStatus.Revision == "" {
		return nil
	}
	items, err := s.store.ConnectionAuditSourceIPsForGeoRevision(ctx, s.geoIPStatus.Revision)
	if err != nil {
		return err
	}
	for _, sourceIP := range items {
		geo, lookupErr := s.geoIP.Lookup(sourceIP)
		if lookupErr != nil {
			return lookupErr
		}
		if err := s.store.UpdateConnectionAuditSourceGeography(ctx, sourceIP, geo); err != nil {
			return err
		}
	}
	return nil
}
