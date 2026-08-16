package auditreview

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/aiprovider"
	"github.com/OboardProject/oboard/internal/auditcontract"
	"github.com/OboardProject/oboard/internal/auditintel"
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
		SubscriptionPlans: []model.SubscriptionPlan{{ID: 1, Name: "plan-a", Enabled: true}, {ID: 2, Name: "plan-b", Enabled: true}},
		ActivePlanNodes: []model.SubscriptionPlanNode{
			{PlanID: 1, NodeType: model.AssignableNodeInbound, NodeID: 100, Enabled: true},
			{PlanID: 2, NodeType: model.AssignableNodeInbound, NodeID: 200, Enabled: true},
		},
		PlanBindings: []model.UserPlanBinding{
			{UserID: 1, PlanID: 1, Enabled: true},
			{UserID: 2, PlanID: 2, Enabled: true},
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

	review := &model.AuditReview{ID: "review", EvidenceTypes: []string{"connection"}, PrivacyMode: "masked", WindowStartedAt: time.Unix(0, 0).UTC(), WindowEndedAt: time.Unix(3600, 0).UTC()}
	evidence := make([]model.AuditReviewEvidence, 0, 81)
	for index := 0; index < 40; index++ {
		userID := int64(index + 1)
		packPayload, _ := json.Marshal(model.AuditEvidencePack{SchemaVersion: model.AuditEvidenceSchemaVersion, Subject: model.AuditEvidenceSubject{Ref: "user:masked-" + string(rune('a'+index))}, Features: []model.AuditEvidenceFeature{{EvidenceID: "ev-01"}}})
		contextPayload, _ := json.Marshal(map[string]string{"sample": strings.Repeat("x", 1800)})
		evidence = append(evidence, model.AuditReviewEvidence{Ref: "user:masked-" + string(rune('a'+index)), Kind: "pack", UserID: &userID, Payload: packPayload})
		evidence = append(evidence, model.AuditReviewEvidence{Ref: "user:masked-" + string(rune('a'+index)) + ":context", Kind: "context", UserID: &userID, Payload: contextPayload})
	}
	inputs, err := findingInputs(review, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 40 {
		t.Fatalf("expected one finding job per user, got %d", len(inputs))
	}
	for _, input := range inputs {
		if len(input) > maxReviewJobInputBytes {
			t.Fatalf("input exceeds limit: %d", len(input))
		}
		var envelope struct {
			Kind       string          `json:"kind"`
			SubjectRef string          `json:"subject_ref"`
			Pack       json.RawMessage `json:"pack"`
			Context    json.RawMessage `json:"context"`
		}
		if err := json.Unmarshal(input, &envelope); err != nil || envelope.Kind != "finding" || envelope.SubjectRef == "" || len(envelope.Pack) == 0 || len(envelope.Context) == 0 {
			t.Fatalf("finding input envelope invalid: %s", input)
		}
	}

	partialJobs := make([]model.AuditReviewJob, 4)
	for index := range partialJobs {
		output, _ := json.Marshal(map[string]string{"schema_version": model.AuditUserFindingSchemaVersion, "summary": strings.Repeat("y", 22000)})
		partialJobs[index] = model.AuditReviewJob{Output: output}
	}
	engine := auditcontractEngineFixture()
	stages := (&Service{key: []byte("k")}).synthesisInputs(review, partialJobs, engine, "findings")
	if len(stages) < 2 {
		t.Fatalf("expected sharded synthesis inputs, got %d", len(stages))
	}
	for _, input := range stages {
		if len(input) > maxReviewJobInputBytes {
			t.Fatalf("synthesis input exceeds limit: %d", len(input))
		}
		var envelope struct {
			Kind   string         `json:"kind"`
			Engine map[string]any `json:"engine"`
		}
		if err := json.Unmarshal(input, &envelope); err != nil || envelope.Kind != "synthesis" || envelope.Engine["overall_risk"] != float64(78) {
			t.Fatalf("synthesis input envelope invalid: %s", input)
		}
	}
}

func openTestDB(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/reviews.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testService(db *store.Store) *Service {
	return New(db, auditintel.New(db, "anonymization-key"), "mask-key")
}

func testCapabilityFixture() *model.AIProviderCapability {
	return &model.AIProviderCapability{ProviderProfileVersion: model.AuditProviderProfileVersion, Model: "model", TestedAt: time.Now().UTC(), ConnectivityOK: true, AuthenticationOK: true, TextSupported: true, AuditReady: true, StructuredOutput: model.AuditProviderStructuredJSONSchema, OutputMode: model.AuditOutputModeStrictSchema, MaxVerifiedOutputTokens: 4096}
}

func testProviderFixture() *model.AIProvider {
	return &model.AIProvider{ID: "provider", Name: "provider", ProviderKind: "openai", DefaultModel: "model", RoutingStrategy: "ordered_failover", Enabled: true}
}

func createTestProviderEndpoint(t *testing.T, ctx context.Context, db *store.Store, provider *model.AIProvider, capability *model.AIProviderCapability) {
	t.Helper()
	endpoint := &model.AIProviderEndpoint{ID: "endpoint", ProviderID: provider.ID, Name: "Primary", BaseURL: "https://api.example.com/v1", APIStyle: string(aiprovider.APIStyleOpenAIResponses), AuthMode: aiprovider.AuthModeNone, Enabled: true}
	if err := db.CreateAIProviderEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	if capability != nil {
		capability.ProviderID, capability.EndpointID, capability.APIStyle = provider.ID, endpoint.ID, endpoint.APIStyle
		capability.ConfigDigest = aiprovider.ConfigDigest(aiprovider.RuntimeEndpoint{ID: endpoint.ID, BaseURL: endpoint.BaseURL, APIStyle: aiprovider.APIStyle(endpoint.APIStyle), AuthMode: endpoint.AuthMode}, provider.DefaultModel)
		if err := db.UpsertAIProviderEndpointCapability(ctx, capability); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCreateRequiresAuditReadyProvider(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	actor := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, actor); err != nil {
		t.Fatal(err)
	}
	provider := &model.AIProvider{ID: "provider", Name: "provider", BaseURL: "http://127.0.0.1", Model: "model", CredentialEncrypted: "encrypted", Enabled: true}
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	createTestProviderEndpoint(t, ctx, db, provider, nil)
	service := testService(db)
	nowTime := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	request := CreateRequest{RequestID: "request-1", ProviderID: provider.ID, RequestedBy: actor.ID, Scope: model.AuditReviewScope{Users: model.AuditReviewSelector{Mode: "all"}, Servers: model.AuditReviewSelector{Mode: "all"}}, EvidenceTypes: []string{"subscription"}, WindowStart: nowTime.Add(-time.Hour), WindowEnd: nowTime}
	if _, err := service.Create(ctx, request); err == nil || !strings.Contains(err.Error(), "兼容性测试") {
		t.Fatalf("provider without capability was accepted: %v", err)
	}
	capability := testCapabilityFixture()
	capability.AuditReady = false
	createCapability := capability
	createCapability.ProviderID, createCapability.EndpointID, createCapability.APIStyle = provider.ID, "endpoint", string(aiprovider.APIStyleOpenAIResponses)
	createCapability.ConfigDigest = aiprovider.ConfigDigest(aiprovider.RuntimeEndpoint{ID: "endpoint", BaseURL: "https://api.example.com/v1", APIStyle: aiprovider.APIStyleOpenAIResponses, AuthMode: aiprovider.AuthModeNone}, "model")
	if err := db.UpsertAIProviderEndpointCapability(ctx, createCapability); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, request); err == nil || !strings.Contains(err.Error(), "兼容性测试") {
		t.Fatalf("non-ready provider was accepted: %v", err)
	}
	createCapability.AuditReady = true
	createCapability.StructuredOutput = model.AuditProviderStructuredPromptedJSON
	createCapability.OutputMode = model.AuditOutputModeText
	if err := db.UpsertAIProviderEndpointCapability(ctx, createCapability); err != nil {
		t.Fatal(err)
	}
	storedProvider, err := db.GetAIProvider(ctx, provider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !auditCapabilityAllowed(storedProvider) {
		t.Fatal("audit-ready prompted JSON endpoint was rejected")
	}
}

func TestCreateIsIdempotentAndValidatesWindow(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	actor := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, actor); err != nil {
		t.Fatal(err)
	}
	provider := testProviderFixture()
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	createTestProviderEndpoint(t, ctx, db, provider, testCapabilityFixture())
	nowTime := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	service := testService(db)
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
	evidence, _, err := db.ListAuditReviewEvidence(ctx, first.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, item := range evidence {
		kinds[item.Kind] = true
		if item.Kind == "pack" {
			var pack model.AuditEvidencePack
			if err := json.Unmarshal(item.Payload, &pack); err != nil || pack.SchemaVersion != model.AuditEvidenceSchemaVersion {
				t.Fatalf("stored pack invalid: %v", err)
			}
			for _, feature := range pack.Features {
				if !strings.HasPrefix(feature.EvidenceID, item.Ref+"/") {
					t.Fatalf("pack evidence id %q not qualified by %q", feature.EvidenceID, item.Ref)
				}
			}
		}
	}
	for _, kind := range []string{"scope", "pack", "context"} {
		if !kinds[kind] {
			t.Fatalf("missing evidence kind %q: %v", kind, kinds)
		}
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

func packFixture(subject string, overall int, confidence float64, coverage float64) model.AuditEvidencePack {
	threshold := 2.0
	return model.AuditEvidencePack{
		SchemaVersion: model.AuditEvidenceSchemaVersion,
		Mode:          "single_user",
		Subject:       model.AuditEvidenceSubject{Ref: subject, IdentityMode: "device_bound", PolicyProfile: "balanced"},
		Window:        model.AuditEvidenceWindow{Current: "2026-08-03T07:00:00Z/2026-08-03T08:00:00Z", Comparisons: []string{"same_time_slot_28d"}},
		DataQuality:   model.AuditEvidenceQuality{Coverage: coverage, BaselineDays: 24, DroppedBuckets: 1, IdentityQuality: 0.9, DataCompleteness: 0.97},
		Scores:        model.AuditEvidenceScores{ConnectionRisk: overall, SubscriptionRisk: 0, OverallRisk: overall, Health: 100 - overall, EvidenceConfidence: confidence, Caps: model.AuditEvidenceCaps{Anomaly: 0.6, DeviceClone: 1, Normal: 1, HighRisk: 0.7}},
		Features: []model.AuditEvidenceFeature{
			{EvidenceID: subject + "/ev-01", Metric: "concurrent_route_count", Value: 3, Unit: "routes", Window: "90s", Threshold: &threshold, Severity: "high", Source: "connection", Category: "device_clone"},
			{EvidenceID: subject + "/ev-02", Metric: "robust_z", Value: 7.2, Unit: "z", Window: "28d-same-slot", Severity: "medium", Source: "connection", Category: "historical_anomaly"},
		},
		Signals:         []model.AuditEvidenceSignal{{SignalID: subject + "/sig-01", Kind: "device_clone", Severity: "high", DurationSeconds: 146, EvidenceRefs: []string{subject + "/ev-01"}, Confidence: confidence, Text: "同一设备凭证在 3 条独立网络上重叠传输 146 秒"}},
		CounterEvidence: []model.AuditEvidenceCounter{{Ref: subject + "/ce-01", Kind: "engine", Text: "已确认并排除 2 次全节点测速", Scope: "engine:connection"}},
		Methodology:     model.AuditEvidenceMethodology{FeatureVersion: 1, ScoringVersion: model.AuditScoringVersion, BaselineVersion: model.AuditBaselineVersion, EvidenceSchemaVersion: model.AuditEvidenceSchemaVersion, PromptVersion: model.AuditPromptFindingVersion, ReportSchemaVersion: model.AuditReportSchemaVersion, ProviderProfileVersion: model.AuditProviderProfileVersion},
	}
}

func auditcontractEngineFixture() auditcontract.EngineSummary {
	return auditcontract.EngineSummary{
		OverallRisk: 78, Health: 22, Confidence: 0.84, Coverage: 0.94, BaselineDays: 24, DroppedBuckets: 1, IdentityQuality: 0.9,
		FeatureVersion: 1, ScoringVersion: model.AuditScoringVersion, BaselineVersion: model.AuditBaselineVersion,
		EvidenceSchemaVersion: model.AuditEvidenceSchemaVersion, PromptVersion: model.AuditPromptReportVersion,
		ReportSchemaVersion: model.AuditReportSchemaVersion, ProviderProfileVersion: model.AuditProviderProfileVersion,
		StructuredOutput: "json_schema", OutputMode: "strict_schema", Model: "model",
		Subjects: []string{"user:2"},
	}
}

func TestValidateFindingJobEnforcesEvidenceRefs(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	pack := packFixture("user:2", 78, 0.84, 0.94)
	packJSON, _ := json.Marshal(pack)
	jobInput, _ := json.Marshal(map[string]any{"review_id": "review-1", "kind": "finding", "subject_ref": "user:2", "pack": json.RawMessage(packJSON)})
	job := &model.AuditReviewJob{ID: "aij_1", ReviewID: "review-1", ProviderID: "provider", Kind: "finding", Input: jobInput}
	service := testService(db)
	valid := model.AuditUserFinding{
		SchemaVersion: model.AuditUserFindingSchemaVersion, SubjectRef: "user:2",
		BehaviorProfile: model.AuditBehaviorProfile{UsualPattern: []string{"通常 1 条路由"}, CurrentPattern: []string{"出现 3 条路由"}, KeyChanges: []string{"并发路由增加"}},
		Findings: []model.AuditReportFinding{{
			FindingID: "finding-01", Title: "同一凭据出现多路由并发", Severity: "high",
			Observation: "过去 15 分钟出现 3 条独立路由重叠。", Interpretation: "明显偏离历史模式。",
			EvidenceRefs:                []string{"user:2/ev-01", "user:2/ev-02"},
			CounterEvidenceRefs:         []string{"user:2/ce-01"},
			PlausibleBenignExplanations: []string{"多设备切换"}, VerificationSteps: []string{"核对设备绑定记录"},
		}},
	}
	validJSON, _ := json.Marshal(valid)
	if err := service.ValidateReport(ctx, "review-1", job, validJSON); err != nil {
		t.Fatalf("valid finding rejected: %v", err)
	}
	badRef := valid
	badRef.Findings[0].EvidenceRefs = []string{"user:3/ev-01"}
	badJSON, _ := json.Marshal(badRef)
	if err := service.ValidateReport(ctx, "review-1", job, badJSON); err == nil || !strings.Contains(err.Error(), "证据引用") {
		t.Fatalf("cross-subject ref was accepted: %v", err)
	}
	noCategories := valid
	noCategories.Findings[0].EvidenceRefs = []string{"user:2/ev-01"}
	noCategoriesJSON, _ := json.Marshal(noCategories)
	if err := service.ValidateReport(ctx, "review-1", job, noCategoriesJSON); err == nil || !strings.Contains(err.Error(), "独立证据类别") {
		t.Fatalf("single-category high finding without verification flag was accepted: %v", err)
	}
	flagged := valid
	flagged.Findings[0].EvidenceRefs = []string{"user:2/ev-01"}
	flagged.Findings[0].NeedsVerification = true
	flaggedJSON, _ := json.Marshal(flagged)
	if err := service.ValidateReport(ctx, "review-1", job, flaggedJSON); err != nil {
		t.Fatalf("flagged finding rejected: %v", err)
	}
}

func TestValidateReportEnforcesEngineValues(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	actor := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, actor); err != nil {
		t.Fatal(err)
	}
	provider := testProviderFixture()
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	createTestProviderEndpoint(t, ctx, db, provider, testCapabilityFixture())
	review := &model.AuditReview{ID: "review-1", RequestID: "request-1", ProviderID: provider.ID, RequestedBy: actor.ID, Status: "running", PrivacyMode: "raw", EvidenceTypes: []string{"connection"}, WindowStartedAt: time.Date(2026, 8, 3, 7, 0, 0, 0, time.UTC), WindowEndedAt: time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC), SnapshotAt: time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)}
	pack := packFixture("user:2", 78, 0.84, 0.94)
	packJSON, _ := json.Marshal(pack)
	evidence := []model.AuditReviewEvidence{{Ref: "user:2", ReviewID: "review-1", Kind: "pack", Payload: packJSON}}
	jobs := []model.AuditReviewJob{{ID: "aij_job", ReviewID: "review-1", ProviderID: "provider", Stage: 1, Position: 0, Kind: "synthesis", Input: json.RawMessage(`{}`)}}
	if err := db.CreateAuditReview(ctx, review, evidence, jobs); err != nil {
		t.Fatal(err)
	}
	service := testService(db)
	job := &model.AuditReviewJob{ID: "aij_job", ReviewID: "review-1", ProviderID: "provider", Kind: "synthesis"}
	report := model.AuditReviewReport{
		SchemaVersion:   model.AuditReportSchemaVersion,
		Executive:       model.AuditReportExecutive{Verdict: "high_risk", RiskScore: 78, HealthScore: 22, EvidenceConfidence: 0.84, OneLineConclusion: "同一凭据出现多路由并发，建议人工核查。"},
		BehaviorProfile: model.AuditBehaviorProfile{UsualPattern: []string{"通常 1 条路由"}, CurrentPattern: []string{"3 条路由重叠 146 秒"}, KeyChanges: []string{"并发路由显著增加"}},
		Findings: []model.AuditReportFinding{{
			FindingID: "finding-01", Title: "同一凭据出现多路由并发", Severity: "high",
			Observation: "过去 15 分钟出现 3 条独立路由重叠。", Interpretation: "明显偏离历史模式。",
			EvidenceRefs: []string{"user:2/ev-01", "user:2/ev-02"}, CounterEvidenceRefs: []string{"user:2/ce-01"},
		}},
		RecommendedActions: []model.AuditReportAction{{Action: "request_manual_review"}},
		DataQuality:        model.AuditReportDataQuality{Coverage: 0.94, BaselineDays: 24, DroppedBuckets: 1, IdentityQuality: 0.9},
		DataGaps:           []string{"无"},
		Methodology:        model.AuditReportMethodology{FeatureVersion: 1, ScoringVersion: model.AuditScoringVersion, BaselineVersion: model.AuditBaselineVersion, EvidenceSchemaVersion: model.AuditEvidenceSchemaVersion, PromptVersion: model.AuditPromptReportVersion, ReportSchemaVersion: model.AuditReportSchemaVersion, ProviderProfileVersion: model.AuditProviderProfileVersion, StructuredOutput: "json_schema", OutputMode: "strict_schema", Model: "model"},
	}
	validJSON, _ := json.Marshal(report)
	if err := service.ValidateReport(ctx, "review-1", job, validJSON); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	promptedCapability := testCapabilityFixture()
	promptedCapability.StructuredOutput = model.AuditProviderStructuredPromptedJSON
	promptedCapability.OutputMode = model.AuditOutputModeText
	promptedReport := report
	promptedReport.Methodology.StructuredOutput = model.AuditProviderStructuredPromptedJSON
	promptedReport.Methodology.OutputMode = model.AuditOutputModeText
	promptedJSON, _ := json.Marshal(promptedReport)
	if err := service.ValidateReportWithCapability(ctx, "review-1", job, promptedJSON, promptedCapability); err != nil {
		t.Fatalf("prompted JSON report rejected: %v", err)
	}
	tampered := report
	tampered.Executive.RiskScore = 60
	tampered.Executive.HealthScore = 40
	tamperedJSON, _ := json.Marshal(tampered)
	if err := service.ValidateReport(ctx, "review-1", job, tamperedJSON); err == nil || !strings.Contains(err.Error(), "风险评分") {
		t.Fatalf("tampered risk score was accepted: %v", err)
	}
	verdict := report
	verdict.Executive.Verdict = "normal"
	verdictJSON, _ := json.Marshal(verdict)
	if err := service.ValidateReport(ctx, "review-1", job, verdictJSON); err == nil || !strings.Contains(err.Error(), "风险等级") {
		t.Fatalf("inconsistent verdict was accepted: %v", err)
	}
	confidence := report
	confidence.Executive.EvidenceConfidence = 0.5
	confidenceJSON, _ := json.Marshal(confidence)
	if err := service.ValidateReport(ctx, "review-1", job, confidenceJSON); err == nil || !strings.Contains(err.Error(), "置信度") {
		t.Fatalf("tampered confidence was accepted: %v", err)
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
