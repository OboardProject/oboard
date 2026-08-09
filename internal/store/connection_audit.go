package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	connectionAuditRetention   = 30 * 24 * time.Hour
	connectionAuditRiskWindow  = 15 * time.Minute
	connectionAuditPresenceTCP = 120 * time.Second
	connectionAuditPresenceUDP = 60 * time.Second
	connectionAuditProbeWindow = 20 * time.Second
)

var connectionAuditNonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func (s *Store) AddConnectionAuditReports(ctx context.Context, reports []model.ConnectionAuditReport) ([]string, error) {
	accepted := make([]string, 0, len(reports))
	if len(reports) == 0 {
		return accepted, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ts := now()
	affectedUsers := map[int64]struct{}{}
	for _, report := range reports {
		if strings.TrimSpace(report.ReportID) == "" || report.ServerID <= 0 || report.UserID <= 0 {
			continue
		}
		var payloadFirstAt, payloadLastAt any
		if !report.PayloadFirstAt.IsZero() {
			payloadFirstAt = report.PayloadFirstAt.UTC().Format(time.RFC3339Nano)
		}
		if !report.PayloadLastAt.IsZero() {
			payloadLastAt = report.PayloadLastAt.UTC().Format(time.RFC3339Nano)
		}
		res, err := tx.ExecContext(ctx, `insert or ignore into connection_audit_reports(
			report_id,server_id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,client_instance_id_hash,
			source_ip,route_id,source_geo_code,source_country_code,source_country,source_province,source_city,source_isp,geo_database_revision,
			network,destination,destination_port,outbound_tag,outbound_type,connection_count,closed_count,duration_total_ms,duration_max_ms,
			upload_bytes,download_bytes,payload_first_at,payload_last_at,duration_le_1s_count,duration_le_5s_count,duration_le_20s_count,duration_gt_20s_count,
			probe_state,internal_probe,presence_sequence,active_peak,active_at_end,collection_generation,bucket_capacity,dropped_bucket_count,
			collection_started_at,collection_ended_at,started_at,ended_at,created_at)
			values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			report.ReportID, report.ServerID, report.UserID, report.InboundID, report.PathID, report.DeviceIDHash, report.CredentialEpoch, report.ClientInstanceIDHash,
			report.SourceIP, report.RouteID, report.SourceGeoCode, report.SourceCountryCode, report.SourceCountry, report.SourceProvince, report.SourceCity, report.SourceISP, report.GeoDatabaseRevision,
			report.Network, report.Destination, report.DestinationPort, report.OutboundTag, report.OutboundType, report.ConnectionCount, report.ClosedCount, report.DurationTotalMS, report.DurationMaxMS,
			report.UploadBytes, report.DownloadBytes, payloadFirstAt, payloadLastAt, report.DurationLE1SCount, report.DurationLE5SCount, report.DurationLE20SCount, report.DurationGT20SCount,
			report.ProbeState, boolInt(report.InternalProbe), report.PresenceSequence, report.ActivePeak, report.ActiveAtEnd, report.CollectionGeneration, report.BucketCapacity, report.DroppedBucketCount,
			report.CollectionStartedAt.UTC().Format(time.RFC3339Nano), report.CollectionEndedAt.UTC().Format(time.RFC3339Nano), report.StartedAt.UTC().Format(time.RFC3339Nano), report.EndedAt.UTC().Format(time.RFC3339Nano), ts)
		if err != nil {
			return nil, err
		}
		if _, err := res.RowsAffected(); err != nil {
			return nil, err
		}
		accepted = append(accepted, report.ReportID)
		affectedUsers[report.UserID] = struct{}{}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for userID := range affectedUsers {
		if err := s.refreshConnectionProbeEpisodes(ctx, userID, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	return accepted, nil
}

func (s *Store) ConnectionAuditOverview(ctx context.Context, windowHours int, connectionAuditEnabled bool, policy model.AuditPolicy) (model.ConnectionAuditOverview, error) {
	if windowHours < 1 {
		windowHours = 24
	}
	if windowHours > 30*24 {
		windowHours = 30 * 24
	}
	nowTime := time.Now().UTC()
	since := nowTime.Add(-time.Duration(windowHours) * time.Hour).Format(time.RFC3339Nano)
	if ValidateAuditPolicy(policy) != nil {
		policy = DefaultAuditPolicy()
	}
	overview := model.ConnectionAuditOverview{WindowHours: windowHours, RiskWindowMinutes: int(connectionAuditRiskWindow / time.Minute), GeneratedAt: nowTime, Policy: policy, Users: []model.ConnectionAuditUserSummary{}}
	if err := s.db.QueryRowContext(ctx, `select count(*) from servers where connection_audit_enabled=1`).Scan(&overview.EnabledServerCount); err != nil {
		return overview, err
	}
	if !connectionAuditEnabled {
		overview.EnabledServerCount = 0
	}
	rows, err := s.db.QueryContext(ctx, `select r.user_id,u.username,u.nickname,count(distinct r.source_ip),count(distinct r.server_id),coalesce(sum(r.connection_count),0),coalesce(max(r.active_peak),0),count(*),max(r.ended_at),coalesce(u.device_limit,0),(select count(*) from user_devices d where d.user_id=u.id and d.status='active')
		from connection_audit_reports r join users u on u.id=r.user_id where r.ended_at>=? group by r.user_id,u.username,u.nickname`, since)
	if err != nil {
		return overview, err
	}
	for rows.Next() {
		var item model.ConnectionAuditUserSummary
		var lastSeen string
		if err := rows.Scan(&item.UserID, &item.Username, &item.Nickname, &item.SourceIPCount, &item.ServerCount, &item.ConnectionCount, &item.ActivePeak, &item.ReportCount, &lastSeen, &item.DeviceLimit, &item.RegisteredDeviceCount); err != nil {
			return overview, errors.Join(err, rows.Close())
		}
		item.LastSeenAt = parseTime(lastSeen)
		overview.Users = append(overview.Users, item)
	}
	if err := rows.Close(); err != nil {
		return overview, err
	}
	if err := rows.Err(); err != nil {
		return overview, err
	}
	usersByID := make(map[int64]int, len(overview.Users))
	for index := range overview.Users {
		usersByID[overview.Users[index].UserID] = index
	}
	presenceByUser := map[int64][]model.ConnectionPresenceEvent{}
	presenceRows, err := s.db.QueryContext(ctx, `select p.server_id,p.user_id,p.inbound_id,p.path_id,p.device_id_hash,p.credential_epoch,p.source_ip,p.route_id,p.network,p.active_connections,p.meaningful,p.payload_last_at,p.last_event_at,p.last_sequence,p.updated_at,u.username,u.nickname,coalesce(u.device_limit,0),(select count(*) from user_devices d where d.user_id=u.id and d.status='active') from connection_presence_states p join users u on u.id=p.user_id where p.last_event_at>=? order by p.user_id,p.device_id_hash,p.source_ip,p.network`, nowTime.Add(-connectionAuditPresenceTCP).Format(time.RFC3339Nano))
	if err != nil {
		return overview, err
	}
	for presenceRows.Next() {
		var event model.ConnectionPresenceEvent
		var meaningful int
		var payloadLastAt *string
		var at, updatedAt string
		var username, nickname string
		var deviceLimit, registeredDevices int
		if err := presenceRows.Scan(&event.ServerID, &event.UserID, &event.InboundID, &event.PathID, &event.DeviceIDHash, &event.CredentialEpoch, &event.SourceIP, &event.RouteID, &event.Network, &event.ActiveConnections, &meaningful, &payloadLastAt, &at, &event.Sequence, &updatedAt, &username, &nickname, &deviceLimit, &registeredDevices); err != nil {
			return overview, errors.Join(err, presenceRows.Close())
		}
		event.Event = "current"
		event.Meaningful = meaningful == 1
		if event.ActiveConnections > 0 {
			event.State = "active"
		} else {
			event.State = "inactive"
		}
		if payloadLastAt != nil {
			event.PayloadLastAt = parseTime(*payloadLastAt)
		}
		event.At = parseTime(at)
		event.CreatedAt = parseTime(updatedAt)
		presenceByUser[event.UserID] = append(presenceByUser[event.UserID], event)
		if _, exists := usersByID[event.UserID]; !exists {
			usersByID[event.UserID] = len(overview.Users)
			overview.Users = append(overview.Users, model.ConnectionAuditUserSummary{UserID: event.UserID, Username: username, Nickname: nickname, DeviceLimit: deviceLimit, RegisteredDeviceCount: registeredDevices, LastSeenAt: event.At})
		}
	}
	if err := presenceRows.Close(); err != nil {
		return overview, err
	}
	if err := presenceRows.Err(); err != nil {
		return overview, err
	}

	usersByIP := map[string]map[int64]struct{}{}
	sharedIPsByUser := map[int64]map[string]struct{}{}
	subnetsByUser := map[int64]map[string]struct{}{}
	ipRows, err := s.db.QueryContext(ctx, `select distinct user_id,source_ip from connection_audit_reports where ended_at>=?`, since)
	if err != nil {
		return overview, err
	}
	for ipRows.Next() {
		var userID int64
		var sourceIP string
		if err := ipRows.Scan(&userID, &sourceIP); err != nil {
			return overview, errors.Join(err, ipRows.Close())
		}
		if usersByIP[sourceIP] == nil {
			usersByIP[sourceIP] = map[int64]struct{}{}
		}
		usersByIP[sourceIP][userID] = struct{}{}
		subnet := auditSubnet(sourceIP)
		if subnet == "" {
			continue
		}
		if subnetsByUser[userID] == nil {
			subnetsByUser[userID] = map[string]struct{}{}
		}
		subnetsByUser[userID][subnet] = struct{}{}
	}
	if err := ipRows.Close(); err != nil {
		return overview, err
	}
	if err := ipRows.Err(); err != nil {
		return overview, err
	}
	for sourceIP, users := range usersByIP {
		if len(users) < 2 {
			continue
		}
		for userID := range users {
			if sharedIPsByUser[userID] == nil {
				sharedIPsByUser[userID] = map[string]struct{}{}
			}
			sharedIPsByUser[userID][sourceIP] = struct{}{}
		}
	}
	sharedRoutes, err := s.connectionAuditSharedRouteUsers(ctx, nowTime.Add(-connectionAuditRiskWindow))
	if err != nil {
		return overview, err
	}
	for index := range overview.Users {
		item := &overview.Users[index]
		presence := presenceByUser[item.UserID]
		for _, event := range presence {
			item.ActiveConnectionCount += event.ActiveConnections
			if event.At.After(item.LastSeenAt) {
				item.LastSeenAt = event.At
			}
		}
		item.SourceSubnetCount = len(subnetsByUser[item.UserID])
		item.SharedSourceIPCount = len(sharedIPsByUser[item.UserID])
		selectedReports, loadErr := s.listConnectionAuditReportsForRisk(ctx, item.UserID, nowTime.Add(-time.Duration(windowHours)*time.Hour), 50000)
		if loadErr != nil {
			return overview, loadErr
		}
		item.SourceRegionCount = connectionAuditDistinctCountries(selectedReports)
		episodes, loadErr := s.listConnectionProbeEpisodes(ctx, item.UserID, nowTime.Add(-time.Duration(windowHours)*time.Hour), 200)
		if loadErr != nil {
			return overview, loadErr
		}
		events := buildConnectionAuditRiskEvents(selectedReports, policy, sharedRoutes)
		var strongest *model.ConnectionAuditRiskEvent
		for eventIndex := range events {
			event := &events[eventIndex]
			if strongest == nil || strongerConnectionAuditRiskEvent(*event, *strongest) {
				strongest = event
			}
		}
		if strongest != nil {
			item.RiskSourceIPCount = strongest.SourceIPCount
			item.RiskRegionCount = strongest.RegionCount
			item.RiskRegions = append([]string(nil), strongest.Regions...)
			startedAt, endedAt := strongest.StartedAt, strongest.EndedAt
			item.RiskWindowStartedAt = &startedAt
			item.RiskWindowEndedAt = &endedAt
		}
		robustZ, loadErr := s.connectionAuditRobustZ(ctx, item.UserID, nowTime)
		if loadErr != nil {
			return overview, loadErr
		}
		evaluateConnectionAuditRisk(item, selectedReports, robustZ, presence, episodes, policy, strongest, sharedRoutes, nowTime)
		overview.TotalConnections += item.ConnectionCount
		if item.RiskScore >= 55 {
			overview.ElevatedRiskCount++
		}
	}
	overview.ReportingUserCount = len(overview.Users)
	overview.UniqueSourceIPs = len(usersByIP)
	sort.SliceStable(overview.Users, func(i, j int) bool {
		if overview.Users[i].RiskScore != overview.Users[j].RiskScore {
			return overview.Users[i].RiskScore > overview.Users[j].RiskScore
		}
		return overview.Users[i].LastSeenAt.After(overview.Users[j].LastSeenAt)
	})
	return overview, nil
}

func (s *Store) ConnectionAuditUserDetail(ctx context.Context, userID int64, windowHours int, policy model.AuditPolicy) (model.ConnectionAuditUserDetail, error) {
	nowTime := time.Now().UTC()
	summary, err := s.ConnectionAuditUserRisk(ctx, userID, windowHours, policy, nowTime)
	if err != nil {
		return model.ConnectionAuditUserDetail{}, err
	}
	detail := model.ConnectionAuditUserDetail{
		Sources: []model.ConnectionAuditDimension{}, Destinations: []model.ConnectionAuditDimension{},
		Outbounds: []model.ConnectionAuditDimension{}, Servers: []model.ConnectionAuditDimension{}, Recent: []model.ConnectionAuditReport{}, RiskEvents: []model.ConnectionAuditRiskEvent{}, ProbeEpisodes: []model.ConnectionProbeEpisode{}, Presence: []model.ConnectionPresenceEvent{},
	}
	detail.Summary = *summary
	if windowHours < 1 {
		windowHours = 24
	}
	if windowHours > 30*24 {
		windowHours = 30 * 24
	}
	since := nowTime.Add(-time.Duration(windowHours) * time.Hour).Format(time.RFC3339Nano)
	detail.Sources, err = s.connectionAuditDimensions(ctx, `select source_ip,source_ip,trim(case when coalesce(max(source_province),'')<>'' then max(source_province) else coalesce(max(source_country),'') end||case when coalesce(max(source_city),'')='' then '' else ' / '||max(source_city) end||case when coalesce(max(source_isp),'')='' then '' else ' / '||max(source_isp) end),sum(connection_count),max(active_peak),max(ended_at) from connection_audit_reports where user_id=? and ended_at>=? group by source_ip order by sum(connection_count) desc limit 20`, userID, since)
	if err != nil {
		return detail, err
	}
	detail.Destinations, err = s.connectionAuditDimensions(ctx, `select destination||':'||destination_port,destination,cast(destination_port as text),sum(connection_count),max(active_peak),max(ended_at) from connection_audit_reports where user_id=? and ended_at>=? and destination!='' group by destination,destination_port order by sum(connection_count) desc limit 20`, userID, since)
	if err != nil {
		return detail, err
	}
	detail.Outbounds, err = s.connectionAuditDimensions(ctx, `select outbound_tag||':'||outbound_type,case when outbound_tag='' then outbound_type else outbound_tag end,outbound_type,sum(connection_count),max(active_peak),max(ended_at) from connection_audit_reports where user_id=? and ended_at>=? and (outbound_tag!='' or outbound_type!='') group by outbound_tag,outbound_type order by sum(connection_count) desc limit 20`, userID, since)
	if err != nil {
		return detail, err
	}
	detail.Servers, err = s.connectionAuditDimensions(ctx, `select cast(r.server_id as text),s.name,'',sum(r.connection_count),max(r.active_peak),max(r.ended_at) from connection_audit_reports r join servers s on s.id=r.server_id where r.user_id=? and r.ended_at>=? group by r.server_id,s.name order by sum(r.connection_count) desc limit 20`, userID, since)
	if err != nil {
		return detail, err
	}
	detail.Recent, err = s.listRecentConnectionAudits(ctx, userID, since, 100)
	if err != nil {
		return detail, err
	}
	reports, err := s.listConnectionAuditReportsForRisk(ctx, userID, parseTime(since), 50000)
	if err != nil {
		return detail, err
	}
	sharedRoutes, err := s.connectionAuditSharedRouteUsers(ctx, nowTime.Add(-connectionAuditRiskWindow))
	if err != nil {
		return detail, err
	}
	detail.RiskEvents = buildConnectionAuditRiskEvents(reports, policy, sharedRoutes)
	sort.SliceStable(detail.RiskEvents, func(i, j int) bool { return detail.RiskEvents[i].EndedAt.After(detail.RiskEvents[j].EndedAt) })
	if len(detail.RiskEvents) > 20 {
		detail.RiskEvents = detail.RiskEvents[:20]
	}
	detail.ProbeEpisodes, err = s.listConnectionProbeEpisodes(ctx, userID, parseTime(since), 100)
	if err != nil {
		return detail, err
	}
	detail.Presence, err = s.ListConnectionPresenceForUser(ctx, userID, nowTime.Add(-connectionAuditPresenceTCP))
	return detail, err
}

// ConnectionAuditUserRisk evaluates the deterministic connection risk for a
// single user with the same engine used by ConnectionAuditOverview, without
// materializing the summaries of every other user. It returns sql.ErrNoRows
// when the user has no connection audit reports and no presence state.
func (s *Store) ConnectionAuditUserRisk(ctx context.Context, userID int64, windowHours int, policy model.AuditPolicy, at time.Time) (*model.ConnectionAuditUserSummary, error) {
	if windowHours < 1 {
		windowHours = 24
	}
	if windowHours > 30*24 {
		windowHours = 30 * 24
	}
	if ValidateAuditPolicy(policy) != nil {
		policy = DefaultAuditPolicy()
	}
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	since := at.Add(-time.Duration(windowHours) * time.Hour)
	sinceText := since.UTC().Format(time.RFC3339Nano)
	var item model.ConnectionAuditUserSummary
	var lastSeen sql.NullString
	err := s.db.QueryRowContext(ctx, `select u.id,u.username,u.nickname,
		coalesce((select count(distinct r.source_ip) from connection_audit_reports r where r.user_id=u.id and r.ended_at>=? and r.source_ip<>''),0),
		coalesce((select count(distinct r.server_id) from connection_audit_reports r where r.user_id=u.id and r.ended_at>=?),0),
		coalesce((select sum(r.connection_count) from connection_audit_reports r where r.user_id=u.id and r.ended_at>=?),0),
		coalesce((select max(r.active_peak) from connection_audit_reports r where r.user_id=u.id and r.ended_at>=?),0),
		coalesce((select count(*) from connection_audit_reports r where r.user_id=u.id and r.ended_at>=?),0),
		(select max(r.ended_at) from connection_audit_reports r where r.user_id=u.id and r.ended_at>=?),
		coalesce(u.device_limit,0),
		(select count(*) from user_devices d where d.user_id=u.id and d.status='active')
		from users u where u.id=?`, sinceText, sinceText, sinceText, sinceText, sinceText, sinceText, userID).Scan(&item.UserID, &item.Username, &item.Nickname, &item.SourceIPCount, &item.ServerCount, &item.ConnectionCount, &item.ActivePeak, &item.ReportCount, &lastSeen, &item.DeviceLimit, &item.RegisteredDeviceCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, err
	}
	if lastSeen.Valid {
		item.LastSeenAt = parseTime(lastSeen.String)
	}
	presence, err := s.ListConnectionPresenceForUser(ctx, userID, at.Add(-connectionAuditPresenceTCP))
	if err != nil {
		return nil, err
	}
	reports, err := s.listConnectionAuditReportsForRisk(ctx, userID, since, 50000)
	if err != nil {
		return nil, err
	}
	if item.ReportCount == 0 && len(presence) == 0 {
		return nil, sql.ErrNoRows
	}
	selected := reports
	subnets := map[string]struct{}{}
	for _, report := range selected {
		if subnet := auditSubnet(report.SourceIP); subnet != "" {
			subnets[subnet] = struct{}{}
		}
	}
	item.SourceSubnetCount = len(subnets)
	if err := s.db.QueryRowContext(ctx, `select count(*) from (
		select r.source_ip from connection_audit_reports r
		where r.user_id=? and r.ended_at>=? and exists(
			select 1 from connection_audit_reports shared where shared.source_ip=r.source_ip and shared.user_id<>r.user_id and shared.ended_at>=?
		) group by r.source_ip
	)`, userID, sinceText, sinceText).Scan(&item.SharedSourceIPCount); err != nil {
		return nil, err
	}
	for _, event := range presence {
		item.ActiveConnectionCount += event.ActiveConnections
		if event.At.After(item.LastSeenAt) {
			item.LastSeenAt = event.At
		}
	}
	episodes, err := s.listConnectionProbeEpisodes(ctx, userID, since, 100)
	if err != nil {
		return nil, err
	}
	sharedRoutes, err := s.connectionAuditSharedRouteUsers(ctx, at.Add(-connectionAuditRiskWindow))
	if err != nil {
		return nil, err
	}
	events := buildConnectionAuditRiskEvents(selected, policy, sharedRoutes)
	var strongest *model.ConnectionAuditRiskEvent
	for index := range events {
		event := &events[index]
		if strongest == nil || strongerConnectionAuditRiskEvent(*event, *strongest) {
			strongest = event
		}
	}
	if strongest != nil {
		item.RiskSourceIPCount = strongest.SourceIPCount
		item.RiskRegionCount = strongest.RegionCount
		item.RiskRegions = append([]string(nil), strongest.Regions...)
		startedAt, endedAt := strongest.StartedAt, strongest.EndedAt
		item.RiskWindowStartedAt = &startedAt
		item.RiskWindowEndedAt = &endedAt
	}
	if item.ReportCount > 0 {
		item.SourceRegionCount = connectionAuditDistinctCountries(selected)
	}
	robustZ, err := s.connectionAuditRobustZ(ctx, userID, at)
	if err != nil {
		return nil, err
	}
	evaluateConnectionAuditRisk(&item, selected, robustZ, presence, episodes, policy, strongest, sharedRoutes, at)
	return &item, nil
}

func (s *Store) connectionAuditDimensions(ctx context.Context, query string, args ...any) ([]model.ConnectionAuditDimension, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ConnectionAuditDimension{}
	for rows.Next() {
		var item model.ConnectionAuditDimension
		var lastSeen string
		if err := rows.Scan(&item.Key, &item.Label, &item.Secondary, &item.ConnectionCount, &item.ActivePeak, &lastSeen); err != nil {
			return nil, err
		}
		item.LastSeenAt = parseTime(lastSeen)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) listRecentConnectionAudits(ctx context.Context, userID int64, since string, limit int) ([]model.ConnectionAuditReport, error) {
	return s.listConnectionAuditsByTime(ctx, userID, "ended_at", since, limit)
}

func (s *Store) listConnectionAuditsByTime(ctx context.Context, userID int64, timeColumn, since string, limit int) ([]model.ConnectionAuditReport, error) {
	if timeColumn != "ended_at" && timeColumn != "started_at" {
		return nil, fmt.Errorf("unsupported connection audit time column %q", timeColumn)
	}
	rows, err := s.db.QueryContext(ctx, `select
		report_id,server_id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,client_instance_id_hash,
		source_ip,route_id,source_geo_code,source_country_code,source_country,source_province,source_city,source_isp,geo_database_revision,
		network,destination,destination_port,outbound_tag,outbound_type,connection_count,closed_count,duration_total_ms,duration_max_ms,
		upload_bytes,download_bytes,payload_first_at,payload_last_at,duration_le_1s_count,duration_le_5s_count,duration_le_20s_count,duration_gt_20s_count,
		probe_state,internal_probe,presence_sequence,active_peak,active_at_end,collection_generation,bucket_capacity,dropped_bucket_count,
		collection_started_at,collection_ended_at,started_at,ended_at,created_at
		from connection_audit_reports where user_id=? and `+timeColumn+`>=? order by `+timeColumn+` desc limit ?`, userID, since, limit) // #nosec G202 -- timeColumn is restricted to ended_at or started_at above.
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ConnectionAuditReport{}
	for rows.Next() {
		var item model.ConnectionAuditReport
		var inboundID, pathID sql.NullInt64
		var collectionStartedAt, collectionEndedAt, startedAt, endedAt, createdAt string
		var payloadFirstAt, payloadLastAt sql.NullString
		var internalProbe int
		if err := rows.Scan(
			&item.ReportID, &item.ServerID, &item.UserID, &inboundID, &pathID, &item.DeviceIDHash, &item.CredentialEpoch, &item.ClientInstanceIDHash,
			&item.SourceIP, &item.RouteID, &item.SourceGeoCode, &item.SourceCountryCode, &item.SourceCountry, &item.SourceProvince, &item.SourceCity, &item.SourceISP, &item.GeoDatabaseRevision,
			&item.Network, &item.Destination, &item.DestinationPort, &item.OutboundTag, &item.OutboundType, &item.ConnectionCount, &item.ClosedCount, &item.DurationTotalMS, &item.DurationMaxMS,
			&item.UploadBytes, &item.DownloadBytes, &payloadFirstAt, &payloadLastAt, &item.DurationLE1SCount, &item.DurationLE5SCount, &item.DurationLE20SCount, &item.DurationGT20SCount,
			&item.ProbeState, &internalProbe, &item.PresenceSequence, &item.ActivePeak, &item.ActiveAtEnd, &item.CollectionGeneration, &item.BucketCapacity, &item.DroppedBucketCount,
			&collectionStartedAt, &collectionEndedAt, &startedAt, &endedAt, &createdAt,
		); err != nil {
			return nil, err
		}
		if inboundID.Valid {
			item.InboundID = &inboundID.Int64
		}
		if pathID.Valid {
			item.PathID = &pathID.Int64
		}
		item.StartedAt = parseTime(startedAt)
		item.EndedAt = parseTime(endedAt)
		item.CollectionStartedAt = parseTime(collectionStartedAt)
		item.CollectionEndedAt = parseTime(collectionEndedAt)
		if parsed := parseNullTime(payloadFirstAt); parsed != nil {
			item.PayloadFirstAt = *parsed
		}
		if parsed := parseNullTime(payloadLastAt); parsed != nil {
			item.PayloadLastAt = *parsed
		}
		item.InternalProbe = internalProbe != 0
		item.CreatedAt = parseTime(createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ConnectionAuditSourceIPsForGeoRevision(ctx context.Context, revision string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `select distinct source_ip from connection_audit_reports where geo_database_revision<>? order by source_ip`, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var sourceIP string
		if err := rows.Scan(&sourceIP); err != nil {
			return nil, err
		}
		out = append(out, sourceIP)
	}
	return out, rows.Err()
}

func (s *Store) UpdateConnectionAuditSourceGeography(ctx context.Context, sourceIP string, geo model.IPGeography) error {
	_, err := s.db.ExecContext(ctx, `update connection_audit_reports set source_country_code=?,source_country=?,source_province=?,source_city=?,source_isp=?,geo_database_revision=? where source_ip=?`, geo.CountryCode, geo.Country, geo.Province, geo.City, geo.ISP, geo.Revision, sourceIP)
	return err
}

type connectionAuditIdentity struct {
	UserID       int64
	DeviceIDHash string
	LegacyKey    string
}

func (identity connectionAuditIdentity) key() string {
	return fmt.Sprintf("%d\x00%s\x00%s", identity.UserID, identity.DeviceIDHash, identity.LegacyKey)
}

func connectionAuditReportIdentity(report model.ConnectionAuditReport) connectionAuditIdentity {
	identity := connectionAuditIdentity{UserID: report.UserID, DeviceIDHash: strings.TrimSpace(report.DeviceIDHash)}
	if identity.DeviceIDHash != "" {
		return identity
	}
	identity.LegacyKey = auditSubnet(report.SourceIP)
	if identity.LegacyKey == "" {
		identity.LegacyKey = strings.TrimSpace(report.SourceIP)
	}
	return identity
}

func connectionAuditPublicSourceIP(raw string) bool {
	ip, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range connectionAuditNonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func (s *Store) ConnectionAuditCurrentRisk(ctx context.Context, userID int64, at time.Time, policy model.AuditPolicy) (*model.ConnectionAuditRiskEvent, error) {
	if userID <= 0 {
		return nil, nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	reports, err := s.listConnectionAuditReportsForRisk(ctx, userID, at.UTC().Add(-connectionAuditRiskWindow), 10000)
	if err != nil {
		return nil, err
	}
	sharedRoutes, err := s.connectionAuditSharedRouteUsers(ctx, at.UTC().Add(-connectionAuditRiskWindow))
	if err != nil {
		return nil, err
	}
	events := buildConnectionAuditRiskEvents(reports, policy, sharedRoutes)
	var strongest *model.ConnectionAuditRiskEvent
	for index := range events {
		event := &events[index]
		if event.EndedAt.Before(at.Add(-connectionAuditRiskWindow)) || event.EndedAt.After(at.Add(5*time.Minute)) {
			continue
		}
		if strongest == nil || strongerConnectionAuditRiskEvent(*event, *strongest) {
			copy := *event
			strongest = &copy
		}
	}
	return strongest, nil
}

func connectionAuditRegion(countryCode, country, province string) (string, string) {
	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	country = strings.TrimSpace(country)
	province = strings.TrimSpace(province)
	if countryCode == "CN" {
		if province == "" {
			return "", ""
		}
		return "CN/" + province, province
	}
	if countryCode == "" {
		return "", ""
	}
	if country == "" {
		country = countryCode
	}
	return "COUNTRY/" + countryCode, country
}

func connectionAuditCountryRegion(countryCode, country string) (string, string) {
	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	if countryCode == "" {
		return "", ""
	}
	country = strings.TrimSpace(country)
	if country == "" {
		country = countryCode
	}
	return "COUNTRY/" + countryCode, country
}

type connectionAuditPayloadInterval struct {
	report     model.ConnectionAuditReport
	networkKey string
	start      time.Time
	end        time.Time
}

func buildConnectionAuditRiskEvents(reports []model.ConnectionAuditReport, policy model.AuditPolicy, sharedRoutes map[string]int) []model.ConnectionAuditRiskEvent {
	byDevice := map[string][]connectionAuditPayloadInterval{}
	for _, report := range reports {
		if report.DeviceIDHash == "" || !connectionAuditMeaningfulReport(report) {
			continue
		}
		networkKey := connectionAuditIndependentNetwork(report)
		if networkKey == "" {
			continue
		}
		byDevice[report.DeviceIDHash] = append(byDevice[report.DeviceIDHash], connectionAuditPayloadInterval{report: report, networkKey: networkKey, start: report.PayloadFirstAt, end: report.PayloadLastAt})
	}
	events := []model.ConnectionAuditRiskEvent{}
	for deviceID, intervals := range byDevice {
		sort.SliceStable(intervals, func(i, j int) bool { return intervals[i].start.Before(intervals[j].start) })
		for _, marker := range intervals {
			networkEnds := map[string]time.Time{}
			routes := map[string]struct{}{}
			ips := map[string]struct{}{}
			regions := map[string]string{}
			sharedCount := 0
			for _, candidate := range intervals {
				if candidate.start.After(marker.start) || candidate.end.Before(marker.start) {
					continue
				}
				if candidate.end.After(networkEnds[candidate.networkKey]) {
					networkEnds[candidate.networkKey] = candidate.end
				}
				if candidate.report.RouteID != "" {
					routes[candidate.report.RouteID] = struct{}{}
					if sharedRoutes[candidate.report.RouteID] > 1 {
						sharedCount++
					}
				}
				ips[candidate.report.SourceIP] = struct{}{}
				key, label := connectionAuditCountryRegion(candidate.report.SourceCountryCode, candidate.report.SourceCountry)
				if key != "" {
					regions[key] = label
				}
			}
			if len(networkEnds) < 2 {
				continue
			}
			commonEnd := time.Time{}
			for _, end := range networkEnds {
				if commonEnd.IsZero() || end.Before(commonEnd) {
					commonEnd = end
				}
			}
			overlap := commonEnd.Sub(marker.start)
			required := time.Duration(policy.CloneOverlapSeconds) * time.Second
			if len(networkEnds) >= 3 && required > 30*time.Second {
				required = 30 * time.Second
			}
			if overlap < required {
				continue
			}
			cloneConfidence := 0.80
			if len(networkEnds) >= 3 {
				cloneConfidence = 1
			}
			regionLabels := make([]string, 0, len(regions))
			for _, label := range regions {
				regionLabels = append(regionLabels, label)
			}
			sort.Strings(regionLabels)
			geoNorm := normalizedAuditValue(float64(len(networkEnds)), policy.ConcurrentRoutes90Secs)
			if sharedCount > 0 {
				geoNorm *= math.Max(0.20, 1/math.Sqrt(float64(sharedCount+1)))
			}
			score := min(100, int(math.Round(45*cloneConfidence+15*geoNorm)))
			events = append(events, model.ConnectionAuditRiskEvent{
				Kind: "device_clone", Level: auditRiskLevel(score), Score: score, DeviceIDHash: deviceID,
				SourceIPCount: len(ips), RegionCount: len(regions), Regions: regionLabels, RouteCount: max(len(routes), len(networkEnds)),
				OverlapSecs: int(overlap / time.Second), CloneConfidence: cloneConfidence, StartedAt: marker.start, EndedAt: commonEnd,
			})
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].DeviceIDHash != events[j].DeviceIDHash {
			return events[i].DeviceIDHash < events[j].DeviceIDHash
		}
		if !events[i].StartedAt.Equal(events[j].StartedAt) {
			return events[i].StartedAt.Before(events[j].StartedAt)
		}
		return events[i].EndedAt.Before(events[j].EndedAt)
	})
	compacted := compactConnectionAuditRiskEvents(events)
	sort.SliceStable(compacted, func(i, j int) bool { return compacted[i].EndedAt.After(compacted[j].EndedAt) })
	return compacted
}

func compactConnectionAuditRiskEvents(events []model.ConnectionAuditRiskEvent) []model.ConnectionAuditRiskEvent {
	out := make([]model.ConnectionAuditRiskEvent, 0, len(events))
	for _, event := range events {
		merged := false
		for index := range out {
			current := &out[index]
			if current.DeviceIDHash != event.DeviceIDHash || event.StartedAt.After(current.EndedAt) || current.StartedAt.After(event.EndedAt) {
				continue
			}
			startedAt, endedAt := current.StartedAt, current.EndedAt
			if event.StartedAt.Before(startedAt) {
				startedAt = event.StartedAt
			}
			if event.EndedAt.After(endedAt) {
				endedAt = event.EndedAt
			}
			if strongerConnectionAuditRiskEvent(event, *current) {
				*current = event
			}
			current.StartedAt, current.EndedAt = startedAt, endedAt
			merged = true
			break
		}
		if !merged {
			out = append(out, event)
		}
	}
	return out
}

func strongerConnectionAuditRiskEvent(left, right model.ConnectionAuditRiskEvent) bool {
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	if left.RegionCount != right.RegionCount {
		return left.RegionCount > right.RegionCount
	}
	if left.SourceIPCount != right.SourceIPCount {
		return left.SourceIPCount > right.SourceIPCount
	}
	return left.EndedAt.After(right.EndedAt)
}

func auditSubnet(raw string) string {
	ip, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	ip = ip.Unmap()
	bits := 48
	if ip.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(ip, bits).Masked().String()
}

func evaluateConnectionAuditRisk(item *model.ConnectionAuditUserSummary, selectedReports []model.ConnectionAuditReport, robustZ float64, presence []model.ConnectionPresenceEvent, episodes []model.ConnectionProbeEpisode, policy model.AuditPolicy, strongest *model.ConnectionAuditRiskEvent, sharedRoutes map[string]int, at time.Time) {
	if item == nil {
		return
	}
	item.RiskSignals = []string{}
	item.EvidenceCategories = []string{}
	item.CounterEvidence = []string{}
	identityQuality := connectionAuditOnlineDevices(item, selectedReports, presence, at)
	item.CoverageQuality, item.CoverageComplete = connectionAuditCoverage(selectedReports)
	item.ProbeEpisodeCount = 0
	probeRecent := 0
	for _, episode := range episodes {
		if episode.State != "confirmed" {
			continue
		}
		item.ProbeEpisodeCount++
		if !episode.EndedAt.Before(at.Add(-10 * time.Minute)) {
			probeRecent++
		}
	}
	cloneNorm := 0.0
	geoNorm := 0.0
	if strongest != nil {
		item.CloneConfidence = strongest.CloneConfidence
		item.RiskDeviceIDHash = strongest.DeviceIDHash
		item.ConcurrentRouteCount = strongest.RouteCount
		cloneNorm = strongest.CloneConfidence
		geoNorm = normalizedAuditValue(float64(strongest.RouteCount), policy.ConcurrentRoutes90Secs)
		item.RiskSignals = append(item.RiskSignals, fmt.Sprintf("同一设备凭证在 %d 条独立网络上重叠传输 %d 秒", strongest.RouteCount, strongest.OverlapSecs))
		item.EvidenceCategories = append(item.EvidenceCategories, "device_clone")
	}
	devNorm := 0.0
	if item.DeviceLimit > 0 {
		if item.IdentityMode == "device_bound" || item.IdentityMode == "mixed" {
			devNorm = normalizedAuditValue(float64(item.RegisteredDeviceCount-item.DeviceLimit), model.AuditThreshold{Soft: 0, Hard: 1})
		} else {
			devNorm = normalizedAuditValue(item.OnlineDeviceEstimate-float64(item.DeviceLimit), policy.LegacyDeviceExcess)
		}
		if devNorm > 0 {
			item.RiskSignals = append(item.RiskSignals, fmt.Sprintf("在线设备估计 %.1f，套餐上限 %d", item.OnlineDeviceEstimate, item.DeviceLimit))
			item.EvidenceCategories = append(item.EvidenceCategories, "device_limit")
		}
	}
	item.NodeFanout = connectionAuditNodeFanout(selectedReports)
	fanoutNorm := normalizedAuditValue(float64(item.NodeFanout), policy.NodeFanout10Seconds)
	if fanoutNorm > 0 {
		item.RiskSignals = append(item.RiskSignals, fmt.Sprintf("排除测速后 10 秒节点扇出达到 %d", item.NodeFanout))
		item.EvidenceCategories = append(item.EvidenceCategories, "node_fanout")
	}
	item.RobustZ = robustZ
	anomalyNorm := clampFloat((item.RobustZ-6)/6, 0, 1)
	if anomalyNorm > 0 {
		item.RiskSignals = append(item.RiskSignals, fmt.Sprintf("连接行为相对 28 天同时段基线异常（Robust Z %.1f）", item.RobustZ))
		item.EvidenceCategories = append(item.EvidenceCategories, "historical_anomaly")
	}
	activePeak := connectionAuditNonProbeActivePeak(selectedReports)
	resourceNorm := normalizedAuditValue(float64(activePeak), policy.ActiveConnections)
	resourceNorm = math.Max(resourceNorm, normalizedAuditValue(float64(probeRecent), policy.ProbeEpisodes10Minutes))
	item.ResourcePressure = roundAuditFloat(resourceNorm)
	if resourceNorm > 0 {
		item.RiskSignals = append(item.RiskSignals, fmt.Sprintf("单设备资源并发压力达到 %d 个活跃连接", activePeak))
		item.EvidenceCategories = append(item.EvidenceCategories, "resource_pressure")
	}
	item.RiskScore = min(100, int(math.Round(45*cloneNorm+30*devNorm+15*geoNorm+10*fanoutNorm+15*anomalyNorm+10*resourceNorm)))
	item.RiskLevel = auditRiskLevel(item.RiskScore)
	if item.ProbeEpisodeCount > 0 {
		item.CounterEvidence = append(item.CounterEvidence, fmt.Sprintf("已确认并排除 %d 次全节点测速", item.ProbeEpisodeCount))
	}
	if item.SharedSourceIPCount > 0 || connectionAuditHasSharedRoute(selectedReports, sharedRoutes) {
		item.CounterEvidence = append(item.CounterEvidence, "部分来源属于多人共用出口，路由证据已折扣")
	}
	if item.IdentityMode != "device_bound" {
		item.CounterEvidence = append(item.CounterEvidence, "存在旧凭证流量，设备数仅提供区间估计且不会单独触发限制")
	}
	if !item.CoverageComplete {
		item.CounterEvidence = append(item.CounterEvidence, "Agent 报告存在丢弃桶，自动限制已禁用")
	}
	item.EvidenceCategories = uniqueStrings(item.EvidenceCategories)
	item.RiskSignals = uniqueStrings(item.RiskSignals)
	item.CounterEvidence = uniqueStrings(item.CounterEvidence)
	geoQuality := connectionAuditGeoQuality(selectedReports)
	independence := math.Min(1, float64(len(item.EvidenceCategories))/2)
	item.Confidence = roundAuditFloat(0.45*identityQuality + 0.25*item.CoverageQuality + 0.20*independence + 0.10*geoQuality)
	item.AutoActionEligible = item.IdentityMode == "device_bound" && item.RiskScore >= 85 && item.Confidence >= policy.AutoActionConfidence && len(item.EvidenceCategories) >= 2 && item.CoverageComplete && strongest != nil
	switch {
	case item.RiskScore >= 95 && item.Confidence >= 0.90 && strongest != nil:
		item.RecommendedAction = "reject_device_authentication"
	case item.RiskScore >= 85 && item.AutoActionEligible:
		item.RecommendedAction = "suspend_device_subscription"
	case item.RiskScore >= 70:
		item.RecommendedAction = "rebind_device_and_limit_raw_requests"
	case item.RiskScore >= 55:
		item.RecommendedAction = "notify_operator"
	default:
		item.RecommendedAction = "observe"
	}
}

func (s *Store) listConnectionAuditReportsForRisk(ctx context.Context, userID int64, since time.Time, limit int) ([]model.ConnectionAuditReport, error) {
	if limit < 1 {
		limit = 10000
	}
	return s.listRecentConnectionAudits(ctx, userID, since.UTC().Format(time.RFC3339Nano), limit)
}

func (s *Store) listConnectionAuditReportsStartedSince(ctx context.Context, userID int64, since time.Time, limit int) ([]model.ConnectionAuditReport, error) {
	if limit < 1 {
		limit = 10000
	}
	return s.listConnectionAuditsByTime(ctx, userID, "started_at", since.UTC().Format(time.RFC3339Nano), limit)
}

func (s *Store) connectionAuditSharedRouteUsers(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `select route_id,count(distinct user_id) from connection_audit_reports where route_id!='' and ended_at>=? group by route_id`, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var routeID string
		var users int
		if err := rows.Scan(&routeID, &users); err != nil {
			return nil, err
		}
		out[routeID] = users
	}
	return out, rows.Err()
}

func connectionAuditHasSharedRoute(reports []model.ConnectionAuditReport, sharedRoutes map[string]int) bool {
	for _, report := range reports {
		if sharedRoutes[report.RouteID] > 1 {
			return true
		}
	}
	return false
}

func connectionAuditDistinctCountries(reports []model.ConnectionAuditReport) int {
	countries := map[string]struct{}{}
	for _, report := range reports {
		code := strings.ToUpper(strings.TrimSpace(report.SourceCountryCode))
		if code != "" && connectionAuditPublicSourceIP(report.SourceIP) {
			countries[code] = struct{}{}
		}
	}
	return len(countries)
}

func connectionAuditMeaningfulReport(report model.ConnectionAuditReport) bool {
	if report.InternalProbe || report.ProbeState == "confirmed" || report.ProbeState == "candidate" || report.UploadBytes+report.DownloadBytes <= 0 {
		return false
	}
	return !report.PayloadFirstAt.IsZero() && !report.PayloadLastAt.Before(report.PayloadFirstAt)
}

func connectionAuditIndependentNetwork(report model.ConnectionAuditReport) string {
	country := strings.ToUpper(strings.TrimSpace(report.SourceCountryCode))
	isp := strings.ToLower(strings.TrimSpace(report.SourceISP))
	if country == "" {
		return ""
	}
	if isp == "" {
		return "country:" + country
	}
	return country + "\x00" + isp
}

func connectionAuditOnlineDevices(item *model.ConnectionAuditUserSummary, reports []model.ConnectionAuditReport, presence []model.ConnectionPresenceEvent, at time.Time) float64 {
	exactObserved, legacyObserved := false, false
	exactActive := map[string]struct{}{}
	legacyNetworks := map[string]struct{}{}
	legacyPrefixes := map[string]struct{}{}
	legacyIPs := map[string]struct{}{}
	for _, report := range reports {
		if report.DeviceIDHash == "" {
			legacyObserved = true
		} else {
			exactObserved = true
		}
		if !connectionAuditMeaningfulReport(report) {
			continue
		}
		ttl := connectionAuditPresenceTCP
		if strings.EqualFold(report.Network, "udp") {
			ttl = connectionAuditPresenceUDP
		}
		if report.PayloadLastAt.Before(at.Add(-ttl)) || report.PayloadLastAt.After(at.Add(5*time.Second)) {
			continue
		}
		if report.DeviceIDHash != "" {
			exactActive[report.DeviceIDHash] = struct{}{}
			continue
		}
		legacyIPs[report.SourceIP] = struct{}{}
		if network := connectionAuditIndependentNetwork(report); network != "" {
			legacyNetworks[network] = struct{}{}
		}
		if prefix := auditSubnet(report.SourceIP); prefix != "" {
			legacyPrefixes[prefix] = struct{}{}
		}
	}
	for _, event := range presence {
		if event.DeviceIDHash == "" {
			legacyObserved = true
		} else {
			exactObserved = true
		}
		if !connectionAuditMeaningfulPresence(event, at) {
			continue
		}
		if event.DeviceIDHash != "" {
			exactActive[event.DeviceIDHash] = struct{}{}
			continue
		}
		legacyIPs[event.SourceIP] = struct{}{}
		if strings.TrimSpace(event.RouteID) != "" {
			legacyNetworks[event.RouteID] = struct{}{}
		}
		if prefix := auditSubnet(event.SourceIP); prefix != "" {
			legacyPrefixes[prefix] = struct{}{}
		}
	}
	switch {
	case exactObserved && legacyObserved:
		item.IdentityMode = "mixed"
	case exactObserved:
		item.IdentityMode = "device_bound"
	default:
		item.IdentityMode = "legacy_unbound"
	}
	exactCount := len(exactActive)
	legacyActive := len(legacyIPs) > 0
	legacyLower, legacyUpper := 0, 0
	legacyEstimate := 0.0
	if legacyActive {
		legacyLower = 1
		networkCount := max(1, len(legacyNetworks))
		clusterCount := max(networkCount, len(legacyPrefixes))
		clusterCount = max(clusterCount, len(legacyIPs))
		legacyUpper = clusterCount
		legacyEstimate = 1 + 0.70*float64(networkCount-1) + 0.25*float64(max(0, clusterCount-networkCount))
		if legacyEstimate > float64(legacyUpper) {
			legacyEstimate = float64(legacyUpper)
		}
	}
	item.OnlineDeviceLower = exactCount + legacyLower
	item.OnlineDeviceEstimate = math.Round((float64(exactCount)+legacyEstimate)*10) / 10
	item.OnlineDeviceUpper = exactCount + legacyUpper
	if item.IdentityMode == "device_bound" {
		item.OnlineDeviceCount = exactCount
		item.OnlineDeviceLower = exactCount
		item.OnlineDeviceEstimate = float64(exactCount)
		item.OnlineDeviceUpper = exactCount
		return 1
	}
	if item.IdentityMode == "mixed" {
		return 0.65
	}
	return 0.40
}

func connectionAuditMeaningfulPresence(event model.ConnectionPresenceEvent, at time.Time) bool {
	if !event.Meaningful || event.PayloadLastAt.IsZero() || event.PayloadLastAt.After(at.Add(5*time.Second)) {
		return false
	}
	ttl := connectionAuditPresenceTCP
	if strings.EqualFold(event.Network, "udp") {
		ttl = connectionAuditPresenceUDP
	}
	return !event.PayloadLastAt.Before(at.Add(-ttl))
}

func connectionAuditCoverage(reports []model.ConnectionAuditReport) (float64, bool) {
	if len(reports) == 0 {
		return 1, true
	}
	type collectionCoverage struct {
		capacity int
		dropped  int64
	}
	collections := map[string]collectionCoverage{}
	for _, report := range reports {
		key := fmt.Sprintf("%d\x00%d\x00%s", report.ServerID, report.CollectionGeneration, report.CollectionEndedAt.UTC().Format(time.RFC3339Nano))
		collections[key] = collectionCoverage{capacity: max(1, report.BucketCapacity), dropped: max(int64(0), report.DroppedBucketCount)}
	}
	quality := 0.0
	complete := true
	for _, coverage := range collections {
		if coverage.dropped > 0 {
			complete = false
		}
		quality += float64(coverage.capacity) / float64(int64(coverage.capacity)+coverage.dropped)
	}
	return roundAuditFloat(quality / float64(len(collections))), complete
}

func connectionAuditGeoQuality(reports []model.ConnectionAuditReport) float64 {
	public, resolved := 0, 0
	for _, report := range reports {
		if !connectionAuditPublicSourceIP(report.SourceIP) {
			continue
		}
		public++
		if strings.TrimSpace(report.SourceCountryCode) != "" && strings.TrimSpace(report.GeoDatabaseRevision) != "" {
			resolved++
		}
	}
	if public == 0 {
		return 0.5
	}
	return float64(resolved) / float64(public)
}

func connectionAuditNode(report model.ConnectionAuditReport) string {
	inboundID := int64(0)
	if report.InboundID != nil {
		inboundID = *report.InboundID
	}
	return fmt.Sprintf("%d:%d:%s", report.ServerID, inboundID, report.OutboundTag)
}

func connectionAuditNodeFanout(reports []model.ConnectionAuditReport) int {
	byIdentity := map[string][]model.ConnectionAuditReport{}
	for _, report := range reports {
		if report.InternalProbe || report.ProbeState == "confirmed" || report.ProbeState == "candidate" || report.ConnectionCount <= 0 {
			continue
		}
		identity := connectionAuditReportIdentity(report)
		byIdentity[identity.key()] = append(byIdentity[identity.key()], report)
	}
	maximum := 0
	for _, items := range byIdentity {
		sort.SliceStable(items, func(i, j int) bool { return items[i].StartedAt.Before(items[j].StartedAt) })
		for left, right := 0, 0; right < len(items); right++ {
			for left <= right && items[right].StartedAt.Sub(items[left].StartedAt) > 10*time.Second {
				left++
			}
			nodes := map[string]struct{}{}
			for index := left; index <= right; index++ {
				nodes[connectionAuditNode(items[index])] = struct{}{}
			}
			maximum = max(maximum, len(nodes))
		}
	}
	return maximum
}

func connectionAuditNonProbeActivePeak(reports []model.ConnectionAuditReport) int64 {
	var peak int64
	for _, report := range reports {
		if report.InternalProbe || report.ProbeState == "confirmed" || report.ProbeState == "candidate" {
			continue
		}
		peak = max(peak, report.ActivePeak)
	}
	return peak
}

func (s *Store) connectionAuditRobustZ(ctx context.Context, userID int64, at time.Time) (float64, error) {
	at = at.UTC()
	currentStart := time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), 0, 0, 0, time.UTC)
	rows, err := s.db.QueryContext(ctx, `select strftime('%Y-%m-%dT%H:00:00Z',started_at) as hour_bucket,coalesce(sum(connection_count),0)
		from connection_audit_reports
		where user_id=? and started_at>=? and started_at<? and internal_probe=0 and probe_state not in ('confirmed','candidate') and dropped_bucket_count=0
		group by hour_bucket`, userID, currentStart.Add(-28*24*time.Hour).Format(time.RFC3339Nano), currentStart.Add(time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	current := 0.0
	history := []float64{}
	for rows.Next() {
		var rawBucket string
		var value float64
		if err := rows.Scan(&rawBucket, &value); err != nil {
			return 0, err
		}
		bucket := parseTime(rawBucket)
		if bucket.Equal(currentStart) {
			current = value
			continue
		}
		if bucket.Before(currentStart) && bucket.Weekday() == at.Weekday() && bucket.Hour() == at.Hour() {
			history = append(history, value)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(history) < 3 {
		return 0, nil
	}
	median := medianAuditValues(history)
	deviations := make([]float64, 0, len(history))
	for _, value := range history {
		deviations = append(deviations, math.Abs(value-median))
	}
	mad := medianAuditValues(deviations)
	robustZ := (current - median) / (1.4826*mad + math.Max(1, 0.1*median))
	if robustZ < 0 {
		return 0, nil
	}
	return math.Round(robustZ*10) / 10, nil
}

func medianAuditValues(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	values = append([]float64(nil), values...)
	sort.Float64s(values)
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle]
	}
	return (values[middle-1] + values[middle]) / 2
}

func normalizedAuditValue(value float64, threshold model.AuditThreshold) float64 {
	soft, hard := float64(threshold.Soft), float64(threshold.Hard)
	if value <= soft {
		return 0
	}
	if hard <= soft {
		return 1
	}
	return clampFloat((value-soft)/(hard-soft), 0, 1)
}

func clampFloat(value, lower, upper float64) float64 {
	return math.Max(lower, math.Min(upper, value))
}

func roundAuditFloat(value float64) float64 {
	return math.Round(value*100) / 100
}

func (s *Store) refreshConnectionProbeEpisodes(ctx context.Context, userID int64, at time.Time) error {
	if userID <= 0 {
		return nil
	}
	cutoff := at.Add(-10*time.Minute - connectionAuditProbeWindow)
	assignedNodes, err := s.connectionAuditAssignedNodes(ctx, userID, at.Add(-24*time.Hour))
	if err != nil {
		return err
	}
	reports, err := s.listConnectionAuditReportsStartedSince(ctx, userID, cutoff, 50000)
	if err != nil {
		return err
	}
	byIdentity := map[string][]model.ConnectionAuditReport{}
	for _, report := range reports {
		if report.InternalProbe {
			continue
		}
		key := connectionAuditReportIdentity(report).key()
		byIdentity[key] = append(byIdentity[key], report)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoffRaw := cutoff.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `delete from connection_probe_episodes where user_id=? and ended_at>=?`, userID, cutoffRaw); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update connection_audit_reports set probe_state='' where user_id=? and ended_at>=? and internal_probe=0`, userID, cutoffRaw); err != nil {
		return err
	}
	for key, items := range byIdentity {
		sort.SliceStable(items, func(i, j int) bool { return items[i].StartedAt.Before(items[j].StartedAt) })
		for start := 0; start < len(items); {
			end := start + 1
			for end < len(items) && !items[end].StartedAt.After(items[start].StartedAt.Add(connectionAuditProbeWindow)) {
				end++
			}
			group := items[start:end]
			threshold := connectionProbeCandidateThreshold(len(assignedNodes[key]))
			episode, state := classifyConnectionProbeEpisode(userID, group, threshold, at)
			if state != "" {
				for _, report := range group {
					if _, err := tx.ExecContext(ctx, `update connection_audit_reports set probe_state=? where report_id=?`, state, report.ReportID); err != nil {
						return err
					}
				}
				if _, err := tx.ExecContext(ctx, `insert into connection_probe_episodes(id,user_id,device_id_hash,state,score,node_count,connection_count,upload_bytes,download_bytes,started_at,ended_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`, episode.ID, episode.UserID, episode.DeviceIDHash, episode.State, episode.Score, episode.NodeCount, episode.ConnectionCount, episode.UploadBytes, episode.DownloadBytes, episode.StartedAt.UTC().Format(time.RFC3339Nano), episode.EndedAt.UTC().Format(time.RFC3339Nano), now()); err != nil {
					return err
				}
			}
			start = end
		}
	}
	return tx.Commit()
}

func (s *Store) connectionAuditAssignedNodes(ctx context.Context, userID int64, since time.Time) (map[string]map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `select distinct device_id_hash,source_ip,server_id,coalesce(inbound_id,0),outbound_tag
		from connection_audit_reports where user_id=? and ended_at>=? and internal_probe=0`, userID, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	assignedNodes := map[string]map[string]struct{}{}
	for rows.Next() {
		var report model.ConnectionAuditReport
		var inboundID int64
		report.UserID = userID
		if err := rows.Scan(&report.DeviceIDHash, &report.SourceIP, &report.ServerID, &inboundID, &report.OutboundTag); err != nil {
			return nil, err
		}
		if inboundID != 0 {
			report.InboundID = &inboundID
		}
		key := connectionAuditReportIdentity(report).key()
		if assignedNodes[key] == nil {
			assignedNodes[key] = map[string]struct{}{}
		}
		assignedNodes[key][connectionAuditNode(report)] = struct{}{}
	}
	return assignedNodes, rows.Err()
}

func connectionProbeCandidateThreshold(assigned int) int {
	return min(8, max(4, int(math.Ceil(0.25*float64(assigned)))))
}

func classifyConnectionProbeEpisode(userID int64, reports []model.ConnectionAuditReport, threshold int, at time.Time) (model.ConnectionProbeEpisode, string) {
	if len(reports) == 0 {
		return model.ConnectionProbeEpisode{}, ""
	}
	nodes := map[string]struct{}{}
	connectionsByNode := map[string]int64{}
	bytesByNode := map[string]int64{}
	var connections, upload, download, closed, shortClosed int64
	startedAt, endedAt := reports[0].StartedAt, reports[0].EndedAt
	latestStart := startedAt
	deviceID := reports[0].DeviceIDHash
	cancelled := false
	activeAfterBudget := false
	for _, report := range reports {
		node := connectionAuditNode(report)
		nodes[node] = struct{}{}
		connectionsByNode[node] += report.ConnectionCount
		bytesByNode[node] += report.UploadBytes + report.DownloadBytes
		connections += report.ConnectionCount
		upload += report.UploadBytes
		download += report.DownloadBytes
		closed += report.ClosedCount
		shortClosed += report.DurationLE1SCount + report.DurationLE5SCount
		if report.StartedAt.Before(startedAt) {
			startedAt = report.StartedAt
		}
		if report.StartedAt.After(latestStart) {
			latestStart = report.StartedAt
		}
		if report.EndedAt.After(endedAt) {
			endedAt = report.EndedAt
		}
		if report.DurationMaxMS > int64((20*time.Second)/time.Millisecond) && report.UploadBytes+report.DownloadBytes > 256*1024 {
			cancelled = true
		}
		if report.ActiveAtEnd > 0 && report.CollectionEndedAt.After(startedAt.Add(connectionAuditProbeWindow)) {
			activeAfterBudget = true
		}
	}
	if len(nodes) < threshold {
		return model.ConnectionProbeEpisode{}, ""
	}
	totalBytes := upload + download
	if totalBytes > 4*1024*1024 || activeAfterBudget {
		cancelled = true
	}
	for _, count := range connectionsByNode {
		if count > 5 {
			cancelled = true
		}
	}
	score := 25
	if latestStart.Sub(startedAt) <= 10*time.Second {
		score += 15
	}
	if closed > 0 && float64(shortClosed)/float64(closed) >= 0.90 {
		score += 20
	}
	nodeBytes := make([]float64, 0, len(bytesByNode))
	for _, value := range bytesByNode {
		nodeBytes = append(nodeBytes, float64(value))
	}
	if medianAuditValues(nodeBytes) <= 64*1024 && totalBytes <= 4*1024*1024 {
		score += 20
	}
	perNodeWithinBudget := true
	for _, count := range connectionsByNode {
		if count > 3 {
			perNodeWithinBudget = false
			break
		}
	}
	if perNodeWithinBudget {
		score += 10
	}
	if !at.Before(startedAt.Add(connectionAuditProbeWindow)) && !activeAfterBudget {
		score += 10
	}
	state := "candidate"
	if cancelled {
		state = "normal_traffic"
	} else if score >= 75 {
		state = "confirmed"
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%d", userID, connectionAuditReportIdentity(reports[0]).key(), startedAt.UnixNano())))
	episode := model.ConnectionProbeEpisode{ID: "probe_" + hex.EncodeToString(digest[:12]), UserID: userID, DeviceIDHash: deviceID, State: state, Score: min(100, score), NodeCount: len(nodes), ConnectionCount: connections, UploadBytes: upload, DownloadBytes: download, StartedAt: startedAt, EndedAt: endedAt, UpdatedAt: at}
	return episode, state
}

func (s *Store) listConnectionProbeEpisodes(ctx context.Context, userID int64, since time.Time, limit int) ([]model.ConnectionProbeEpisode, error) {
	if limit < 1 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `select id,user_id,device_id_hash,state,score,node_count,connection_count,upload_bytes,download_bytes,started_at,ended_at,updated_at from connection_probe_episodes where user_id=? and ended_at>=? order by ended_at desc limit ?`, userID, since.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ConnectionProbeEpisode{}
	for rows.Next() {
		var episode model.ConnectionProbeEpisode
		var startedAt, endedAt, updatedAt string
		if err := rows.Scan(&episode.ID, &episode.UserID, &episode.DeviceIDHash, &episode.State, &episode.Score, &episode.NodeCount, &episode.ConnectionCount, &episode.UploadBytes, &episode.DownloadBytes, &startedAt, &endedAt, &updatedAt); err != nil {
			return nil, err
		}
		episode.StartedAt = parseTime(startedAt)
		episode.EndedAt = parseTime(endedAt)
		episode.UpdatedAt = parseTime(updatedAt)
		out = append(out, episode)
	}
	return out, rows.Err()
}
