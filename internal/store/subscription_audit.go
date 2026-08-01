package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const subscriptionAuditRetention = 30 * 24 * time.Hour

type subscriptionAuditQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type SubscriptionPullDecision struct {
	AuditID       int64
	Allowed       bool
	Burned        bool
	JustSuspended bool
	Access        model.SubscriptionAccessState
	Risk          model.SubscriptionAuditRisk
}

func DefaultSubscriptionAuditPolicy() model.SubscriptionAuditPolicy {
	return model.SubscriptionAuditPolicy{
		ShortWindowMinutes: 15,
		LongWindowHours:    24,
		Short: model.SubscriptionAuditThresholds{
			RegionLimit: 3, SourceIPLimit: 6, PullLimit: 12, ClientFormatLimit: 3,
		},
		Long: model.SubscriptionAuditThresholds{
			RegionLimit: 4, SourceIPLimit: 12, PullLimit: 48, ClientFormatLimit: 5,
		},
	}
}

func ValidateSubscriptionAuditPolicy(policy model.SubscriptionAuditPolicy) error {
	if policy.ShortWindowMinutes < 5 || policy.ShortWindowMinutes > 1440 {
		return errors.New("subscription audit short window must be between 5 and 1440 minutes")
	}
	if policy.LongWindowHours < 1 || policy.LongWindowHours > 720 || policy.LongWindowHours*60 <= policy.ShortWindowMinutes {
		return errors.New("subscription audit long window must be greater than the short window and no more than 720 hours")
	}
	validate := func(name string, value, minimum, maximum int) error {
		if value < minimum || value > maximum {
			return fmt.Errorf("subscription audit %s must be between %d and %d", name, minimum, maximum)
		}
		return nil
	}
	for _, item := range []struct {
		name        string
		short, long int
		min, max    int
	}{
		{"region limit", policy.Short.RegionLimit, policy.Long.RegionLimit, 2, 50},
		{"source IP limit", policy.Short.SourceIPLimit, policy.Long.SourceIPLimit, 2, 1000},
		{"pull limit", policy.Short.PullLimit, policy.Long.PullLimit, 2, 10000},
		{"client-format limit", policy.Short.ClientFormatLimit, policy.Long.ClientFormatLimit, 2, 100},
	} {
		if err := validate("short "+item.name, item.short, item.min, item.max); err != nil {
			return err
		}
		if err := validate("long "+item.name, item.long, item.min, item.max); err != nil {
			return err
		}
		if item.long < item.short {
			return fmt.Errorf("subscription audit long %s must not be lower than the short limit", item.name)
		}
	}
	return nil
}

