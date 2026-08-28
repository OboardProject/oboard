package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
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
	RateLimited   bool
	RetryAfter    time.Duration
	DeviceID      string
	JustSuspended bool
	Warned        bool
	Access        model.SubscriptionAccessState
	Risk          model.SubscriptionAuditRisk
}

type SubscriptionAuditOptions struct {
	AuditEnabled bool
	Action       model.AuditAction
}

func DefaultAuditPolicy() model.AuditPolicy {
	return AuditPolicyPreset("balanced")
}

// DefaultSubscriptionAuditPolicy remains the internal constructor name used by
// existing callers while the persisted contract is now the global AuditPolicy.
func DefaultSubscriptionAuditPolicy() model.AuditPolicy { return DefaultAuditPolicy() }

func AuditPolicyPreset(mode string) model.AuditPolicy {
	threshold := func(soft, hard int) model.AuditThreshold { return model.AuditThreshold{Soft: soft, Hard: hard} }
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "loose":
		return model.AuditPolicy{Mode: "loose", RawRequestsPer60Seconds: threshold(60, 180), LogicalPullsPer10Minutes: threshold(20, 60), LogicalPullsPer24Hours: threshold(180, 480), RoutesPer15Minutes: threshold(6, 12), ClientFamiliesPer24Hours: threshold(8, 14), ConcurrentRoutes90Secs: threshold(3, 5), NodeFanout10Seconds: threshold(20, 40), ProbeEpisodes10Minutes: threshold(20, 60), ActiveConnections: threshold(192, 768), LegacyDeviceExcess: threshold(3, 5), CloneOverlapSeconds: 120, AutoActionConfidence: 0.80}
	case "strict":
		return model.AuditPolicy{Mode: "strict", RawRequestsPer60Seconds: threshold(20, 60), LogicalPullsPer10Minutes: threshold(8, 24), LogicalPullsPer24Hours: threshold(48, 144), RoutesPer15Minutes: threshold(3, 6), ClientFamiliesPer24Hours: threshold(4, 7), ConcurrentRoutes90Secs: threshold(2, 3), NodeFanout10Seconds: threshold(8, 16), ProbeEpisodes10Minutes: threshold(8, 20), ActiveConnections: threshold(96, 320), LegacyDeviceExcess: threshold(1, 2), CloneOverlapSeconds: 30, AutoActionConfidence: 0.80}
	default:
		return model.AuditPolicy{Mode: "balanced", RawRequestsPer60Seconds: threshold(30, 120), LogicalPullsPer10Minutes: threshold(12, 36), LogicalPullsPer24Hours: threshold(96, 288), RoutesPer15Minutes: threshold(4, 8), ClientFamiliesPer24Hours: threshold(6, 10), ConcurrentRoutes90Secs: threshold(2, 4), NodeFanout10Seconds: threshold(12, 24), ProbeEpisodes10Minutes: threshold(12, 30), ActiveConnections: threshold(128, 512), LegacyDeviceExcess: threshold(2, 3), CloneOverlapSeconds: 60, AutoActionConfidence: 0.80}
	}
}

func ValidateAuditPolicy(policy model.AuditPolicy) error {
	switch policy.Mode {
	case "loose", "balanced", "strict", "custom":
	default:
		return errors.New("audit policy mode must be loose, balanced, strict or custom")
	}
	for name, threshold := range map[string]model.AuditThreshold{
		"raw requests":                 policy.RawRequestsPer60Seconds,
		"logical pulls per 10 minutes": policy.LogicalPullsPer10Minutes,
		"logical pulls per 24 hours":   policy.LogicalPullsPer24Hours,
		"routes":                       policy.RoutesPer15Minutes,
		"client families":              policy.ClientFamiliesPer24Hours,
		"concurrent routes":            policy.ConcurrentRoutes90Secs,
		"node fanout":                  policy.NodeFanout10Seconds,
		"probe episodes":               policy.ProbeEpisodes10Minutes,
		"active connections":           policy.ActiveConnections,
		"legacy device excess":         policy.LegacyDeviceExcess,
	} {
		if threshold.Soft < 0 || threshold.Hard <= threshold.Soft || threshold.Hard > 1_000_000 {
			return fmt.Errorf("audit policy %s must use 0 <= soft < hard <= 1000000", name)
		}
	}
	if policy.CloneOverlapSeconds < 10 || policy.CloneOverlapSeconds > 600 {
		return errors.New("audit policy clone overlap must be between 10 and 600 seconds")
	}
	if policy.AutoActionConfidence < 0.5 || policy.AutoActionConfidence > 1 {
		return errors.New("audit policy auto action confidence must be between 0.5 and 1")
	}
	return nil
}

func ValidateSubscriptionAuditPolicy(policy model.AuditPolicy) error {
	return ValidateAuditPolicy(policy)
}

func (s *Store) AuthorizeSubscriptionPull(ctx context.Context, userID int64, token string, event model.SubscriptionPullAudit, policy model.AuditPolicy, options SubscriptionAuditOptions) (SubscriptionPullDecision, error) {
	return s.authorizeSubscriptionPull(ctx, userID, token, false, "", "", event, policy, options)
}

func (s *Store) AuthorizeCustomSubscriptionPull(ctx context.Context, userID int64, alias string, event model.SubscriptionPullAudit, policy model.AuditPolicy, options SubscriptionAuditOptions) (SubscriptionPullDecision, error) {
	return s.authorizeSubscriptionPull(ctx, userID, alias, true, "", "", event, policy, options)
}

