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
	// One immutable routing snapshot per report path instead of a full routing
	// rebuild per batch; the store revision invalidates it synchronously on
	// any authorization-relevant mutation.
	routing, err := s.routingSnapshot(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	data := routing.data
	snapshot := routing.snapshot
	userByID := routing.usersByID
	inboundByID := routing.inboundsByID
	paths := data.ProxyPaths
	steps := data.ProxyPathSteps
	type accessPair struct{ inboundID, userID, pathID int64 }
	allowed := map[accessPair]struct{}{}
	for _, binding := range snapshot.InboundUserBindings() {
		if binding.Enabled {
			allowed[accessPair{inboundID: binding.InboundID, userID: binding.UserID}] = struct{}{}
		}
	}
	for _, binding := range snapshot.ProxyPathUserBindings() {
		if binding.Enabled {
			allowed[accessPair{inboundID: binding.InboundID, userID: binding.UserID, pathID: binding.ProxyPathID}] = struct{}{}
		}
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
	insertedReports := make(map[string]struct{}, len(addResult.InsertedReportIDs))
	for _, reportID := range addResult.InsertedReportIDs {
		insertedReports[reportID] = struct{}{}
	}
	// Device activity is deduplicated per batch and written with one
	// statement instead of one UPDATE per report. Retries are already
	// accounted for and must not create another write.
	deviceActivity := make(map[string]time.Time, len(addResult.InsertedReportIDs))
	for _, report := range reports {
		if _, inserted := insertedReports[report.ReportID]; !inserted {
			continue
		}
		if report.DeviceIDHash == "" || report.PayloadLastAt.IsZero() {
			continue
		}
		if latest, ok := deviceActivity[report.DeviceIDHash]; !ok || report.PayloadLastAt.After(latest) {
			deviceActivity[report.DeviceIDHash] = report.PayloadLastAt
		}
	}
	if err := s.store.MarkUserDevicesProxyActivity(r.Context(), deviceActivity); err != nil {
		log.Printf("mark device proxy activity: %v", err)
	}
	// Actions, notifications, and incident evaluation share one bounded,
	// debounced queue. Only newly inserted reports wake it; idempotent Agent
	// retries are acknowledged without repeating historical scans.
	for _, userID := range addResult.InsertedUserIDs {
		s.auditRisk.enqueue(userID)
	}
	accepted = append(accepted, addResult.AcceptedReportIDs...)
	write(w, http.StatusOK, map[string]any{"ok": true, "accepted_report_ids": accepted})
}

func (s *Server) applyConnectionAuditDeviceActions(ctx context.Context, userIDs []int64) {
	if len(userIDs) == 0 || s.auditSettingsState(ctx).Action != model.AuditActionRestrict {
		return
	}
	s.connectionAuditActionMu.Lock()
	defer s.connectionAuditActionMu.Unlock()
	// Evaluate only the reported users' 24h overview instead of computing the
	// overview of every user in the system.
	overview, err := s.store.ConnectionAuditOverviewForUsers(ctx, 24, true, s.auditPolicy(ctx), userIDs)
	if err != nil {
		log.Printf("evaluate connection audit device actions: %v", err)
		return
	}
	targets := make(map[int64]bool, len(userIDs))
	for _, userID := range userIDs {
		targets[userID] = true
	}
	for _, connection := range overview.Users {
		if !targets[connection.UserID] {
			continue
		}
		subscription, _, err := s.store.SubscriptionAuditCurrentRisk(ctx, connection.UserID, time.Now().UTC(), s.auditPolicy(ctx))
		if err != nil {
			log.Printf("load subscription evidence for device action user=%d: %v", connection.UserID, err)
			continue
		}
		s.applyConnectionAuditDeviceAction(ctx, connection, subscription)
	}
}

func (s *Server) applyConnectionAuditDeviceAction(ctx context.Context, connection model.ConnectionAuditUserSummary, subscription model.SubscriptionAuditRisk) {
	if s.auditSettingsState(ctx).Action != model.AuditActionRestrict || connection.IdentityMode != "device_bound" || !connection.CoverageComplete || connection.RiskDeviceIDHash == "" || connection.CloneConfidence < 0.80 {
		return
	}
	evidence := uniqueAuditStrings(append(append([]string{}, connection.EvidenceCategories...), subscription.EvidenceCategories...))
	if len(evidence) < 2 {
		return
	}
	higher, lower := max(connection.RiskScore, subscription.Score), min(connection.RiskScore, subscription.Score)
	totalScore := min(100, higher+int(math.Round(0.20*float64(lower))))
	confidence := max(connection.Confidence, subscription.Confidence)
	if connection.Confidence > 0 && subscription.Confidence > 0 {
		confidence = math.Round(((connection.Confidence+subscription.Confidence)/2)*100) / 100
	}
	if totalScore < 85 || confidence < s.auditPolicy(ctx).AutoActionConfidence {
		return
	}
	device, err := s.store.GetUserDeviceByHash(ctx, connection.UserID, connection.RiskDeviceIDHash)
	if err != nil || device.Status != "active" || device.CredentialEpoch <= 0 {
		return
	}
	changed := false
	deviceID := device.ID
	if !device.SubscriptionSuspended {
		device, err = s.store.SetUserDeviceSubscriptionSuspended(ctx, connection.UserID, deviceID, true)
		if err != nil {
			log.Printf("suspend risky device subscription user=%d device=%s: %v", connection.UserID, deviceID, err)
			return
		}
		changed = true
	}
	if totalScore >= 95 && confidence >= 0.90 && device.ProxyAccessState == "active" {
		_, err = s.store.SetUserDeviceProxyAccessState(ctx, connection.UserID, deviceID, "reject_new")
		if err != nil {
			log.Printf("reject risky device authentication user=%d device=%s: %v", connection.UserID, deviceID, err)
			return
		}
		if err := s.queueUserDeviceCredentialDeployment(ctx); err != nil {
			_, _ = s.store.SetUserDeviceProxyAccessState(ctx, connection.UserID, deviceID, "active")
			log.Printf("queue risky device credential deployment user=%d device=%s: %v", connection.UserID, deviceID, err)
			return
		}
		changed = true
	}
	if changed {
		s.publishRealtime("audit", "users", "deployment", "user_overview")
	}
}

func validateConnectionAuditItem(item connectionAuditReportItem, serverID int64) (model.ConnectionAuditReport, error) {
	reportID := strings.TrimSpace(item.ReportID)
	if reportID == "" || len(reportID) > 200 || item.UserID <= 0 {
		return model.ConnectionAuditReport{}, errors.New("connection audit identity is invalid")
	}
	deviceIDHash := strings.TrimSpace(item.DeviceIDHash)
	clientInstanceIDHash := strings.TrimSpace(item.ClientInstanceIDHash)
	if len(deviceIDHash) > 128 || len(clientInstanceIDHash) > 128 || (deviceIDHash == "") != (item.CredentialEpoch == 0) || item.CredentialEpoch < 0 {
		return model.ConnectionAuditReport{}, errors.New("connection audit device identity is invalid")
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
	maxDurationMS := int64((31 * 24 * time.Hour) / time.Millisecond)
	durationBucketTotal := item.DurationLE1SCount + item.DurationLE5SCount + item.DurationLE20SCount + item.DurationGT20SCount
	if item.ConnectionCount < 0 || item.ClosedCount < 0 || item.DurationTotalMS < 0 || item.DurationMaxMS < 0 || item.DurationMaxMS > item.DurationTotalMS || item.DurationMaxMS > maxDurationMS || item.UploadBytes < 0 || item.DownloadBytes < 0 || item.UploadBytes > 1<<60 || item.DownloadBytes > 1<<60 || item.DurationLE1SCount < 0 || item.DurationLE5SCount < 0 || item.DurationLE20SCount < 0 || item.DurationGT20SCount < 0 || durationBucketTotal != item.ClosedCount || item.ActivePeak < 0 || item.ActiveAtEnd < 0 || item.ActiveAtEnd > item.ActivePeak || item.ConnectionCount > 1_000_000_000 || item.ClosedCount > 1_000_000_000 || item.DurationTotalMS > maxDurationMS*1_000_000 || item.ActivePeak > 1_000_000 || item.ClosedCount+item.ActiveAtEnd > item.ConnectionCount+item.ActivePeak {
		return model.ConnectionAuditReport{}, errors.New("connection audit counters are invalid")
	}
	probeState := strings.ToLower(strings.TrimSpace(item.ProbeState))
	switch probeState {
	case "", "normal", "candidate", "confirmed", "normal_traffic":
	default:
		return model.ConnectionAuditReport{}, errors.New("connection audit probe_state is invalid")
	}
	if item.CollectionGeneration > math.MaxInt64 || item.BucketCapacity < 1 || item.BucketCapacity > 1_000_000 || item.DroppedBucketCount < 0 || item.DroppedBucketCount > 1_000_000_000 || item.PresenceSequence == 0 {
		return model.ConnectionAuditReport{}, errors.New("connection audit collection coverage is invalid")
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
	collectionStartedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.CollectionStartedAt))
	if err != nil {
		return model.ConnectionAuditReport{}, errors.New("connection audit collection_started_at is invalid")
	}
	collectionEndedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.CollectionEndedAt))
	if err != nil || collectionEndedAt.Before(collectionStartedAt) || collectionEndedAt.After(nowTime.Add(5*time.Minute)) || collectionStartedAt.Before(nowTime.Add(-31*24*time.Hour)) {
		return model.ConnectionAuditReport{}, errors.New("connection audit collection_ended_at is invalid")
	}
	if startedAt.Before(collectionStartedAt) || endedAt.After(collectionEndedAt) {
		return model.ConnectionAuditReport{}, errors.New("connection audit event is outside its collection window")
	}
	var payloadFirstAt, payloadLastAt time.Time
	if strings.TrimSpace(item.PayloadFirstAt) != "" || strings.TrimSpace(item.PayloadLastAt) != "" {
		payloadFirstAt, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(item.PayloadFirstAt))
		if err != nil {
			return model.ConnectionAuditReport{}, errors.New("connection audit payload_first_at is invalid")
		}
		payloadLastAt, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(item.PayloadLastAt))
		if err != nil || payloadLastAt.Before(payloadFirstAt) || payloadFirstAt.Before(startedAt) || payloadLastAt.After(endedAt) {
			return model.ConnectionAuditReport{}, errors.New("connection audit payload_last_at is invalid")
		}
	}
	if (item.UploadBytes+item.DownloadBytes > 0) != !payloadFirstAt.IsZero() {
		return model.ConnectionAuditReport{}, errors.New("connection audit payload coverage is inconsistent")
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
	overview, err := s.store.ConnectionAuditOverview(r.Context(), intQuery(r, "window_hours", 24), s.connectionAuditEnabled(r.Context()), s.auditPolicy(r.Context()))
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	overview.GeoDatabase = s.geoIPStatus
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
	detail, err := s.store.ConnectionAuditUserDetail(r.Context(), userID, intQuery(r, "window_hours", 24), s.auditPolicy(r.Context()))
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	write(w, http.StatusOK, map[string]any{"connection_audit_user": detail})
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