func (s *Store) AuthorizeSubscriptionPull(ctx context.Context, userID int64, token string, event model.SubscriptionPullAudit, policy model.SubscriptionAuditPolicy) (SubscriptionPullDecision, error) {
	decision := SubscriptionPullDecision{}
	if err := ValidateSubscriptionAuditPolicy(policy); err != nil {
		return decision, err
	}
	if userID <= 0 || strings.TrimSpace(token) == "" {
		return decision, sql.ErrNoRows
	}
	if event.RequestedAt.IsZero() {
		event.RequestedAt = time.Now().UTC()
	} else {
		event.RequestedAt = event.RequestedAt.UTC()
	}
	event.UserID = userID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return decision, err
	}
	defer tx.Rollback()
	tokenKind, err := subscriptionTokenKind(ctx, tx, userID, token)
	if err != nil {
		return decision, err
	}
	event.TokenKind = tokenKind
	state, err := getSubscriptionAccessState(ctx, tx, userID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return decision, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		state = model.SubscriptionAccessState{UserID: userID}
	}
	if state.Suspended {
		event.Outcome = "denied_suspended"
		event.Reason = state.Reason
		event.RiskEligible = false
		decision.AuditID, err = insertSubscriptionPullAudit(ctx, tx, event)
		if err != nil {
			return decision, err
		}
		decision.Access = state
		if state.TriggerRisk != nil {
			decision.Risk = *state.TriggerRisk
		}
		if err := pruneSubscriptionAudits(ctx, tx, event.RequestedAt); err != nil {
			return decision, err
		}
		return decision, tx.Commit()
	}
	event.Outcome = "pending"
	event.RiskEligible = true
	decision.AuditID, err = insertSubscriptionPullAudit(ctx, tx, event)
	if err != nil {
		return decision, err
	}
	risk, err := evaluateSubscriptionAuditRisk(ctx, tx, userID, event.RequestedAt, policy, state.EvaluationStartedAt)
	if err != nil {
		return decision, err
	}
	decision.Risk = risk
	if risk.HardBlock {
		if _, err := tx.ExecContext(ctx, `update subscription_pull_audits set outcome='denied_risk',reason=? where id=?`, risk.Reason, decision.AuditID); err != nil {
			return decision, err
		}
		raw, err := json.Marshal(risk)
		if err != nil {
			return decision, err
		}
		ts := event.RequestedAt.Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `insert into subscription_access_states(user_id,suspended,suspended_at,suspension_reason,trigger_audit_id,trigger_snapshot_json,evaluation_started_at,resumed_at,resumed_by,updated_at)
			values(?,1,?,?,?,?,?,null,null,?)
			on conflict(user_id) do update set suspended=1,suspended_at=excluded.suspended_at,suspension_reason=excluded.suspension_reason,trigger_audit_id=excluded.trigger_audit_id,trigger_snapshot_json=excluded.trigger_snapshot_json,updated_at=excluded.updated_at`,
			userID, ts, risk.Reason, decision.AuditID, string(raw), subscriptionEvaluationStart(state, event.RequestedAt), ts); err != nil {
			return decision, err
		}
		state, err = getSubscriptionAccessState(ctx, tx, userID)
		if err != nil {
			return decision, err
		}
		decision.Access = state
		decision.JustSuspended = true
	} else {
		if _, err := tx.ExecContext(ctx, `update subscription_pull_audits set outcome='served' where id=?`, decision.AuditID); err != nil {
			return decision, err
		}
		decision.Burned, err = consumeSubscriptionTokenTx(ctx, tx, userID, token, tokenKind, event.RequestedAt)
		if err != nil {
			return decision, err
		}
		decision.Allowed = true
		decision.Access = state
	}
	if err := pruneSubscriptionAudits(ctx, tx, event.RequestedAt); err != nil {
		return decision, err
	}
	return decision, tx.Commit()
}

func subscriptionTokenKind(ctx context.Context, tx *sql.Tx, userID int64, token string) (string, error) {
	var persistent string
	var burnAfterRead, oneTime int
	var burnedAt sql.NullString
	err := tx.QueryRowContext(ctx, `select coalesce(u.subscription_token,''),coalesce(p.burn_after_read,0),p.burned_at,
		exists(select 1 from subscription_one_time_tokens t where t.user_id=u.id and t.token=?)
		from users u left join subscription_token_policies p on p.user_id=u.id where u.id=? and u.status='active'`, token, userID).Scan(&persistent, &burnAfterRead, &burnedAt, &oneTime)
	if err != nil {
		return "", err
	}
	if persistent == token && (!burnedAt.Valid || strings.TrimSpace(burnedAt.String) == "") {
		if burnAfterRead != 0 {
			return "burn_after_read", nil
		}
		return "persistent", nil
	}
	if oneTime != 0 {
		return "one_time", nil
	}
	return "", sql.ErrNoRows
}

