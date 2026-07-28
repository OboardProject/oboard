package store

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	connectionAuditRetention  = 30 * 24 * time.Hour
	connectionAuditRiskWindow = 15 * time.Minute
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
	for _, report := range reports {
		if strings.TrimSpace(report.ReportID) == "" || report.ServerID <= 0 || report.UserID <= 0 {
			continue
		}
		res, err := tx.ExecContext(ctx, `insert or ignore into connection_audit_reports(report_id,server_id,user_id,inbound_id,path_id,source_ip,source_geo_code,source_country_code,source_country,source_province,source_city,source_isp,geo_database_revision,network,destination,destination_port,outbound_tag,outbound_type,connection_count,active_peak,active_at_end,started_at,ended_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			report.ReportID, report.ServerID, report.UserID, report.InboundID, report.PathID, report.SourceIP, report.SourceGeoCode, report.SourceCountryCode, report.SourceCountry, report.SourceProvince, report.SourceCity, report.SourceISP, report.GeoDatabaseRevision, report.Network, report.Destination, report.DestinationPort, report.OutboundTag, report.OutboundType, report.ConnectionCount, report.ActivePeak, report.ActiveAtEnd, report.StartedAt.UTC().Format(time.RFC3339Nano), report.EndedAt.UTC().Format(time.RFC3339Nano), ts)
		if err != nil {
			return nil, err
		}
		if _, err := res.RowsAffected(); err != nil {
			return nil, err
		}
		accepted = append(accepted, report.ReportID)
	}
	cutoff := time.Now().UTC().Add(-connectionAuditRetention).Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `delete from connection_audit_reports where ended_at < ?`, cutoff); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return accepted, nil
}

func (s *Store) ConnectionAuditOverview(ctx context.Context, windowHours int) (model.ConnectionAuditOverview, error) {
	if windowHours < 1 {
		windowHours = 24
	}
	if windowHours > 30*24 {
		windowHours = 30 * 24
	}
	nowTime := time.Now().UTC()
	since := nowTime.Add(-time.Duration(windowHours) * time.Hour).Format(time.RFC3339Nano)
	overview := model.ConnectionAuditOverview{WindowHours: windowHours, RiskWindowMinutes: int(connectionAuditRiskWindow / time.Minute), GeneratedAt: nowTime, Users: []model.ConnectionAuditUserSummary{}}
	if err := s.db.QueryRowContext(ctx, `select count(*) from servers where connection_audit_enabled=1`).Scan(&overview.EnabledServerCount); err != nil {
		return overview, err
	}
	rows, err := s.db.QueryContext(ctx, `select r.user_id,u.username,u.nickname,count(distinct r.source_ip),count(distinct r.server_id),coalesce(sum(r.connection_count),0),coalesce(max(r.active_peak),0),count(*),max(r.ended_at)
		from connection_audit_reports r join users u on u.id=r.user_id where r.ended_at>=? group by r.user_id,u.username,u.nickname`, since)
	if err != nil {
		return overview, err
	}
	for rows.Next() {
		var item model.ConnectionAuditUserSummary
		var lastSeen string
		if err := rows.Scan(&item.UserID, &item.Username, &item.Nickname, &item.SourceIPCount, &item.ServerCount, &item.ConnectionCount, &item.ActivePeak, &item.ReportCount, &lastSeen); err != nil {
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

	firstUserByIP := map[string]int64{}
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
		firstUserID, seen := firstUserByIP[sourceIP]
		if !seen {
			firstUserByIP[sourceIP] = userID
		} else if firstUserID != userID {
			if sharedIPsByUser[firstUserID] == nil {
				sharedIPsByUser[firstUserID] = map[string]struct{}{}
			}
			if sharedIPsByUser[userID] == nil {
				sharedIPsByUser[userID] = map[string]struct{}{}
			}
			sharedIPsByUser[firstUserID][sourceIP] = struct{}{}
			sharedIPsByUser[userID][sourceIP] = struct{}{}
		}
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
	riskEventsByUser, regionCountsByUser, err := s.connectionAuditRiskEvents(ctx, since)
	if err != nil {
		return overview, err
	}
	for index := range overview.Users {
		item := &overview.Users[index]
		item.SourceSubnetCount = len(subnetsByUser[item.UserID])
		item.SharedSourceIPCount = len(sharedIPsByUser[item.UserID])
		item.SourceRegionCount = regionCountsByUser[item.UserID]
		var strongest *model.ConnectionAuditRiskEvent
		for eventIndex := range riskEventsByUser[item.UserID] {
			event := &riskEventsByUser[item.UserID][eventIndex]
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
		item.RiskScore, item.RiskLevel, item.RiskSignals = connectionAuditRisk(*item, windowHours, strongest)
		overview.TotalConnections += item.ConnectionCount
		if item.RiskLevel == "high" || item.RiskLevel == "critical" {
			overview.ElevatedRiskCount++
		}
	}
	overview.ReportingUserCount = len(overview.Users)
	overview.UniqueSourceIPs = len(firstUserByIP)
	sort.SliceStable(overview.Users, func(i, j int) bool {
		if overview.Users[i].RiskScore != overview.Users[j].RiskScore {
			return overview.Users[i].RiskScore > overview.Users[j].RiskScore
		}
		return overview.Users[i].LastSeenAt.After(overview.Users[j].LastSeenAt)
	})
	return overview, nil
}

func (s *Store) ConnectionAuditUserDetail(ctx context.Context, userID int64, windowHours int) (model.ConnectionAuditUserDetail, error) {
	overview, err := s.ConnectionAuditOverview(ctx, windowHours)
	if err != nil {
		return model.ConnectionAuditUserDetail{}, err
	}
	detail := model.ConnectionAuditUserDetail{
		Sources: []model.ConnectionAuditDimension{}, Destinations: []model.ConnectionAuditDimension{},
		Outbounds: []model.ConnectionAuditDimension{}, Servers: []model.ConnectionAuditDimension{}, Recent: []model.ConnectionAuditReport{}, RiskEvents: []model.ConnectionAuditRiskEvent{},
	}
	found := false
	for _, item := range overview.Users {
		if item.UserID == userID {
			detail.Summary = item
			found = true
			break
		}
	}
	if !found {
		return detail, sql.ErrNoRows
	}
	since := time.Now().UTC().Add(-time.Duration(overview.WindowHours) * time.Hour).Format(time.RFC3339Nano)
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
	eventsByUser, _, err := s.connectionAuditRiskEvents(ctx, since, userID)
	if err != nil {
		return detail, err
	}
	detail.RiskEvents = eventsByUser[userID]
	sort.SliceStable(detail.RiskEvents, func(i, j int) bool { return detail.RiskEvents[i].EndedAt.After(detail.RiskEvents[j].EndedAt) })
	if len(detail.RiskEvents) > 20 {
		detail.RiskEvents = detail.RiskEvents[:20]
	}
	return detail, err
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
	rows, err := s.db.QueryContext(ctx, `select report_id,server_id,user_id,inbound_id,path_id,source_ip,source_geo_code,source_country_code,source_country,source_province,source_city,source_isp,geo_database_revision,network,destination,destination_port,outbound_tag,outbound_type,connection_count,active_peak,active_at_end,started_at,ended_at,created_at from connection_audit_reports where user_id=? and ended_at>=? order by ended_at desc limit ?`, userID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ConnectionAuditReport{}
	for rows.Next() {
		var item model.ConnectionAuditReport
		var inboundID, pathID sql.NullInt64
		var startedAt, endedAt, createdAt string
		if err := rows.Scan(&item.ReportID, &item.ServerID, &item.UserID, &inboundID, &pathID, &item.SourceIP, &item.SourceGeoCode, &item.SourceCountryCode, &item.SourceCountry, &item.SourceProvince, &item.SourceCity, &item.SourceISP, &item.GeoDatabaseRevision, &item.Network, &item.Destination, &item.DestinationPort, &item.OutboundTag, &item.OutboundType, &item.ConnectionCount, &item.ActivePeak, &item.ActiveAtEnd, &startedAt, &endedAt, &createdAt); err != nil {
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

type connectionAuditGeoObservation struct {
	UserID      int64
	SourceIP    string
	RegionKey   string
	RegionLabel string
	At          time.Time
}

func (s *Store) connectionAuditRiskEvents(ctx context.Context, since string, userIDs ...int64) (map[int64][]model.ConnectionAuditRiskEvent, map[int64]int, error) {
	query := `select user_id,source_ip,source_country_code,source_country,source_province,ended_at from connection_audit_reports where ended_at>=?`
	args := []any{since}
	if len(userIDs) == 1 && userIDs[0] > 0 {
		query += ` and user_id=?`
		args = append(args, userIDs[0])
	}
	query += ` order by user_id,ended_at,source_ip`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	observationsByUser := map[int64][]connectionAuditGeoObservation{}
	regionsByUser := map[int64]map[string]struct{}{}
	for rows.Next() {
		var userID int64
		var sourceIP, countryCode, country, province, endedAt string
		if err := rows.Scan(&userID, &sourceIP, &countryCode, &country, &province, &endedAt); err != nil {
			return nil, nil, err
		}
		if !connectionAuditPublicSourceIP(sourceIP) {
			continue
		}
		regionKey, regionLabel := connectionAuditRegion(countryCode, country, province)
		if regionKey == "" {
			continue
		}
		at := parseTime(endedAt)
		if at.IsZero() {
			continue
		}
		observationsByUser[userID] = append(observationsByUser[userID], connectionAuditGeoObservation{UserID: userID, SourceIP: sourceIP, RegionKey: regionKey, RegionLabel: regionLabel, At: at})
		if regionsByUser[userID] == nil {
			regionsByUser[userID] = map[string]struct{}{}
		}
		regionsByUser[userID][regionKey] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	eventsByUser := map[int64][]model.ConnectionAuditRiskEvent{}
	regionCounts := map[int64]int{}
	for userID, observations := range observationsByUser {
		eventsByUser[userID] = buildConnectionAuditRiskEvents(observations)
		regionCounts[userID] = len(regionsByUser[userID])
	}
	return eventsByUser, regionCounts, nil
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

func (s *Store) ConnectionAuditCurrentRisk(ctx context.Context, userID int64, at time.Time) (*model.ConnectionAuditRiskEvent, error) {
	if userID <= 0 {
		return nil, nil
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	since := at.UTC().Add(-connectionAuditRiskWindow).Format(time.RFC3339Nano)
	eventsByUser, _, err := s.connectionAuditRiskEvents(ctx, since, userID)
	if err != nil {
		return nil, err
	}
	var strongest *model.ConnectionAuditRiskEvent
	for index := range eventsByUser[userID] {
		event := &eventsByUser[userID][index]
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

func buildConnectionAuditRiskEvents(observations []connectionAuditGeoObservation) []model.ConnectionAuditRiskEvent {
	if len(observations) < 2 {
		return []model.ConnectionAuditRiskEvent{}
	}
	sort.SliceStable(observations, func(i, j int) bool { return observations[i].At.Before(observations[j].At) })
	type ipWindowState struct {
		count       int
		regionKey   string
		regionLabel string
	}
	ips := map[string]ipWindowState{}
	regionRefs := map[string]int{}
	regionLabels := map[string]string{}
	events := []model.ConnectionAuditRiskEvent{}
	left := 0
	add := func(observation connectionAuditGeoObservation) {
		state := ips[observation.SourceIP]
		state.count++
		if state.count == 1 {
			state.regionKey = observation.RegionKey
			state.regionLabel = observation.RegionLabel
			regionRefs[state.regionKey]++
			regionLabels[state.regionKey] = state.regionLabel
		}
		ips[observation.SourceIP] = state
	}
	remove := func(observation connectionAuditGeoObservation) {
		state := ips[observation.SourceIP]
		state.count--
		if state.count <= 0 {
			delete(ips, observation.SourceIP)
			regionRefs[state.regionKey]--
			if regionRefs[state.regionKey] <= 0 {
				delete(regionRefs, state.regionKey)
				delete(regionLabels, state.regionKey)
			}
			return
		}
		ips[observation.SourceIP] = state
	}
	for right, observation := range observations {
		add(observation)
		for left <= right && observation.At.Sub(observations[left].At) > connectionAuditRiskWindow {
			remove(observations[left])
			left++
		}
		if len(ips) < 2 || len(regionRefs) < 2 {
			continue
		}
		regions := make([]string, 0, len(regionLabels))
		for _, label := range regionLabels {
			regions = append(regions, label)
		}
		sort.Strings(regions)
		score, level := 50, "high"
		if len(regionRefs) >= 3 {
			score, level = 75, "critical"
		}
		candidate := model.ConnectionAuditRiskEvent{Level: level, Score: score, SourceIPCount: len(ips), RegionCount: len(regionRefs), Regions: regions, StartedAt: observations[left].At, EndedAt: observation.At}
		if len(events) > 0 && !candidate.StartedAt.After(events[len(events)-1].EndedAt) {
			current := &events[len(events)-1]
			current.EndedAt = candidate.EndedAt
			if candidate.StartedAt.Before(current.StartedAt) {
				current.StartedAt = candidate.StartedAt
			}
			if strongerConnectionAuditRiskEvent(candidate, *current) {
				current.Level, current.Score = candidate.Level, candidate.Score
				current.SourceIPCount, current.RegionCount = candidate.SourceIPCount, candidate.RegionCount
				current.Regions = candidate.Regions
			}
			continue
		}
		events = append(events, candidate)
	}
	return events
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

func connectionAuditRisk(item model.ConnectionAuditUserSummary, windowHours int, geoEvent *model.ConnectionAuditRiskEvent) (int, string, []string) {
	if windowHours < 1 {
		windowHours = 24
	}
	sharedIPMedium, sharedIPHigh := 2, 5
	serverThreshold := 3
	switch {
	case windowHours > 7*24:
		sharedIPMedium, sharedIPHigh = 8, 20
		serverThreshold = 5
	case windowHours > 24:
		sharedIPMedium, sharedIPHigh = 4, 10
		serverThreshold = 4
	}
	score := 0
	signals := []string{}
	add := func(points int, signal string) {
		score += points
		signals = append(signals, signal)
	}
	if geoEvent != nil {
		add(geoEvent.Score, "15 分钟内 "+strconv.Itoa(geoEvent.SourceIPCount)+" 个来源 IP 跨 "+strings.Join(geoEvent.Regions, "、"))
	}
	if item.SharedSourceIPCount >= sharedIPHigh {
		add(35, "有 "+strconv.Itoa(item.SharedSourceIPCount)+" 个来源 IP 同时被多个用户使用，疑似共享出口")
	} else if item.SharedSourceIPCount >= sharedIPMedium {
		add(20, "有 "+strconv.Itoa(item.SharedSourceIPCount)+" 个来源 IP 同时被多个用户使用")
	}
	if item.ServerCount >= serverThreshold {
		add(15, "当前窗口涉及 "+strconv.Itoa(item.ServerCount)+" 台服务器")
	}
	if item.ActivePeak >= 20 {
		add(35, "连接并发峰值达到 "+strconv.FormatInt(item.ActivePeak, 10))
	} else if item.ActivePeak >= 8 {
		add(20, "连接并发峰值达到 "+strconv.FormatInt(item.ActivePeak, 10))
	}
	if float64(item.ConnectionCount)/float64(windowHours) >= 1000.0/24.0 {
		add(10, "平均每小时连接次数偏高")
	}
	if score > 100 {
		score = 100
	}
	level := "low"
	switch {
	case score >= 75:
		level = "critical"
	case score >= 50:
		level = "high"
	case score >= 25:
		level = "medium"
	}
	return score, level, signals
}
