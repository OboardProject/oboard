package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) subscriptionAuditPolicy(ctx context.Context) model.SubscriptionAuditPolicy {
	policy := store.DefaultSubscriptionAuditPolicy()
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return policy
	}
	raw := strings.TrimSpace(settings[settingSubscriptionAuditPolicy])
	if raw == "" {
		return policy
	}
	var saved model.SubscriptionAuditPolicy
	if json.Unmarshal([]byte(raw), &saved) == nil && store.ValidateSubscriptionAuditPolicy(saved) == nil {
		return saved
	}
	return policy
}

func (s *Server) newSubscriptionPullAudit(r *http.Request, userID int64, format string, profileID *int64, ageEncrypted bool) model.SubscriptionPullAudit {
	item := model.SubscriptionPullAudit{
		UserID: userID, SourceIP: clientIP(r), UserAgent: sanitizeSubscriptionUserAgent(r.UserAgent()),
		ClientName: subscriptionClientName(r.UserAgent()), Format: strings.TrimSpace(format), ProfileID: profileID,
		AgeEncrypted: ageEncrypted, RequestedAt: time.Now().UTC(),
	}
	if s.geoIP != nil && connectionAuditPublicIP(item.SourceIP) {
		if geo, err := s.geoIP.Lookup(item.SourceIP); err == nil {
			item.SourceCountryCode = geo.CountryCode
			item.SourceCountry = geo.Country
			item.SourceProvince = geo.Province
			item.SourceCity = geo.City
			item.SourceISP = geo.ISP
			item.GeoDatabaseRevision = geo.Revision
		}
	}
	return item
}

func connectionAuditPublicIP(raw string) bool {
	ip, err := netip.ParseAddr(normalizedIP(raw))
	return err == nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

func sanitizeSubscriptionUserAgent(raw string) string {
	raw = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw))
	runes := []rune(raw)
	if len(runes) > 512 {
		raw = string(runes[:512])
	}
	return raw
}

func subscriptionClientName(raw string) string {
	lower := strings.ToLower(sanitizeSubscriptionUserAgent(raw))
	for _, candidate := range []struct {
		contains string
		name     string
	}{
		{"mihomo", "Mihomo"}, {"clash.meta", "Clash Meta"}, {"clash", "Clash"},
		{"sing-box", "sing-box"}, {"singbox", "sing-box"}, {"v2rayn", "v2rayN"},
		{"shadowrocket", "Shadowrocket"}, {"quantumult", "Quantumult X"}, {"surge", "Surge"},
		{"loon", "Loon"}, {"stash", "Stash"}, {"egern", "Egern"}, {"surfboard", "Surfboard"},
	} {
		if strings.Contains(lower, candidate.contains) {
			return candidate.name
		}
	}
	if lower == "" {
		return "未知客户端"
	}
	product := strings.Fields(lower)[0]
	if index := strings.IndexByte(product, '/'); index > 0 {
		product = product[:index]
	}
	productRunes := []rune(product)
	if len(productRunes) > 48 {
		product = string(productRunes[:48])
	}
	if product == "" {
		return "其他客户端"
	}
	return product
}

func (s *Server) recordRejectedSubscriptionPull(r *http.Request, userID int64, format string, profileID *int64, ageEncrypted bool, reason string) {
	event := s.newSubscriptionPullAudit(r, userID, format, profileID, ageEncrypted)
	event.Outcome = "rejected_invalid_request"
	event.Reason = boundedSubscriptionAuditReason(reason)
	token := strings.TrimPrefix(r.URL.Path, "/api/v1/subscriptions/")
	if s.store.AddRejectedSubscriptionPullAudit(r.Context(), token, event) == nil {
		s.publishRealtime("audit", "subscriptions")
	}
}

func boundedSubscriptionAuditReason(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 240 {
		value = string(runes[:240])
	}
	return value
}

func (s *Server) subscriptionAuditOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	overview, err := s.subscriptionAuditOverviewData(r.Context(), intQuery(r, "window_hours", 24))
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"subscription_audit": overview})
}

func (s *Server) subscriptionAuditOverviewData(ctx context.Context, windowHours int) (model.SubscriptionAuditOverview, error) {
	overview, err := s.store.SubscriptionAuditOverview(ctx, windowHours, s.subscriptionAuditPolicy(ctx))
	if err == nil {
		overview.GeoDatabase = s.geoIPStatus
	}
	return overview, err
}

func (s *Server) subscriptionAuditUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/audit/subscriptions/users/")
	userID, err := strconv.ParseInt(strings.Trim(path, "/"), 10, 64)
	if err != nil || userID <= 0 {
		fail(w, errors.New("invalid subscription audit user id"), http.StatusBadRequest)
		return
	}
	detail, err := s.store.SubscriptionAuditUserDetail(r.Context(), userID, intQuery(r, "window_hours", 24), s.subscriptionAuditPolicy(r.Context()))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		fail(w, err, status)
		return
	}
	write(w, http.StatusOK, map[string]any{"subscription_audit_user": detail})
}

