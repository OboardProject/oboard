package auditreview

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestResolveScopeCombinations(t *testing.T) {
	routing := store.FullRoutingConfig{
		Users:   []model.User{{ID: 1, Status: "active"}, {ID: 2, Status: "active"}, {ID: 3, Status: "active"}},
		Servers: []model.Server{{ID: 10}, {ID: 20}, {ID: 30}},
		Inbounds: []model.Inbound{
			{ID: 100, ServerID: 10, Enabled: true},
			{ID: 200, ServerID: 20, Enabled: true},
		},
		InboundUsers: []model.InboundUser{
			{InboundID: 100, UserID: 1, Enabled: true},
			{InboundID: 200, UserID: 2, Enabled: true},
		},
	}
	historical := map[int64]map[int64]bool{3: {10: true}}
	tests := []struct {
		name    string
		scope   model.AuditReviewScope
		users   []int64
		servers []int64
	}{
		{"selected intersection", model.AuditReviewScope{Users: model.AuditReviewSelector{Mode: "selected", IDs: []int64{1, 2}}, Servers: model.AuditReviewSelector{Mode: "selected", IDs: []int64{20}}}, []int64{2}, []int64{20}},
		{"all users selected servers", model.AuditReviewScope{Users: model.AuditReviewSelector{Mode: "all"}, Servers: model.AuditReviewSelector{Mode: "selected", IDs: []int64{10}}}, []int64{1, 3}, []int64{10}},
		{"selected users all servers", model.AuditReviewScope{Users: model.AuditReviewSelector{Mode: "selected", IDs: []int64{1}}, Servers: model.AuditReviewSelector{Mode: "all"}}, []int64{1}, []int64{10}},
		{"all inventory", model.AuditReviewScope{Users: model.AuditReviewSelector{Mode: "all"}, Servers: model.AuditReviewSelector{Mode: "all"}}, []int64{1, 2, 3}, []int64{10, 20, 30}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users, servers, _, err := resolveScope(test.scope, routing, historical)
			if err != nil {
				t.Fatal(err)
			}
			if !equalIDs(users, test.users) || !equalIDs(servers, test.servers) {
				t.Fatalf("resolved users=%v servers=%v", users, servers)
			}
		})
	}
	_, _, _, err := resolveScope(model.AuditReviewScope{Users: model.AuditReviewSelector{Mode: "selected", IDs: []int64{99}}, Servers: model.AuditReviewSelector{Mode: "all"}}, routing, historical)
	if err == nil {
		t.Fatal("unknown selected user was accepted")
	}
}

func TestAuditReviewMaskingAndPacking(t *testing.T) {
	if got := maskIP("203.0.113.44"); got != "203.0.113.0/24" {
		t.Fatalf("masked IPv4 = %q", got)
	}
	if got := maskIP("2001:db8:1234:5678::1"); got != "2001:db8:1234::/48" {
		t.Fatalf("masked IPv6 = %q", got)
	}
	if got := reducedDestination("api.service.example.com"); got != "example.com" {
		t.Fatalf("reduced destination = %q", got)
	}
	service := &Service{key: []byte("test-key")}
	if raw, masked := service.subjectRef("user", 42, true), service.subjectRef("user", 42, false); raw != "user:42" || masked == raw || !strings.HasPrefix(masked, "user:") {
		t.Fatalf("subject refs raw=%q masked=%q", raw, masked)
	}
	user := model.User{ID: 42, Username: "secret-user", Nickname: "Secret", Status: "active", Role: model.RoleViewer}
	data := model.AuditReviewUserData{
		UserID:              user.ID,
		RecentSubscriptions: []model.SubscriptionPullAudit{{SourceIP: "203.0.113.44", UserAgent: "secret-agent", ClientName: "client", Format: "sing-box"}},
		RecentConnections:   []model.ConnectionAuditReport{{ServerID: 7, SourceIP: "203.0.113.44", Destination: "api.service.example.com", DestinationPort: 443}},
		Destinations:        []model.AuditReviewDestination{{Destination: "api.service.example.com", Port: 443}},
	}
	maskedJSON, _ := json.Marshal(service.userEvidencePayload(service.subjectRef("user", user.ID, false), user, data, false))
	for _, secret := range []string{"secret-user", "secret-agent", "203.0.113.44", "api.service.example.com", "secret-server"} {
		if strings.Contains(string(maskedJSON), secret) {
			t.Fatalf("masked evidence leaked %q: %s", secret, maskedJSON)
		}
	}
	for _, expected := range []string{"203.0.113.0/24", "example.com"} {
		if !strings.Contains(string(maskedJSON), expected) {
			t.Fatalf("masked evidence omitted %q: %s", expected, maskedJSON)
		}
	}
	rawJSON, _ := json.Marshal(service.userEvidencePayload(service.subjectRef("user", user.ID, true), user, data, true))
	for _, expected := range []string{"secret-user", "secret-agent", "203.0.113.44", "api.service.example.com"} {
		if !strings.Contains(string(rawJSON), expected) {
			t.Fatalf("raw evidence omitted %q: %s", expected, rawJSON)
		}
	}

	review := &model.AuditReview{ID: "review", EvidenceTypes: []string{"connection"}}
	evidence := make([]model.AuditReviewEvidence, 80)
	for index := range evidence {
		payload, _ := json.Marshal(map[string]string{"sample": strings.Repeat("x", 1800)})
		evidence[index] = model.AuditReviewEvidence{Ref: "evidence:" + string(rune(index+100)), Kind: "user", Payload: payload}
	}
	inputs, err := reviewInputs(review, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) < 2 {
		t.Fatalf("expected sharded inputs, got %d", len(inputs))
	}
	for _, input := range inputs {
		if len(input) > maxReviewJobInputBytes {
			t.Fatalf("input exceeds limit: %d", len(input))
		}
	}
	partialJobs := make([]model.AuditReviewJob, 4)
	for index := range partialJobs {
		output, _ := json.Marshal(map[string]string{"summary": strings.Repeat("y", 22000)})
		partialJobs[index] = model.AuditReviewJob{Output: output}
	}
	stages := packSynthesisInputs(review, partialJobs)
	if len(stages) < 2 {
		t.Fatalf("expected sharded synthesis inputs, got %d", len(stages))
	}
	for _, input := range stages {
		if len(input) > maxReviewJobInputBytes {
			t.Fatalf("synthesis input exceeds limit: %d", len(input))
		}
	}
}