func consumeSubscriptionTokenTx(ctx context.Context, tx *sql.Tx, userID int64, token, tokenKind string, at time.Time) (bool, error) {
	ts := at.UTC().Format(time.RFC3339Nano)
	switch tokenKind {
	case "persistent":
		return false, nil
	case "one_time":
		res, err := tx.ExecContext(ctx, `delete from subscription_one_time_tokens where token=? and user_id=?`, token, userID)
		if err != nil {
			return false, err
		}
		n, err := res.RowsAffected()
		return n == 1, err
	case "burn_after_read":
		res, err := tx.ExecContext(ctx, `update subscription_token_policies set burned_at=?,updated_at=? where user_id=? and burn_after_read=1 and burned_at is null`, ts, ts, userID)
		if err != nil {
			return false, err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			if err != nil {
				return false, err
			}
			return false, sql.ErrNoRows
		}
		res, err = tx.ExecContext(ctx, `update users set subscription_token=null,updated_at=? where id=? and subscription_token=? and status='active'`, ts, userID, token)
		if err != nil {
			return false, err
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			if err != nil {
				return false, err
			}
			return false, sql.ErrNoRows
		}
		return true, nil
	default:
		return false, sql.ErrNoRows
	}
}

func (s *Store) AddRejectedSubscriptionPullAudit(ctx context.Context, token string, event model.SubscriptionPullAudit) error {
	if event.UserID <= 0 {
		return sql.ErrNoRows
	}
	if event.RequestedAt.IsZero() {
		event.RequestedAt = time.Now().UTC()
	}
	event.RiskEligible = false
	if event.Outcome == "" {
		event.Outcome = "rejected_invalid_request"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tokenKind, err := subscriptionTokenKind(ctx, tx, event.UserID, token)
	if err != nil {
		return err
	}
	event.TokenKind = tokenKind
	if _, err := insertSubscriptionPullAudit(ctx, tx, event); err != nil {
		return err
	}
	if err := pruneSubscriptionAudits(ctx, tx, event.RequestedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func insertSubscriptionPullAudit(ctx context.Context, tx *sql.Tx, event model.SubscriptionPullAudit) (int64, error) {
	ts := now()
	res, err := tx.ExecContext(ctx, `insert into subscription_pull_audits(user_id,source_ip,source_country_code,source_country,source_province,source_city,source_isp,geo_database_revision,user_agent,client_name,format,profile_id,age_encrypted,token_kind,outcome,reason,risk_eligible,requested_at,created_at)
		values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.UserID, event.SourceIP, event.SourceCountryCode, event.SourceCountry, event.SourceProvince, event.SourceCity, event.SourceISP, event.GeoDatabaseRevision, event.UserAgent, event.ClientName, event.Format, event.ProfileID, boolInt(event.AgeEncrypted), event.TokenKind, event.Outcome, event.Reason, boolInt(event.RiskEligible), event.RequestedAt.UTC().Format(time.RFC3339Nano), ts)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func pruneSubscriptionAudits(ctx context.Context, tx *sql.Tx, at time.Time) error {
	_, err := tx.ExecContext(ctx, `delete from subscription_pull_audits where requested_at<?`, at.UTC().Add(-subscriptionAuditRetention).Format(time.RFC3339Nano))
	return err
}

func subscriptionEvaluationStart(state model.SubscriptionAccessState, fallback time.Time) string {
	if !state.EvaluationStartedAt.IsZero() {
		return state.EvaluationStartedAt.UTC().Format(time.RFC3339Nano)
	}
	return fallback.UTC().Add(-subscriptionAuditRetention).Format(time.RFC3339Nano)
}

type subscriptionAuditWindow struct {
	pulls   int
	ips     map[string]struct{}
	regions map[string]string
	clients map[string]struct{}
}

func newSubscriptionAuditWindow() subscriptionAuditWindow {
	return subscriptionAuditWindow{ips: map[string]struct{}{}, regions: map[string]string{}, clients: map[string]struct{}{}}
}

func (w *subscriptionAuditWindow) add(sourceIP, countryCode, country, province, clientName, format string, encrypted bool) {
	w.pulls++
	if connectionAuditPublicSourceIP(sourceIP) {
		w.ips[sourceIP] = struct{}{}
		if key, label := connectionAuditRegion(countryCode, country, province); key != "" {
			w.regions[key] = label
		}
	}
	clientName = strings.TrimSpace(clientName)
	if clientName == "" {
		clientName = "unknown"
	}
	w.clients[clientName+"|"+strings.TrimSpace(format)+"|"+fmt.Sprint(encrypted)] = struct{}{}
}

func evaluateSubscriptionAuditRisk(ctx context.Context, q subscriptionAuditQueryer, userID int64, at time.Time, policy model.SubscriptionAuditPolicy, evaluationStartedAt time.Time) (model.SubscriptionAuditRisk, error) {
	shortSince := at.Add(-time.Duration(policy.ShortWindowMinutes) * time.Minute)
	longSince := at.Add(-time.Duration(policy.LongWindowHours) * time.Hour)
	if !evaluationStartedAt.IsZero() {
		if evaluationStartedAt.After(shortSince) {
			shortSince = evaluationStartedAt
		}
		if evaluationStartedAt.After(longSince) {
			longSince = evaluationStartedAt
		}
	}
	rows, err := q.QueryContext(ctx, `select source_ip,source_country_code,source_country,source_province,client_name,format,age_encrypted,requested_at from subscription_pull_audits
		where user_id=? and risk_eligible=1 and requested_at>=? and requested_at<=? order by requested_at`, userID, longSince.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return model.SubscriptionAuditRisk{}, err
	}
	defer rows.Close()
	shortWindow, longWindow := newSubscriptionAuditWindow(), newSubscriptionAuditWindow()
	for rows.Next() {
		var sourceIP, countryCode, country, province, clientName, format, requestedAt string
		var encrypted int
		if err := rows.Scan(&sourceIP, &countryCode, &country, &province, &clientName, &format, &encrypted, &requestedAt); err != nil {
			return model.SubscriptionAuditRisk{}, err
		}
		when := parseTime(requestedAt)
		longWindow.add(sourceIP, countryCode, country, province, clientName, format, encrypted != 0)
		if !when.Before(shortSince) {
			shortWindow.add(sourceIP, countryCode, country, province, clientName, format, encrypted != 0)
		}
	}
	if err := rows.Err(); err != nil {
		return model.SubscriptionAuditRisk{}, err
	}
	risk := model.SubscriptionAuditRisk{
		Short: subscriptionWindowSnapshot(shortWindow, policy.ShortWindowMinutes),
		Long:  subscriptionWindowSnapshot(longWindow, policy.LongWindowHours*60),
	}
	risk.Score, risk.Signals, risk.HardBlock, risk.Reason = subscriptionAuditScore(risk.Short, risk.Long, policy)
	risk.Level = auditRiskLevel(risk.Score)
	return risk, nil
}

func subscriptionWindowSnapshot(window subscriptionAuditWindow, minutes int) model.SubscriptionAuditWindowSnapshot {
	regions := make([]string, 0, len(window.regions))
	for _, label := range window.regions {
		regions = append(regions, label)
	}
	sort.Strings(regions)
	return model.SubscriptionAuditWindowSnapshot{WindowMinutes: minutes, PullCount: window.pulls, SourceIPCount: len(window.ips), RegionCount: len(window.regions), ClientFormatCount: len(window.clients), Regions: regions}
}

func subscriptionAuditScore(short, long model.SubscriptionAuditWindowSnapshot, policy model.SubscriptionAuditPolicy) (int, []string, bool, string) {
	type windowRule struct {
		label     string
		snapshot  model.SubscriptionAuditWindowSnapshot
		threshold model.SubscriptionAuditThresholds
	}
	windows := []windowRule{{fmt.Sprintf("%d 分钟", policy.ShortWindowMinutes), short, policy.Short}, {fmt.Sprintf("%d 小时", policy.LongWindowHours), long, policy.Long}}
	score := 0
	signals := []string{}
	hardBlock := false
	reason := ""
	regionPoints := 0
	ipReached, pullsReached, clientsReached := false, false, false
	for _, window := range windows {
		if window.snapshot.RegionCount >= window.threshold.RegionLimit {
			regionPoints = 75
			hardBlock = true
			signal := fmt.Sprintf("%s内达到 %d 个地域：%s", window.label, window.snapshot.RegionCount, strings.Join(window.snapshot.Regions, "、"))
			signals = append(signals, signal)
			if reason == "" {
				reason = signal
			}
		} else if window.snapshot.RegionCount >= 2 && window.snapshot.RegionCount == window.threshold.RegionLimit-1 && regionPoints < 35 {
			regionPoints = 35
			signals = append(signals, fmt.Sprintf("%s内已出现 %d 个地域", window.label, window.snapshot.RegionCount))
		}
		if window.snapshot.SourceIPCount >= window.threshold.SourceIPLimit {
			ipReached = true
			signals = append(signals, fmt.Sprintf("%s内出现 %d 个独立 IP", window.label, window.snapshot.SourceIPCount))
		}
		if window.snapshot.PullCount >= window.threshold.PullLimit {
			pullsReached = true
			signals = append(signals, fmt.Sprintf("%s内拉取 %d 次", window.label, window.snapshot.PullCount))
		}
		if window.snapshot.ClientFormatCount >= window.threshold.ClientFormatLimit {
			clientsReached = true
			signals = append(signals, fmt.Sprintf("%s内出现 %d 种客户端/格式组合", window.label, window.snapshot.ClientFormatCount))
		}
	}
	score += regionPoints
	if ipReached {
		score += 25
	}
	if pullsReached {
		score += 20
	}
	if clientsReached {
		score += 20
	}
	if score > 100 {
		score = 100
	}
	return score, uniqueStrings(signals), hardBlock, reason
}

func uniqueStrings(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func auditRiskLevel(score int) string {
	switch {
	case score >= 75:
		return "critical"
	case score >= 50:
		return "high"
	case score >= 25:
		return "medium"
	default:
		return "low"
	}
}

func getSubscriptionAccessState(ctx context.Context, q subscriptionAuditQueryer, userID int64) (model.SubscriptionAccessState, error) {
	rows, err := q.QueryContext(ctx, `select user_id,suspended,suspended_at,suspension_reason,trigger_audit_id,trigger_snapshot_json,evaluation_started_at,resumed_at,resumed_by,updated_at from subscription_access_states where user_id=?`, userID)
	if err != nil {
		return model.SubscriptionAccessState{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return model.SubscriptionAccessState{}, err
		}
		return model.SubscriptionAccessState{}, sql.ErrNoRows
	}
	var state model.SubscriptionAccessState
	var suspended int
	var suspendedAt, resumedAt, triggerRaw sql.NullString
	var triggerID, resumedBy sql.NullInt64
	var evaluationStartedAt, updatedAt string
	if err := rows.Scan(&state.UserID, &suspended, &suspendedAt, &state.Reason, &triggerID, &triggerRaw, &evaluationStartedAt, &resumedAt, &resumedBy, &updatedAt); err != nil {
		return model.SubscriptionAccessState{}, err
	}
	state.Suspended = suspended != 0
	state.SuspendedAt = parseNullTime(suspendedAt)
	state.ResumedAt = parseNullTime(resumedAt)
	state.EvaluationStartedAt = parseTime(evaluationStartedAt)
	state.UpdatedAt = parseTime(updatedAt)
	if triggerID.Valid {
		state.TriggerAuditID = &triggerID.Int64
	}
	if resumedBy.Valid {
		state.ResumedBy = &resumedBy.Int64
	}
	if triggerRaw.Valid && strings.TrimSpace(triggerRaw.String) != "" {
		var risk model.SubscriptionAuditRisk
		if json.Unmarshal([]byte(triggerRaw.String), &risk) == nil {
			state.TriggerRisk = &risk
		}
	}
	return state, nil
}

func (s *Store) GetSubscriptionAccessState(ctx context.Context, userID int64) (model.SubscriptionAccessState, error) {
	state, err := getSubscriptionAccessState(ctx, s.db, userID)
	if errors.Is(err, sql.ErrNoRows) {
		if _, userErr := s.GetUser(ctx, userID); userErr != nil {
			return state, userErr
		}
		state.UserID = userID
		return state, nil
	}
	return state, err
}

func (s *Store) ResumeSubscriptionAccess(ctx context.Context, userID, actorID int64) (model.SubscriptionAccessState, error) {
	if userID <= 0 || actorID <= 0 {
		return model.SubscriptionAccessState{}, sql.ErrNoRows
	}
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into subscription_access_states(user_id,suspended,suspended_at,suspension_reason,trigger_audit_id,trigger_snapshot_json,evaluation_started_at,resumed_at,resumed_by,updated_at)
		select id,0,null,'',null,'',?,?,?,? from users where id=?
		on conflict(user_id) do update set suspended=0,suspended_at=null,suspension_reason='',trigger_audit_id=null,trigger_snapshot_json='',evaluation_started_at=excluded.evaluation_started_at,resumed_at=excluded.resumed_at,resumed_by=excluded.resumed_by,updated_at=excluded.updated_at`, ts, ts, actorID, ts, userID)
	if err != nil {
		return model.SubscriptionAccessState{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return model.SubscriptionAccessState{}, err
		}
		return model.SubscriptionAccessState{}, sql.ErrNoRows
	}
	return s.GetSubscriptionAccessState(ctx, userID)
}

func (s *Store) SubscriptionAuditCurrentRisk(ctx context.Context, userID int64, at time.Time, policy model.SubscriptionAuditPolicy) (model.SubscriptionAuditRisk, model.SubscriptionAccessState, error) {
	state, err := s.GetSubscriptionAccessState(ctx, userID)
	if err != nil {
		return model.SubscriptionAuditRisk{}, state, err
	}
	if state.Suspended && state.TriggerRisk != nil {
		return *state.TriggerRisk, state, nil
	}
	risk, err := evaluateSubscriptionAuditRisk(ctx, s.db, userID, at.UTC(), policy, state.EvaluationStartedAt)
	return risk, state, err
}

type subscriptionAuditSummaryBuilder struct {
	item    model.SubscriptionAuditUserSummary
	ips     map[string]struct{}
	regions map[string]struct{}
	clients map[string]struct{}
}

func (s *Store) SubscriptionAuditOverview(ctx context.Context, windowHours int, policy model.SubscriptionAuditPolicy) (model.SubscriptionAuditOverview, error) {
	if windowHours < 1 {
		windowHours = 24
	}
	if windowHours > 30*24 {
		windowHours = 30 * 24
	}
	at := time.Now().UTC()
	overview := model.SubscriptionAuditOverview{WindowHours: windowHours, GeneratedAt: at, Policy: policy, Users: []model.SubscriptionAuditUserSummary{}}
	rows, err := s.db.QueryContext(ctx, `select a.user_id,u.username,u.nickname,a.source_ip,a.source_country_code,a.source_country,a.source_province,a.client_name,a.format,a.age_encrypted,a.outcome,a.requested_at
		from subscription_pull_audits a join users u on u.id=a.user_id where a.requested_at>=? order by a.requested_at`, at.Add(-time.Duration(windowHours)*time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		return overview, err
	}
	builders := map[int64]*subscriptionAuditSummaryBuilder{}
	allIPs := map[string]struct{}{}
	for rows.Next() {
		var userID int64
		var username, nickname, sourceIP, countryCode, country, province, clientName, format, outcome, requestedAt string
		var encrypted int
		if err := rows.Scan(&userID, &username, &nickname, &sourceIP, &countryCode, &country, &province, &clientName, &format, &encrypted, &outcome, &requestedAt); err != nil {
			return overview, errors.Join(err, rows.Close())
		}
		builder := builders[userID]
		if builder == nil {
			builder = &subscriptionAuditSummaryBuilder{item: model.SubscriptionAuditUserSummary{UserID: userID, Username: username, Nickname: nickname}, ips: map[string]struct{}{}, regions: map[string]struct{}{}, clients: map[string]struct{}{}}
			builders[userID] = builder
		}
		builder.item.PullCount++
		overview.TotalPulls++
		if outcome == "served" {
			builder.item.SuccessfulCount++
		} else if strings.HasPrefix(outcome, "denied_") {
			builder.item.DeniedCount++
		}
		when := parseTime(requestedAt)
		if when.After(builder.item.LastSeenAt) {
			builder.item.LastSeenAt = when
		}
		if connectionAuditPublicSourceIP(sourceIP) {
			builder.ips[sourceIP] = struct{}{}
			allIPs[sourceIP] = struct{}{}
			if key, _ := connectionAuditRegion(countryCode, country, province); key != "" {
				builder.regions[key] = struct{}{}
			}
		}
		builder.clients[clientName+"|"+format+"|"+fmt.Sprint(encrypted != 0)] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return overview, err
	}
	if err := rows.Err(); err != nil {
		return overview, err
	}
	stateRows, err := s.db.QueryContext(ctx, `select s.user_id,u.username,u.nickname from subscription_access_states s join users u on u.id=s.user_id where s.suspended=1`)
	if err != nil {
		return overview, err
	}
	for stateRows.Next() {
		var userID int64
		var username, nickname string
		if err := stateRows.Scan(&userID, &username, &nickname); err != nil {
			return overview, errors.Join(err, stateRows.Close())
		}
		if builders[userID] == nil {
			builders[userID] = &subscriptionAuditSummaryBuilder{item: model.SubscriptionAuditUserSummary{UserID: userID, Username: username, Nickname: nickname}, ips: map[string]struct{}{}, regions: map[string]struct{}{}, clients: map[string]struct{}{}}
		}
	}
	if err := stateRows.Close(); err != nil {
		return overview, err
	}
	for userID, builder := range builders {
		risk, state, err := s.SubscriptionAuditCurrentRisk(ctx, userID, at, policy)
		if err != nil {
			return overview, err
		}
		builder.item.SourceIPCount = len(builder.ips)
		builder.item.RegionCount = len(builder.regions)
		builder.item.ClientFormatCount = len(builder.clients)
		builder.item.RiskLevel = risk.Level
		builder.item.RiskScore = risk.Score
		builder.item.RiskSignals = append([]string(nil), risk.Signals...)
		builder.item.CurrentRisk = risk
		builder.item.Suspended = state.Suspended
		builder.item.SuspendedAt = state.SuspendedAt
		builder.item.SuspensionReason = state.Reason
		if state.Suspended {
			overview.SuspendedCount++
		}
		if risk.Level == "high" || risk.Level == "critical" {
			overview.ElevatedRiskCount++
		}
		overview.Users = append(overview.Users, builder.item)
	}
	overview.ReportingUsers = len(overview.Users)
	overview.UniqueSourceIPs = len(allIPs)
	sort.SliceStable(overview.Users, func(i, j int) bool {
		if overview.Users[i].RiskScore != overview.Users[j].RiskScore {
			return overview.Users[i].RiskScore > overview.Users[j].RiskScore
		}
		return overview.Users[i].LastSeenAt.After(overview.Users[j].LastSeenAt)
	})
	return overview, nil
}

func (s *Store) SubscriptionAuditUserDetail(ctx context.Context, userID int64, windowHours int, policy model.SubscriptionAuditPolicy) (model.SubscriptionAuditUserDetail, error) {
	overview, err := s.SubscriptionAuditOverview(ctx, windowHours, policy)
	if err != nil {
		return model.SubscriptionAuditUserDetail{}, err
	}
	detail := model.SubscriptionAuditUserDetail{Sources: []model.SubscriptionAuditDimension{}, Regions: []model.SubscriptionAuditDimension{}, Clients: []model.SubscriptionAuditDimension{}, Formats: []model.SubscriptionAuditDimension{}, Recent: []model.SubscriptionPullAudit{}}
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
	detail.Access, err = s.GetSubscriptionAccessState(ctx, userID)
	if err != nil {
		return detail, err
	}
	since := time.Now().UTC().Add(-time.Duration(overview.WindowHours) * time.Hour).Format(time.RFC3339Nano)
	detail.Recent, err = s.listRecentSubscriptionAudits(ctx, userID, since, 100)
	if err != nil {
		return detail, err
	}
	detail.Sources, err = s.subscriptionAuditDimensions(ctx, `select source_ip,source_ip,trim(case when max(source_province)<>'' then max(source_province) else max(source_country) end||case when max(source_city)='' then '' else ' / '||max(source_city) end||case when max(source_isp)='' then '' else ' / '||max(source_isp) end),count(*),max(requested_at) from subscription_pull_audits where user_id=? and requested_at>=? group by source_ip order by count(*) desc limit 50`, userID, since)
	if err != nil {
		return detail, err
	}
	detail.Regions, err = s.subscriptionAuditDimensions(ctx, `select case when source_country_code='CN' and source_province<>'' then 'CN/'||source_province when source_country_code<>'' and source_country_code<>'CN' then 'COUNTRY/'||source_country_code else '' end,case when source_country_code='CN' then source_province else source_country end,'',count(*),max(requested_at) from subscription_pull_audits where user_id=? and requested_at>=? and source_country_code<>'' group by 1,2 order by count(*) desc limit 50`, userID, since)
	if err != nil {
		return detail, err
	}
	detail.Clients, err = s.subscriptionAuditDimensions(ctx, `select client_name,client_name,'',count(*),max(requested_at) from subscription_pull_audits where user_id=? and requested_at>=? group by client_name order by count(*) desc limit 50`, userID, since)
	if err != nil {
		return detail, err
	}
	detail.Formats, err = s.subscriptionAuditDimensions(ctx, `select format||':'||age_encrypted,format,case when age_encrypted=1 then 'Age' else '普通' end,count(*),max(requested_at) from subscription_pull_audits where user_id=? and requested_at>=? group by format,age_encrypted order by count(*) desc limit 50`, userID, since)
	return detail, err
}

func (s *Store) subscriptionAuditDimensions(ctx context.Context, query string, args ...any) ([]model.SubscriptionAuditDimension, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SubscriptionAuditDimension{}
	for rows.Next() {
		var item model.SubscriptionAuditDimension
		var lastSeen string
		if err := rows.Scan(&item.Key, &item.Label, &item.Secondary, &item.PullCount, &lastSeen); err != nil {
			return nil, err
		}
		item.LastSeenAt = parseTime(lastSeen)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) listRecentSubscriptionAudits(ctx context.Context, userID int64, since string, limit int) ([]model.SubscriptionPullAudit, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,source_ip,source_country_code,source_country,source_province,source_city,source_isp,geo_database_revision,user_agent,client_name,format,profile_id,age_encrypted,token_kind,outcome,reason,risk_eligible,requested_at,created_at from subscription_pull_audits where user_id=? and requested_at>=? order by requested_at desc limit ?`, userID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SubscriptionPullAudit{}
	for rows.Next() {
		var item model.SubscriptionPullAudit
		var profileID sql.NullInt64
		var encrypted, eligible int
		var requestedAt, createdAt string
		if err := rows.Scan(&item.ID, &item.UserID, &item.SourceIP, &item.SourceCountryCode, &item.SourceCountry, &item.SourceProvince, &item.SourceCity, &item.SourceISP, &item.GeoDatabaseRevision, &item.UserAgent, &item.ClientName, &item.Format, &profileID, &encrypted, &item.TokenKind, &item.Outcome, &item.Reason, &eligible, &requestedAt, &createdAt); err != nil {
			return nil, err
		}
		if profileID.Valid {
			item.ProfileID = &profileID.Int64
		}
		item.AgeEncrypted = encrypted != 0
		item.RiskEligible = eligible != 0
		item.RequestedAt = parseTime(requestedAt)
		item.CreatedAt = parseTime(createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}