func (s *Server) combinedAuditOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	connectionOverview, subscriptionOverview, combinedOverview, err := s.auditOverviewData(r.Context(), intQuery(r, "window_hours", 24))
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{
		"connection_audit":   connectionOverview,
		"subscription_audit": subscriptionOverview,
		"audit_risk":         combinedOverview,
	})
}

func (s *Server) combinedAuditOverviewData(ctx context.Context, windowHours int) (model.CombinedAuditOverview, error) {
	_, _, overview, err := s.auditOverviewData(ctx, windowHours)
	return overview, err
}

func (s *Server) auditOverviewData(ctx context.Context, windowHours int) (model.ConnectionAuditOverview, model.SubscriptionAuditOverview, model.CombinedAuditOverview, error) {
	connectionOverview, err := s.store.ConnectionAuditOverview(ctx, windowHours)
	if err != nil {
		return model.ConnectionAuditOverview{}, model.SubscriptionAuditOverview{}, model.CombinedAuditOverview{}, err
	}
	connectionOverview.GeoDatabase = s.geoIPStatus
	subscriptionOverview, err := s.subscriptionAuditOverviewData(ctx, windowHours)
	if err != nil {
		return model.ConnectionAuditOverview{}, model.SubscriptionAuditOverview{}, model.CombinedAuditOverview{}, err
	}
	return connectionOverview, subscriptionOverview, combineAuditOverviews(connectionOverview, subscriptionOverview), nil
}

func combineAuditOverviews(connectionOverview model.ConnectionAuditOverview, subscriptionOverview model.SubscriptionAuditOverview) model.CombinedAuditOverview {
	overview := model.CombinedAuditOverview{WindowHours: connectionOverview.WindowHours, GeneratedAt: time.Now().UTC(), Users: []model.CombinedAuditUserSummary{}, SuspendedCount: subscriptionOverview.SuspendedCount}
	users := map[int64]*model.CombinedAuditUserSummary{}
	for _, item := range connectionOverview.Users {
		users[item.UserID] = &model.CombinedAuditUserSummary{
			UserID: item.UserID, Username: item.Username, Nickname: item.Nickname,
			ConnectionRiskLevel: item.RiskLevel, ConnectionRiskScore: item.RiskScore,
			SubscriptionRiskLevel: "low",
			ConnectionObserved:    true,
			LastSeenAt:            item.LastSeenAt,
		}
	}
	for _, item := range subscriptionOverview.Users {
		user := users[item.UserID]
		if user == nil {
			user = &model.CombinedAuditUserSummary{UserID: item.UserID, Username: item.Username, Nickname: item.Nickname, ConnectionRiskLevel: "low"}
			users[item.UserID] = user
		}
		user.SubscriptionRiskLevel = item.RiskLevel
		user.SubscriptionRiskScore = item.RiskScore
		user.SubscriptionObserved = true
		user.SubscriptionSuspended = item.Suspended
		if item.LastSeenAt.After(user.LastSeenAt) {
			user.LastSeenAt = item.LastSeenAt
		}
	}
	connectionByID := map[int64]model.ConnectionAuditUserSummary{}
	for _, item := range connectionOverview.Users {
		connectionByID[item.UserID] = item
	}
	subscriptionByID := map[int64]model.SubscriptionAuditUserSummary{}
	for _, item := range subscriptionOverview.Users {
		subscriptionByID[item.UserID] = item
	}
	for userID, user := range users {
		connection := connectionByID[userID]
		subscription := subscriptionByID[userID]
		user.RiskScore = max(connection.RiskScore, subscription.RiskScore)
		if connection.RiskScore >= 25 && subscription.RiskScore >= 25 {
			user.RiskScore = min(100, user.RiskScore+15)
		}
		user.RiskLevel = combinedAuditRiskLevel(user.RiskScore)
		for _, signal := range connection.RiskSignals {
			user.RiskSignals = append(user.RiskSignals, "连接："+signal)
		}
		for _, signal := range subscription.RiskSignals {
			user.RiskSignals = append(user.RiskSignals, "订阅："+signal)
		}
		if user.RiskLevel == "high" || user.RiskLevel == "critical" {
			overview.ElevatedRiskCount++
		}
		overview.Users = append(overview.Users, *user)
	}
	sort.SliceStable(overview.Users, func(i, j int) bool {
		if overview.Users[i].RiskScore != overview.Users[j].RiskScore {
			return overview.Users[i].RiskScore > overview.Users[j].RiskScore
		}
		return overview.Users[i].LastSeenAt.After(overview.Users[j].LastSeenAt)
	})
	return overview
}

func combinedAuditRiskLevel(score int) string {
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

func (s *Server) resumeUserSubscriptionAccess(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	claims, ok := r.Context().Value(claimsKey).(security.TokenClaims)
	if !ok || claims.Subject <= 0 || userID <= 0 {
		fail(w, errors.New("invalid administrator session"), http.StatusUnauthorized)
		return
	}
	state, err := s.store.ResumeSubscriptionAccess(r.Context(), userID, claims.Subject)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		fail(w, err, status)
		return
	}
	auditReq(s, r, "resume", "subscription-access", strconv.FormatInt(userID, 10))
	user, _ := s.store.GetUser(r.Context(), userID)
	write(w, http.StatusOK, map[string]any{"subscription_access": state, "user": user})
}