func (s *Store) AuthorizeDeviceSubscriptionPull(ctx context.Context, userID int64, deviceID, tokenHash string, event model.SubscriptionPullAudit, policy model.AuditPolicy, options SubscriptionAuditOptions) (SubscriptionPullDecision, error) {
	return s.authorizeSubscriptionPull(ctx, userID, "", false, deviceID, tokenHash, event, policy, options)
}

func (s *Store) authorizeSubscriptionPull(ctx context.Context, userID int64, credential string, custom bool, deviceID, deviceTokenHash string, event model.SubscriptionPullAudit, policy model.AuditPolicy, options SubscriptionAuditOptions) (SubscriptionPullDecision, error) {
	decision := SubscriptionPullDecision{}
	if err := ValidateAuditPolicy(policy); err != nil {
		return decision, err
	}
	if userID <= 0 || (strings.TrimSpace(credential) == "" && strings.TrimSpace(deviceID) == "") {
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
	tokenKind := "device"
	if deviceID != "" {
		var deviceHash string
		var suspended int
		err = tx.QueryRowContext(ctx, `select device_id_hash,subscription_suspended from user_devices where id=? and user_id=? and token_hash=? and status='active'`, deviceID, userID, deviceTokenHash).Scan(&deviceHash, &suspended)
		if err != nil {
			return decision, err
		}
		event.DeviceIDHash = deviceHash
		decision.DeviceID = deviceID
		if suspended != 0 {
			if options.AuditEnabled {
				event.TokenKind = tokenKind
				event.Outcome = "denied_device_suspended"
				event.Reason = "设备订阅凭证已暂停"
				event.RiskEligible = false
				decision.AuditID, err = insertSubscriptionPullAudit(ctx, tx, event)
				if err != nil {
					return decision, err
				}
			}
			return decision, tx.Commit()
		}
	} else {
		tokenKind, err = subscriptionCredentialKind(ctx, tx, userID, credential, custom)
		if err != nil {
			return decision, err
		}
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
		if options.AuditEnabled {
			event.Outcome = "denied_suspended"
			event.Reason = state.Reason
			event.RiskEligible = false
			decision.AuditID, err = insertSubscriptionPullAudit(ctx, tx, event)
			if err != nil {
				return decision, err
			}
		}
		decision.Access = state
		if state.TriggerRisk != nil {
			decision.Risk = *state.TriggerRisk
		}
		return decision, tx.Commit()
	}
	if !options.AuditEnabled {
		decision.Burned, err = consumeSubscriptionTokenTx(ctx, tx, userID, credential, tokenKind, event.RequestedAt)
		if err != nil {
			return decision, err
		}
		if deviceID != "" {
			_, err = tx.ExecContext(ctx, `update user_devices set last_subscription_at=?,updated_at=? where id=? and user_id=?`, event.RequestedAt.Format(time.RFC3339Nano), now(), deviceID, userID)
			if err != nil {
				return decision, err
			}
		}
		decision.Allowed = true
		decision.Access = state
		return decision, tx.Commit()
	}
	event.Outcome = "pending"
	event.RiskEligible = true
	if err := prepareSubscriptionAuditEvent(ctx, tx, &event); err != nil {
		return decision, err
	}
	rateLimited, retryAfter, err := consumeSubscriptionRateBuckets(ctx, tx, event, policy)
	if err != nil {
		return decision, err
	}
	if rateLimited {
		event.Outcome = "rate_limited"
		event.Reason = "原始订阅请求频率超过资源保护上限"
	}
	decision.AuditID, err = insertSubscriptionPullAudit(ctx, tx, event)
	if err != nil {
		return decision, err
	}
	risk, err := evaluateSubscriptionAuditRisk(ctx, tx, userID, event.RequestedAt, policy, state.EvaluationStartedAt)
	if err != nil {
		return decision, err
	}
	decision.Risk = risk
	if rateLimited {
		decision.RateLimited = true
		decision.RetryAfter = retryAfter
		decision.Access = state
		return decision, tx.Commit()
	}
	if risk.HardBlock {
		if options.Action == model.AuditActionWarn {
			if _, err := tx.ExecContext(ctx, `update subscription_pull_audits set outcome='served_risk_warn',reason=? where id=?`, risk.Reason, decision.AuditID); err != nil {
				return decision, err
			}
			decision.Burned, err = consumeSubscriptionTokenTx(ctx, tx, userID, credential, tokenKind, event.RequestedAt)
			if err != nil {
				return decision, err
			}
			decision.Allowed = true
			decision.Warned = true
			decision.Access = state
		} else if deviceID != "" {
			if _, err := tx.ExecContext(ctx, `update subscription_pull_audits set outcome='denied_risk',reason=? where id=?`, risk.Reason, decision.AuditID); err != nil {
				return decision, err
			}
			ts := event.RequestedAt.Format(time.RFC3339Nano)
			if _, err := tx.ExecContext(ctx, `update user_devices set subscription_suspended=1,subscription_suspended_at=?,updated_at=? where id=? and user_id=? and status='active'`, ts, ts, deviceID, userID); err != nil {
				return decision, err
			}
			decision.Access = state
			decision.JustSuspended = true
		} else {
			// Legacy tokens do not carry enough identity evidence for an automatic
			// suspension. Keep serving and surface the risk for operator review.
			if _, err := tx.ExecContext(ctx, `update subscription_pull_audits set outcome='served_risk_warn',reason=? where id=?`, risk.Reason, decision.AuditID); err != nil {
				return decision, err
			}
			decision.Burned, err = consumeSubscriptionTokenTx(ctx, tx, userID, credential, tokenKind, event.RequestedAt)
			if err != nil {
				return decision, err
			}
			decision.Allowed = true
			decision.Warned = true
			decision.Access = state
		}
	} else {
		if _, err := tx.ExecContext(ctx, `update subscription_pull_audits set outcome='served' where id=?`, decision.AuditID); err != nil {
			return decision, err
		}
		decision.Burned, err = consumeSubscriptionTokenTx(ctx, tx, userID, credential, tokenKind, event.RequestedAt)
		if err != nil {
			return decision, err
		}
		decision.Allowed = true
		decision.Access = state
	}
	if decision.Allowed && deviceID != "" {
		if _, err := tx.ExecContext(ctx, `update user_devices set last_subscription_at=?,updated_at=? where id=? and user_id=?`, event.RequestedAt.Format(time.RFC3339Nano), now(), deviceID, userID); err != nil {
			return decision, err
		}
	}
	return decision, tx.Commit()
}

func subscriptionCredentialKind(ctx context.Context, tx *sql.Tx, userID int64, credential string, custom bool) (string, error) {
	if custom {
		var exists int
		err := tx.QueryRowContext(ctx, `select exists(select 1 from subscription_custom_paths p join users u on u.id=p.user_id where p.user_id=? and p.alias=? and u.status='active')`, userID, credential).Scan(&exists)
		if err != nil || exists == 0 {
			if err != nil {
				return "", err
			}
			return "", sql.ErrNoRows
		}
		return "custom_path", nil
	}
	return subscriptionTokenKind(ctx, tx, userID, credential)
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
	case "persistent", "custom_path", "device":
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

func prepareSubscriptionAuditEvent(ctx context.Context, tx *sql.Tx, event *model.SubscriptionPullAudit) error {
	if event == nil {
		return errors.New("subscription audit event is required")
	}
	event.RawRequestWeight = 1
	event.ClientName = normalizeAuditClientFamily(event.ClientName)
	event.Format = normalizeAuditFormatFamily(event.Format)
	if strings.TrimSpace(event.RepresentationID) == "" {
		profileID := int64(0)
		if event.ProfileID != nil {
			profileID = *event.ProfileID
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%d\x00%t\x00%s", event.UserID, event.DeviceIDHash, event.Format, profileID, event.AgeEncrypted, event.SubscriptionRevision)))
		event.RepresentationID = fmt.Sprintf("rep_%x", sum[:16])
	}
	event.LogicalPullWeight = 1
	identityClause := `device_id_hash=''`
	identityArgs := []any{}
	dedupeSince := event.RequestedAt.Add(-15 * time.Second)
	if event.DeviceIDHash != "" {
		identityClause = `device_id_hash=?`
		identityArgs = append(identityArgs, event.DeviceIDHash)
		dedupeSince = event.RequestedAt.Add(-90 * time.Second)
	}
	query := `select logical_fetch_id,route_id from subscription_pull_audits where user_id=? and ` + identityClause + ` and representation_id=? and subscription_revision=? and risk_eligible=1 and requested_at>=? and requested_at<=? order by requested_at desc limit 20`
	args := []any{event.UserID}
	args = append(args, identityArgs...)
	args = append(args, event.RepresentationID, event.SubscriptionRevision, dedupeSince.Format(time.RFC3339Nano), event.RequestedAt.Format(time.RFC3339Nano))
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	previousFetchID := ""
	routes := map[string]struct{}{}
	matched := false
	for rows.Next() {
		var fetchID, routeID string
		if err := rows.Scan(&fetchID, &routeID); err != nil {
			return errors.Join(err, rows.Close())
		}
		matched = true
		if previousFetchID == "" {
			previousFetchID = fetchID
		}
		if routeID != "" {
			routes[routeID] = struct{}{}
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if matched {
		if event.DeviceIDHash != "" {
			event.LogicalPullWeight = 0
			event.DedupeReason = "same_device_representation_retry"
		} else {
			if event.RouteID != "" {
				routes[event.RouteID] = struct{}{}
			}
			if len(routes) <= 2 {
				event.LogicalPullWeight = 0.25
				event.DedupeReason = "legacy_two_route_retry"
			}
		}
	}
	if event.ConditionalRequest {
		event.LogicalPullWeight = 0.10
		event.DedupeReason = "http_304"
	}
	if previousFetchID != "" && event.LogicalPullWeight < 1 {
		event.LogicalFetchID = previousFetchID
	} else {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%d", event.UserID, event.DeviceIDHash, event.RepresentationID, event.RequestedAt.UnixNano())))
		event.LogicalFetchID = fmt.Sprintf("lf_%x", sum[:12])
	}
	novelty, err := subscriptionRouteNovelty(ctx, tx, *event)
	if err != nil {
		return err
	}
	event.RouteNoveltyWeight = novelty
	return nil
}

func normalizeAuditClientFamily(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(value, "mihomo"), strings.Contains(value, "clash meta"), strings.Contains(value, "clash-meta"):
		return "mihomo"
	case strings.Contains(value, "clash"):
		return "clash"
	case strings.Contains(value, "sing-box"), strings.Contains(value, "singbox"):
		return "sing-box"
	case strings.Contains(value, "shadowrocket"):
		return "shadowrocket"
	case strings.Contains(value, "surge"):
		return "surge"
	case strings.Contains(value, "quantumult"):
		return "quantumult"
	case strings.Contains(value, "v2ray"), strings.Contains(value, "xray"):
		return "v2ray"
	case value == "":
		return "unknown"
	default:
		if len(value) > 64 {
			return value[:64]
		}
		return value
	}
}

func normalizeAuditFormatFamily(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "mihomo", "clash-meta", "clashmeta", "clash":
		return "clash"
	case "sing-box", "singbox", "sing-box-mieru":
		return "sing-box"
	case "v2ray", "base64", "uri", "uri-list":
		return "uri"
	default:
		if value == "" {
			return "unknown"
		}
		return value
	}
}

func subscriptionRouteNovelty(ctx context.Context, tx *sql.Tx, event model.SubscriptionPullAudit) (float64, error) {
	if event.RouteID == "" {
		return 0, nil
	}
	identityClause := `device_id_hash=''`
	identityArgs := []any{}
	if event.DeviceIDHash != "" {
		identityClause = `device_id_hash=?`
		identityArgs = append(identityArgs, event.DeviceIDHash)
	}
	query := `select route_id,source_country_code,source_isp from subscription_pull_audits where user_id=? and ` + identityClause + ` and risk_eligible=1 and requested_at>=? and requested_at<=? order by requested_at desc limit 100`
	args := []any{event.UserID}
	args = append(args, identityArgs...)
	args = append(args, event.RequestedAt.Add(-15*time.Minute).Format(time.RFC3339Nano), event.RequestedAt.Format(time.RFC3339Nano))
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	novelty := 1.0
	for rows.Next() {
		var routeID, countryCode, isp string
		if err := rows.Scan(&routeID, &countryCode, &isp); err != nil {
			return 0, errors.Join(err, rows.Close())
		}
		if routeID == event.RouteID {
			novelty = 0
			break
		}
		if strings.EqualFold(countryCode, event.SourceCountryCode) {
			if strings.TrimSpace(isp) != "" && strings.EqualFold(strings.TrimSpace(isp), strings.TrimSpace(event.SourceISP)) {
				novelty = math.Min(novelty, 0.25)
			} else {
				novelty = math.Min(novelty, 0.50)
			}
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if novelty == 0 {
		return 0, nil
	}
	var sharedUsers int
	err = tx.QueryRowContext(ctx, `select count(distinct user_id) from subscription_pull_audits where route_id=? and risk_eligible=1 and requested_at>=? and requested_at<=?`, event.RouteID, event.RequestedAt.Add(-15*time.Minute).Format(time.RFC3339Nano), event.RequestedAt.Format(time.RFC3339Nano)).Scan(&sharedUsers)
	if err != nil {
		return 0, err
	}
	sharedUsers++
	discount := math.Max(0.20, 1/math.Sqrt(float64(sharedUsers)))
	return novelty * discount, nil
}

func consumeSubscriptionRateBuckets(ctx context.Context, tx *sql.Tx, event model.SubscriptionPullAudit, policy model.AuditPolicy) (bool, time.Duration, error) {
	keys := []string{"user:" + strconv.FormatInt(event.UserID, 10)}
	if event.DeviceIDHash != "" {
		keys = append(keys, "device:"+event.DeviceIDHash)
	}
	if event.RouteID != "" {
		keys = append(keys, "route:"+event.RouteID)
	}
	limited := false
	retryAfter := time.Duration(0)
	for _, key := range keys {
		hit, retry, err := consumeSubscriptionRateBucket(ctx, tx, key, policy.RawRequestsPer60Seconds.Hard, event.RequestedAt)
		if err != nil {
			return false, 0, err
		}
		limited = limited || hit
		if retry > retryAfter {
			retryAfter = retry
		}
	}
	return limited, retryAfter, nil
}

func consumeSubscriptionRateBucket(ctx context.Context, tx *sql.Tx, key string, capacity int, at time.Time) (bool, time.Duration, error) {
	if capacity <= 0 {
		return false, 0, nil
	}
	level := 0.0
	updatedAt := at
	var rawUpdated string
	err := tx.QueryRowContext(ctx, `select level,updated_at from subscription_rate_buckets where bucket_key=?`, key).Scan(&level, &rawUpdated)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, 0, err
	}
	if err == nil {
		updatedAt = parseTime(rawUpdated)
		elapsed := at.Sub(updatedAt).Seconds()
		if elapsed > 0 {
			level = math.Max(0, level-elapsed*(float64(capacity)/60))
		}
	}
	next := level + 1
	limited := next > float64(capacity)
	stored := math.Min(next, float64(capacity))
	ts := at.Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `insert into subscription_rate_buckets(bucket_key,level,updated_at) values(?,?,?) on conflict(bucket_key) do update set level=excluded.level,updated_at=excluded.updated_at`, key, stored, ts)
	if err != nil {
		return false, 0, err
	}
	if !limited {
		return false, 0, nil
	}
	seconds := math.Ceil((next - float64(capacity)) / (float64(capacity) / 60))
	if seconds < 1 {
		seconds = 1
	}
	return true, time.Duration(seconds) * time.Second, nil
}

func (s *Store) AddRejectedSubscriptionPullAudit(ctx context.Context, token string, event model.SubscriptionPullAudit) error {
	return s.addRejectedSubscriptionPullAudit(ctx, token, false, event)
}

func (s *Store) AddRejectedCustomSubscriptionPullAudit(ctx context.Context, alias string, event model.SubscriptionPullAudit) error {
	return s.addRejectedSubscriptionPullAudit(ctx, alias, true, event)
}

func (s *Store) addRejectedSubscriptionPullAudit(ctx context.Context, credential string, custom bool, event model.SubscriptionPullAudit) error {
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
	tokenKind, err := subscriptionCredentialKind(ctx, tx, event.UserID, credential, custom)
	if err != nil {
		return err
	}
	event.TokenKind = tokenKind
	if _, err := insertSubscriptionPullAudit(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func insertSubscriptionPullAudit(ctx context.Context, tx *sql.Tx, event model.SubscriptionPullAudit) (int64, error) {
	if event.RawRequestWeight <= 0 {
		event.RawRequestWeight = 1
	}
	ts := now()
	res, err := tx.ExecContext(ctx, `insert into subscription_pull_audits(user_id,device_id_hash,representation_id,subscription_revision,raw_request_weight,logical_pull_weight,logical_fetch_id,route_id,route_novelty_weight,dedupe_reason,conditional_request,source_ip,source_country_code,source_country,source_province,source_city,source_isp,geo_database_revision,user_agent,client_name,format,requested_format,auto_detected,profile_id,age_encrypted,token_kind,outcome,reason,risk_eligible,requested_at,created_at)
		values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.UserID, event.DeviceIDHash, event.RepresentationID, event.SubscriptionRevision, event.RawRequestWeight, event.LogicalPullWeight, event.LogicalFetchID, event.RouteID, event.RouteNoveltyWeight, event.DedupeReason, boolInt(event.ConditionalRequest), event.SourceIP, event.SourceCountryCode, event.SourceCountry, event.SourceProvince, event.SourceCity, event.SourceISP, event.GeoDatabaseRevision, event.UserAgent, event.ClientName, event.Format, event.RequestedFormat, boolInt(event.AutoDetected), event.ProfileID, boolInt(event.AgeEncrypted), event.TokenKind, event.Outcome, event.Reason, boolInt(event.RiskEligible), event.RequestedAt.UTC().Format(time.RFC3339Nano), ts)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type subscriptionAuditWindow struct {
	pulls        int
	raw          float64
	logical      float64
	routeNovelty float64
	ips          map[string]struct{}
	routes       map[string]struct{}
	regions      map[string]string
	clients      map[string]struct{}
	formats      map[string]struct{}
	devices      map[string]struct{}
	geoKnown     int
	conditional  int
	deduped      int
	latestDevice string
	latestAt     time.Time
}

func newSubscriptionAuditWindow() subscriptionAuditWindow {
	return subscriptionAuditWindow{ips: map[string]struct{}{}, routes: map[string]struct{}{}, regions: map[string]string{}, clients: map[string]struct{}{}, formats: map[string]struct{}{}, devices: map[string]struct{}{}}
}

func (w *subscriptionAuditWindow) add(sourceIP, routeID, countryCode, country, province, geoRevision, clientName, format, deviceID string, rawWeight, logicalWeight, routeNovelty float64, conditional bool, requestedAt time.Time) {
	w.pulls++
	w.raw += rawWeight
	w.logical += logicalWeight
	w.routeNovelty += routeNovelty
	if connectionAuditPublicSourceIP(sourceIP) {
		w.ips[sourceIP] = struct{}{}
		if key, label := connectionAuditRegion(countryCode, country, province); key != "" {
			w.regions[key] = label
		}
	}
	if routeID != "" {
		w.routes[routeID] = struct{}{}
	}
	w.clients[normalizeAuditClientFamily(clientName)] = struct{}{}
	w.formats[normalizeAuditFormatFamily(format)] = struct{}{}
	if deviceID != "" {
		w.devices[deviceID] = struct{}{}
	}
	if geoRevision != "" {
		w.geoKnown++
	}
	if conditional {
		w.conditional++
	}
	if logicalWeight < 1 {
		w.deduped++
	}
	if requestedAt.After(w.latestAt) || w.latestAt.IsZero() {
		w.latestAt = requestedAt
		w.latestDevice = deviceID
	}
}

func evaluateSubscriptionAuditRisk(ctx context.Context, q subscriptionAuditQueryer, userID int64, at time.Time, policy model.AuditPolicy, evaluationStartedAt time.Time) (model.SubscriptionAuditRisk, error) {
	shortSince := at.Add(-10 * time.Minute)
	longSince := at.Add(-24 * time.Hour)
	if !evaluationStartedAt.IsZero() {
		if evaluationStartedAt.After(shortSince) {
			shortSince = evaluationStartedAt
		}
		if evaluationStartedAt.After(longSince) {
			longSince = evaluationStartedAt
		}
	}
	rows, err := q.QueryContext(ctx, `select source_ip,route_id,source_country_code,source_country,source_province,geo_database_revision,client_name,format,device_id_hash,raw_request_weight,logical_pull_weight,route_novelty_weight,conditional_request,requested_at from subscription_pull_audits
		where user_id=? and risk_eligible=1 and requested_at>=? and requested_at<=? order by requested_at`, userID, longSince.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return model.SubscriptionAuditRisk{}, err
	}
	defer rows.Close()
	shortWindow, longWindow := newSubscriptionAuditWindow(), newSubscriptionAuditWindow()
	rawWindow, routeWindow := newSubscriptionAuditWindow(), newSubscriptionAuditWindow()
	for rows.Next() {
		var sourceIP, routeID, countryCode, country, province, geoRevision, clientName, format, deviceID, requestedAt string
		var rawWeight, logicalWeight, routeNovelty float64
		var conditional int
		if err := rows.Scan(&sourceIP, &routeID, &countryCode, &country, &province, &geoRevision, &clientName, &format, &deviceID, &rawWeight, &logicalWeight, &routeNovelty, &conditional, &requestedAt); err != nil {
			return model.SubscriptionAuditRisk{}, err
		}
		when := parseTime(requestedAt)
		longWindow.add(sourceIP, routeID, countryCode, country, province, geoRevision, clientName, format, deviceID, rawWeight, logicalWeight, routeNovelty, conditional != 0, when)
		if !when.Before(shortSince) {
			shortWindow.add(sourceIP, routeID, countryCode, country, province, geoRevision, clientName, format, deviceID, rawWeight, logicalWeight, routeNovelty, conditional != 0, when)
		}
		if !when.Before(at.Add(-time.Minute)) {
			rawWindow.add(sourceIP, routeID, countryCode, country, province, geoRevision, clientName, format, deviceID, rawWeight, logicalWeight, routeNovelty, conditional != 0, when)
		}
		if !when.Before(at.Add(-15 * time.Minute)) {
			routeWindow.add(sourceIP, routeID, countryCode, country, province, geoRevision, clientName, format, deviceID, rawWeight, logicalWeight, routeNovelty, conditional != 0, when)
		}
	}
	if err := rows.Err(); err != nil {
		return model.SubscriptionAuditRisk{}, err
	}
	risk := model.SubscriptionAuditRisk{
		Short: subscriptionWindowSnapshot(shortWindow, 10),
		Long:  subscriptionWindowSnapshot(longWindow, 24*60),
	}
	activeDevices, deviceLimit, err := subscriptionAuditDeviceCounts(ctx, q, userID)
	if err != nil {
		return model.SubscriptionAuditRisk{}, err
	}
	risk = subscriptionAuditScore(risk, rawWindow, routeWindow, longWindow, activeDevices, deviceLimit, policy)
	risk.Level = auditRiskLevel(risk.Score)
	return risk, nil
}

func subscriptionAuditDeviceCounts(ctx context.Context, q subscriptionAuditQueryer, userID int64) (int, int, error) {
	rows, err := q.QueryContext(ctx, `select coalesce(u.device_limit,0),(select count(*) from user_devices d where d.user_id=u.id and d.status='active') from users u where u.id=?`, userID)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, 0, sql.ErrNoRows
	}
	var limit, active int
	if err := rows.Scan(&limit, &active); err != nil {
		return 0, 0, err
	}
	return active, limit, rows.Err()
}

func subscriptionWindowSnapshot(window subscriptionAuditWindow, minutes int) model.SubscriptionAuditWindowSnapshot {
	regions := make([]string, 0, len(window.regions))
	for _, label := range window.regions {
		regions = append(regions, label)
	}
	sort.Strings(regions)
	return model.SubscriptionAuditWindowSnapshot{WindowMinutes: minutes, PullCount: window.pulls, RawRequestCount: int(math.Round(window.raw)), LogicalPullWeight: window.logical, RouteCount: len(window.routes), RouteNovelty: window.routeNovelty, ClientFamilyCount: len(window.clients), FormatFamilyCount: len(window.formats), SourceIPCount: len(window.ips), RegionCount: len(window.regions), ClientFormatCount: len(window.clients) + len(window.formats), Regions: regions}
}

func subscriptionAuditScore(risk model.SubscriptionAuditRisk, rawWindow, routeWindow, longWindow subscriptionAuditWindow, activeDevices, deviceLimit int, policy model.AuditPolicy) model.SubscriptionAuditRisk {
	normalized := func(value float64, threshold model.AuditThreshold) float64 {
		return math.Max(0, math.Min(1, (value-float64(threshold.Soft))/float64(threshold.Hard-threshold.Soft)))
	}
	raw := normalized(rawWindow.raw, policy.RawRequestsPer60Seconds)
	pull := math.Max(normalized(risk.Short.LogicalPullWeight, policy.LogicalPullsPer10Minutes), normalized(risk.Long.LogicalPullWeight, policy.LogicalPullsPer24Hours))
	route := normalized(routeWindow.routeNovelty, policy.RoutesPer15Minutes)
	client := normalized(float64(len(longWindow.clients)), policy.ClientFamiliesPer24Hours)
	device := 0.0
	if deviceLimit > 0 && activeDevices > deviceLimit {
		device = math.Min(1, float64(activeDevices-deviceLimit)/float64(max(1, deviceLimit)))
	}
	risk.Score = int(math.Round(math.Min(100, 25*device+15*pull+10*route+5*client+25*raw)))
	risk.Signals = []string{}
	risk.EvidenceCategories = []string{}
	if device > 0 {
		risk.Signals = append(risk.Signals, fmt.Sprintf("已绑定 %d 台设备，超过设备上限 %d", activeDevices, deviceLimit))
		risk.EvidenceCategories = append(risk.EvidenceCategories, "device_identity")
	}
	if pull > 0 {
		risk.Signals = append(risk.Signals, fmt.Sprintf("10 分钟有效逻辑拉取 %.2f 次，24 小时 %.2f 次", risk.Short.LogicalPullWeight, risk.Long.LogicalPullWeight))
		risk.EvidenceCategories = append(risk.EvidenceCategories, "logical_pull")
	}
	if route > 0 {
		risk.Signals = append(risk.Signals, fmt.Sprintf("15 分钟路由新颖度 %.2f（%d 条归一化路径）", routeWindow.routeNovelty, len(routeWindow.routes)))
		risk.EvidenceCategories = append(risk.EvidenceCategories, "network_route")
	}
	if client > 0 {
		risk.Signals = append(risk.Signals, fmt.Sprintf("24 小时内出现 %d 个客户端族", len(longWindow.clients)))
		risk.EvidenceCategories = append(risk.EvidenceCategories, "client_family")
	}
	if raw > 0 {
		risk.Signals = append(risk.Signals, fmt.Sprintf("60 秒内原始请求 %.0f 次", rawWindow.raw))
		risk.EvidenceCategories = append(risk.EvidenceCategories, "raw_request")
	}
	risk.CounterEvidence = []string{}
	if longWindow.deduped > 0 {
		risk.CounterEvidence = append(risk.CounterEvidence, fmt.Sprintf("已合并 %d 次代理/直连重试或条件请求", longWindow.deduped))
	}
	if risk.Long.RegionCount > 1 {
		risk.CounterEvidence = append(risk.CounterEvidence, "地域变化仅作取证，不参与自动封禁")
	}
	identityQuality := 0.40
	risk.IdentityMode = "legacy_unbound"
	if longWindow.latestDevice != "" {
		identityQuality = 0.85
		risk.IdentityMode = "device_bound"
	}
	geoQuality := 0.40
	if longWindow.pulls > 0 {
		geoQuality = math.Max(0.40, math.Min(1, float64(longWindow.geoKnown)/float64(longWindow.pulls)))
	}
	independence := math.Min(1, float64(len(uniqueStrings(risk.EvidenceCategories)))/2)
	risk.Confidence = math.Round((0.45*identityQuality+0.25+0.20*independence+0.10*geoQuality)*100) / 100
	risk.EvidenceCategories = uniqueStrings(risk.EvidenceCategories)
	risk.Signals = uniqueStrings(risk.Signals)
	risk.HardBlock = risk.IdentityMode == "device_bound" && risk.Score >= 85 && risk.Confidence >= policy.AutoActionConfidence && len(risk.EvidenceCategories) >= 2
	if len(risk.Signals) > 0 {
		risk.Reason = risk.Signals[0]
	}
	switch {
	case risk.Score >= 95:
		risk.RecommendedAction = "reject_device_authentication"
	case risk.Score >= 85:
		risk.RecommendedAction = "suspend_device_subscription"
	case risk.Score >= 70:
		risk.RecommendedAction = "rebind_device_and_limit_raw_requests"
	case risk.Score >= 55:
		risk.RecommendedAction = "notify_operator"
	default:
		risk.RecommendedAction = "observe"
	}
	return risk
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
	case score >= 95:
		return "confirmed"
	case score >= 85:
		return "critical"
	case score >= 70:
		return "high"
	case score >= 55:
		return "alert"
	case score >= 35:
		return "watch"
	default:
		return "normal"
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

func (s *Store) SubscriptionAuditCurrentRisk(ctx context.Context, userID int64, at time.Time, policy model.AuditPolicy) (model.SubscriptionAuditRisk, model.SubscriptionAccessState, error) {
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
	routes  map[string]struct{}
	regions map[string]struct{}
	clients map[string]struct{}
}

func (s *Store) SubscriptionAuditOverview(ctx context.Context, windowHours int, policy model.AuditPolicy) (model.SubscriptionAuditOverview, error) {
	if windowHours < 1 {
		windowHours = 24
	}
	if windowHours > 30*24 {
		windowHours = 30 * 24
	}
	at := time.Now().UTC()
	overview := model.SubscriptionAuditOverview{WindowHours: windowHours, GeneratedAt: at, Policy: policy, Users: []model.SubscriptionAuditUserSummary{}}
	rows, err := s.db.QueryContext(ctx, `select a.user_id,u.username,u.nickname,a.source_ip,a.route_id,a.source_country_code,a.source_country,a.source_province,a.client_name,a.format,a.outcome,a.raw_request_weight,a.logical_pull_weight,a.requested_at
		from subscription_pull_audits a join users u on u.id=a.user_id where a.requested_at>=? order by a.requested_at`, at.Add(-time.Duration(windowHours)*time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		return overview, err
	}
	builders := map[int64]*subscriptionAuditSummaryBuilder{}
	allIPs := map[string]struct{}{}
	for rows.Next() {
		var userID int64
		var username, nickname, sourceIP, routeID, countryCode, country, province, clientName, format, outcome, requestedAt string
		var rawWeight, logicalWeight float64
		if err := rows.Scan(&userID, &username, &nickname, &sourceIP, &routeID, &countryCode, &country, &province, &clientName, &format, &outcome, &rawWeight, &logicalWeight, &requestedAt); err != nil {
			return overview, errors.Join(err, rows.Close())
		}
		builder := builders[userID]
		if builder == nil {
			builder = &subscriptionAuditSummaryBuilder{item: model.SubscriptionAuditUserSummary{UserID: userID, Username: username, Nickname: nickname}, ips: map[string]struct{}{}, routes: map[string]struct{}{}, regions: map[string]struct{}{}, clients: map[string]struct{}{}}
			builders[userID] = builder
		}
		builder.item.PullCount++
		builder.item.RawRequestCount += int64(math.Round(rawWeight))
		builder.item.LogicalPullWeight += logicalWeight
		overview.TotalPulls++
		if strings.HasPrefix(outcome, "served") {
			builder.item.SuccessfulCount++
		} else if strings.HasPrefix(outcome, "denied_") || outcome == "rate_limited" {
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
		if routeID != "" {
			builder.routes[routeID] = struct{}{}
		}
		builder.clients[normalizeAuditClientFamily(clientName)+"|"+normalizeAuditFormatFamily(format)] = struct{}{}
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
			builders[userID] = &subscriptionAuditSummaryBuilder{item: model.SubscriptionAuditUserSummary{UserID: userID, Username: username, Nickname: nickname}, ips: map[string]struct{}{}, routes: map[string]struct{}{}, regions: map[string]struct{}{}, clients: map[string]struct{}{}}
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
		builder.item.RouteCount = len(builder.routes)
		builder.item.RegionCount = len(builder.regions)
		builder.item.ClientFormatCount = len(builder.clients)
		builder.item.DeviceCount, _, err = subscriptionAuditDeviceCounts(ctx, s.db, userID)
		if err != nil {
			return overview, err
		}
		builder.item.RiskLevel = risk.Level
		builder.item.RiskScore = risk.Score
		builder.item.RiskSignals = append([]string(nil), risk.Signals...)
		builder.item.Confidence = risk.Confidence
		builder.item.EvidenceCategories = append([]string(nil), risk.EvidenceCategories...)
		builder.item.CounterEvidence = append([]string(nil), risk.CounterEvidence...)
		builder.item.RecommendedAction = risk.RecommendedAction
		builder.item.IdentityMode = risk.IdentityMode
		builder.item.CurrentRisk = risk
		builder.item.Suspended = state.Suspended
		builder.item.SuspendedAt = state.SuspendedAt
		builder.item.SuspensionReason = state.Reason
		var suspendedDevices int
		if err := s.db.QueryRowContext(ctx, `select count(*) from user_devices where user_id=? and status='active' and subscription_suspended=1`, userID).Scan(&suspendedDevices); err != nil {
			return overview, err
		}
		if suspendedDevices > 0 {
			builder.item.Suspended = true
			if builder.item.SuspensionReason == "" {
				builder.item.SuspensionReason = "部分设备订阅已暂停"
			}
		}
		if builder.item.Suspended {
			overview.SuspendedCount++
		}
		if risk.Score >= 55 {
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

func (s *Store) SubscriptionAuditUserDetail(ctx context.Context, userID int64, windowHours int, policy model.AuditPolicy) (model.SubscriptionAuditUserDetail, error) {
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
	rows, err := s.db.QueryContext(ctx, `select id,user_id,device_id_hash,representation_id,subscription_revision,raw_request_weight,logical_pull_weight,logical_fetch_id,route_id,route_novelty_weight,dedupe_reason,conditional_request,source_ip,source_country_code,source_country,source_province,source_city,source_isp,geo_database_revision,user_agent,client_name,format,requested_format,auto_detected,profile_id,age_encrypted,token_kind,outcome,reason,risk_eligible,requested_at,created_at from subscription_pull_audits where user_id=? and requested_at>=? order by requested_at desc limit ?`, userID, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SubscriptionPullAudit{}
	for rows.Next() {
		var item model.SubscriptionPullAudit
		var profileID sql.NullInt64
		var encrypted, conditional, eligible, autoDetected int
		var requestedAt, createdAt string
		if err := rows.Scan(&item.ID, &item.UserID, &item.DeviceIDHash, &item.RepresentationID, &item.SubscriptionRevision, &item.RawRequestWeight, &item.LogicalPullWeight, &item.LogicalFetchID, &item.RouteID, &item.RouteNoveltyWeight, &item.DedupeReason, &conditional, &item.SourceIP, &item.SourceCountryCode, &item.SourceCountry, &item.SourceProvince, &item.SourceCity, &item.SourceISP, &item.GeoDatabaseRevision, &item.UserAgent, &item.ClientName, &item.Format, &item.RequestedFormat, &autoDetected, &profileID, &encrypted, &item.TokenKind, &item.Outcome, &item.Reason, &eligible, &requestedAt, &createdAt); err != nil {
			return nil, err
		}
		if profileID.Valid {
			item.ProfileID = &profileID.Int64
		}
		item.AgeEncrypted = encrypted != 0
		item.ConditionalRequest = conditional != 0
		item.RiskEligible = eligible != 0
		item.AutoDetected = autoDetected != 0
		item.RequestedAt = parseTime(requestedAt)
		item.CreatedAt = parseTime(createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}