func TestCreateIsIdempotentAndValidatesTime(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/reviews.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	actor := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, actor); err != nil {
		t.Fatal(err)
	}
	provider := &model.AIProvider{ID: "provider", Name: "provider", BaseURL: "http://127.0.0.1", Model: "model", CredentialEncrypted: "encrypted", Enabled: true}
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	nowTime := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	service := New(db, "mask-key")
	service.now = func() time.Time { return nowTime }
	request := CreateRequest{RequestID: "request-1", ProviderID: provider.ID, RequestedBy: actor.ID, Scope: model.AuditReviewScope{Users: model.AuditReviewSelector{Mode: "all"}, Servers: model.AuditReviewSelector{Mode: "all"}}, EvidenceTypes: []string{"subscription"}, WindowStart: nowTime.Add(-time.Hour), WindowEnd: nowTime}
	first, err := service.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(ctx, request)
	if err != nil || first.ID != second.ID {
		t.Fatalf("idempotent create first=%q second=%q err=%v", first.ID, second.ID, err)
	}
	request.RequestID = "request-2"
	request.WindowStart = nowTime.Add(-31 * 24 * time.Hour)
	if _, err := service.Create(ctx, request); err == nil {
		t.Fatal("out-of-retention time range was accepted")
	}
	request.RequestID = "request-3"
	request.WindowStart, request.WindowEnd = nowTime, nowTime
	if _, err := service.Create(ctx, request); err == nil {
		t.Fatal("empty time range was accepted")
	}
	if _, err := db.GetAuditReviewByRequestID(ctx, "missing"); err != sql.ErrNoRows {
		t.Fatalf("missing request error = %v", err)
	}
}

func TestValidateReportRequiresUserSubjects(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/reviews.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	nowTime := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	actor := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, actor); err != nil {
		t.Fatal(err)
	}
	provider := &model.AIProvider{ID: "provider", Name: "provider", BaseURL: "http://127.0.0.1", Model: "model", CredentialEncrypted: "encrypted", Enabled: true}
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	review := &model.AuditReview{
		ID: "review-1", RequestID: "request-1", ProviderID: provider.ID, RequestedBy: actor.ID, Status: "running",
		PrivacyMode: "raw", EvidenceTypes: []string{"subscription"},
		WindowStartedAt: nowTime.Add(-time.Hour), WindowEndedAt: nowTime, SnapshotAt: nowTime,
	}
	evidence := []model.AuditReviewEvidence{
		{Ref: "user:2", ReviewID: "review-1", Kind: "user", Payload: json.RawMessage(`{}`)},
		{Ref: "server:1", ReviewID: "review-1", Kind: "server", Payload: json.RawMessage(`{}`)},
	}
	jobs := []model.AuditReviewJob{{ID: "aij_job", ReviewID: "review-1", ProviderID: "provider", Stage: 0, Position: 0, Kind: "evidence", Input: json.RawMessage(`{}`)}}
	if err := db.CreateAuditReview(ctx, review, evidence, jobs); err != nil {
		t.Fatal(err)
	}
	service := New(db, "mask-key")
	healthScore := 92
	base := model.AuditReviewReport{Verdict: "normal", RiskLevel: "low", HealthScore: &healthScore, Confidence: 0.9, Summary: "正常", CoverageSummary: "覆盖 1 个用户", RecommendedActions: []string{"continue_observation"}}
	userOnly := base
	userOnly.NotableSubjects = []model.AuditReviewSubjectFinding{{SubjectRef: "user:2", RiskLevel: "low", Summary: "用户行为正常", EvidenceRefs: []string{"user:2"}}}
	if err := service.ValidateReport(ctx, "review-1", &userOnly); err != nil {
		t.Fatalf("user subject was rejected: %v", err)
	}
	withServer := base
	withServer.NotableSubjects = []model.AuditReviewSubjectFinding{{SubjectRef: "server:1", RiskLevel: "low", Summary: "服务器在线", EvidenceRefs: []string{"server:1"}}}
	if err := service.ValidateReport(ctx, "review-1", &withServer); err == nil {
		t.Fatal("server subject was accepted")
	}
	invalidScore := base
	outOfRangeScore := 101
	invalidScore.HealthScore = &outOfRangeScore
	if err := service.ValidateReport(ctx, "review-1", &invalidScore); err == nil {
		t.Fatal("out-of-range health score was accepted")
	}
	inconsistentScore := 59
	invalidScore.HealthScore = &inconsistentScore
	if err := service.ValidateReport(ctx, "review-1", &invalidScore); err == nil {
		t.Fatal("health score inconsistent with risk level was accepted")
	}
	invalidScore.HealthScore = nil
	if err := service.ValidateReport(ctx, "review-1", &invalidScore); err == nil {
		t.Fatal("missing health score was accepted")
	}
}

func equalIDs(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
