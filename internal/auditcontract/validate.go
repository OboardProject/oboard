package auditcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// EngineSummary carries the deterministic engine values that a report must
// reproduce exactly. The AI never computes or modifies these fields.
type EngineSummary struct {
	OverallRisk            int                 `json:"overall_risk"`
	Health                 int                 `json:"health"`
	Confidence             float64             `json:"confidence"`
	Coverage               float64             `json:"coverage"`
	BaselineDays           int                 `json:"baseline_days"`
	DroppedBuckets         int64               `json:"dropped_buckets"`
	IdentityQuality        float64             `json:"identity_quality"`
	FeatureVersion         int                 `json:"feature_version"`
	ScoringVersion         string              `json:"scoring_version"`
	BaselineVersion        string              `json:"baseline_version"`
	EvidenceSchemaVersion  string              `json:"evidence_schema_version"`
	PromptVersion          string              `json:"prompt_version"`
	ReportSchemaVersion    string              `json:"report_schema_version"`
	ProviderProfileVersion string              `json:"provider_profile_version"`
	StructuredOutput       string              `json:"structured_output"`
	OutputMode             string              `json:"output_mode"`
	Model                  string              `json:"model"`
	Subjects               []string            `json:"subjects"`
	RefCategories          map[string][]string `json:"ref_categories,omitempty"`
}

func (e EngineSummary) Insufficient() bool {
	return e.Coverage < 0.8 || e.BaselineDays < 3
}

// ExpectedVerdict is the deterministic verdict for a risk score band.
func ExpectedVerdict(engine EngineSummary) string {
	switch {
	case engine.OverallRisk >= 70:
		return "high_risk"
	case engine.OverallRisk >= 35:
		return "attention"
	case engine.Insufficient():
		return "insufficient_evidence"
	default:
		return "normal"
	}
}

type findingJobInput struct {
	SubjectRef string          `json:"subject_ref"`
	Pack       json.RawMessage `json:"pack"`
}

type synthesisJobInput struct {
	Engine EngineSummary `json:"engine"`
}

const (
	maxFindingTitle       = 200
	maxFindingObservation = 500
	maxFindingInterp      = 800
	maxFindingRefs        = 16
	maxExplanation        = 8
	maxProfileItem        = 300
	maxFindingCount       = 32
	maxTimelineCount      = 24
	maxCounterCount       = 16
	maxActionCount        = 8
	maxGapCount           = 16
	maxGapLength          = 300
)

var findingSeverities = map[string]bool{"low": true, "medium": true, "high": true, "critical": true}

// ValidateUserFinding validates a stage-0 finding output against its evidence
// pack and returns the normalized finding. Refs must exist in the pack and
// belong to the finding's subject.
func ValidateUserFinding(input, output json.RawMessage) (*model.AuditUserFinding, error) {
	var job findingJobInput
	if err := json.Unmarshal(input, &job); err != nil || strings.TrimSpace(job.SubjectRef) == "" || len(job.Pack) == 0 {
		return nil, errors.New("AI 审查任务输入无效")
	}
	var pack model.AuditEvidencePack
	if err := json.Unmarshal(job.Pack, &pack); err != nil || pack.SchemaVersion != model.AuditEvidenceSchemaVersion {
		return nil, errors.New("AI 审查证据包无效")
	}
	finding := &model.AuditUserFinding{}
	if err := decodeStrict(output, finding); err != nil {
		return nil, errors.New("AI 审查输出结构无效")
	}
	if finding.SchemaVersion != model.AuditUserFindingSchemaVersion {
		return nil, errors.New("AI 审查输出 Schema 版本无效")
	}
	if finding.SubjectRef != job.SubjectRef {
		return nil, errors.New("AI 审查对象引用无效")
	}
	allowed := packRefs(&pack, job.SubjectRef)
	if err := validateProfile(finding.BehaviorProfile); err != nil {
		return nil, err
	}
	refErr := func(refs []string) error { return validateRefs(refs, allowed, nil) }
	seen := map[string]bool{}
	for _, item := range finding.Findings {
		if err := validateFinding(item, allowed, packRefCategories(&pack), true, seen, refErr); err != nil {
			return nil, err
		}
	}
	if len(finding.CounterEvidence) > 8 || anyTooLong(finding.CounterEvidence, 300) || len(finding.DataGaps) > 8 || anyTooLong(finding.DataGaps, 300) {
		return nil, errors.New("AI 审查反证或数据缺口无效")
	}
	return finding, nil
}

// ValidateReport validates the final stage-1 report against the engine summary
// embedded in the synthesis job input. Engine fields are compared with a small
// tolerance and then overwritten with the authoritative values.
func ValidateReport(input, output json.RawMessage) (*model.AuditReviewReport, error) {
	var job synthesisJobInput
	if err := json.Unmarshal(input, &job); err != nil {
		return nil, errors.New("AI 审查任务输入无效")
	}
	engine := job.Engine
	if engine.OverallRisk < 0 || engine.OverallRisk > 100 || engine.Health != 100-engine.OverallRisk || engine.Confidence < 0 || engine.Confidence > 1 {
		return nil, errors.New("AI 审查引擎摘要无效")
	}
	report := &model.AuditReviewReport{}
	if err := decodeStrict(output, report); err != nil {
		return nil, errors.New("AI 审查输出结构无效")
	}
	if report.SchemaVersion != model.AuditReportSchemaVersion {
		return nil, errors.New("AI 审查输出 Schema 版本无效")
	}
	executive := report.Executive
	if executive.RiskScore != engine.OverallRisk {
		return nil, errors.New("AI 修改了系统风险评分")
	}
	if executive.HealthScore != 100-executive.RiskScore {
		return nil, errors.New("AI 健康评分与风险评分不一致")
	}
	if !closeFloat(executive.EvidenceConfidence, engine.Confidence) {
		return nil, errors.New("AI 修改了系统置信度")
	}
	if expected := ExpectedVerdict(engine); executive.Verdict != expected {
		return nil, fmt.Errorf("AI 判定与风险等级不一致（应为 %s）", expected)
	}
	if runeLen(executive.OneLineConclusion) == 0 || runeLen(executive.OneLineConclusion) > 300 {
		return nil, errors.New("AI 一句话结论无效")
	}
	if !closeFloat(report.DataQuality.Coverage, engine.Coverage) || report.DataQuality.BaselineDays != engine.BaselineDays || report.DataQuality.DroppedBuckets != engine.DroppedBuckets || !closeFloat(report.DataQuality.IdentityQuality, engine.IdentityQuality) {
		return nil, errors.New("AI 修改了系统数据质量字段")
	}
	if err := validateMethodology(report.Methodology, engine); err != nil {
		return nil, err
	}
	if err := validateProfile(report.BehaviorProfile); err != nil {
		return nil, err
	}
	allowed := subjectRefSet(engine.Subjects)
	categories := engine.RefCategories
	refErr := func(refs []string) error { return validateRefsWithPrefixes(refs, allowed, allowed) }
	seen := map[string]bool{}
	for _, item := range report.Findings {
		if err := validateFinding(item, allowed, categories, false, seen, refErr); err != nil {
			return nil, err
		}
	}
	if len(report.Timeline) > maxTimelineCount {
		return nil, errors.New("AI 审查时间线无效")
	}
	for _, item := range report.Timeline {
		if runeLen(item.TimelineID) == 0 || runeLen(item.Title) == 0 || runeLen(item.Title) > 200 || runeLen(item.Detail) > 400 {
			return nil, errors.New("AI 审查时间线条目无效")
		}
		if err := refErr(item.EvidenceRefs); err != nil {
			return nil, err
		}
	}
	if len(report.CounterEvidence) > maxCounterCount {
		return nil, errors.New("AI 审查反证无效")
	}
	for _, item := range report.CounterEvidence {
		if runeLen(item.CounterID) == 0 || runeLen(item.Text) == 0 || runeLen(item.Text) > 400 {
			return nil, errors.New("AI 审查反证条目无效")
		}
		if err := refErr(item.EvidenceRefs); err != nil {
			return nil, err
		}
	}
	if len(report.RecommendedActions) > maxActionCount {
		return nil, errors.New("AI 审查建议无效")
	}
	allowedActions := map[string]bool{"notify_admin": true, "request_manual_review": true, "continue_observation": true, "inspect_user": true, "inspect_server": true, "propose_temporary_subscription_suspension": true}
	for _, action := range report.RecommendedActions {
		if !allowedActions[action.Action] || runeLen(action.Reason) > 300 {
			return nil, errors.New("AI 审查建议无效")
		}
	}
	if len(report.DataGaps) > maxGapCount || anyTooLong(report.DataGaps, maxGapLength) {
		return nil, errors.New("AI 审查数据缺口无效")
	}
	// Overwrite authoritative engine fields so the stored report is always
	// byte-exact with the deterministic engine.
	report.Executive.EvidenceConfidence = engine.Confidence
	report.Executive.HealthScore = 100 - engine.OverallRisk
	report.Executive.RiskScore = engine.OverallRisk
	report.DataQuality = model.AuditReportDataQuality{Coverage: engine.Coverage, BaselineDays: engine.BaselineDays, DroppedBuckets: engine.DroppedBuckets, IdentityQuality: engine.IdentityQuality}
	report.Methodology = engineMethodology(engine)
	return report, nil
}

func engineMethodology(engine EngineSummary) model.AuditReportMethodology {
	return model.AuditReportMethodology{
		FeatureVersion: engine.FeatureVersion, ScoringVersion: engine.ScoringVersion, BaselineVersion: engine.BaselineVersion,
		EvidenceSchemaVersion: engine.EvidenceSchemaVersion, PromptVersion: engine.PromptVersion, ReportSchemaVersion: engine.ReportSchemaVersion,
		ProviderProfileVersion: engine.ProviderProfileVersion, StructuredOutput: engine.StructuredOutput,
		OutputMode: engine.OutputMode, Model: engine.Model,
	}
}

func validateMethodology(value model.AuditReportMethodology, engine EngineSummary) error {
	if value.FeatureVersion != engine.FeatureVersion || value.ScoringVersion != engine.ScoringVersion || value.BaselineVersion != engine.BaselineVersion ||
		value.EvidenceSchemaVersion != engine.EvidenceSchemaVersion || value.PromptVersion != engine.PromptVersion || value.ReportSchemaVersion != engine.ReportSchemaVersion ||
		value.ProviderProfileVersion != engine.ProviderProfileVersion || value.StructuredOutput != engine.StructuredOutput || value.OutputMode != engine.OutputMode || value.Model != engine.Model {
		return errors.New("AI 审查方法学版本无效")
	}
	switch value.OutputMode {
	case model.AuditOutputModeStrictSchema:
		if value.StructuredOutput != model.AuditProviderStructuredJSONSchema {
			return errors.New("AI 审查结构化输出模式无效")
		}
	case model.AuditOutputModeJSONObject:
		if value.StructuredOutput != model.AuditProviderStructuredJSONObject {
			return errors.New("AI 审查结构化输出模式无效")
		}
	case model.AuditOutputModeText:
		if value.StructuredOutput != model.AuditProviderStructuredPromptedJSON {
			return errors.New("AI 审查结构化输出模式无效")
		}
	default:
		return errors.New("AI 审查输出模式无效")
	}
	return nil
}

func validateFinding(item model.AuditReportFinding, allowed map[string]bool, categories map[string][]string, subjectOnly bool, seen map[string]bool, refErr func([]string) error) error {
	if runeLen(item.FindingID) == 0 || seen[item.FindingID] || !findingSeverities[item.Severity] {
		return errors.New("AI 审查 Finding 标识或严重程度无效")
	}
	seen[item.FindingID] = true
	if runeLen(item.Title) == 0 || runeLen(item.Title) > maxFindingTitle || runeLen(item.Observation) == 0 || runeLen(item.Observation) > maxFindingObservation || runeLen(item.Interpretation) == 0 || runeLen(item.Interpretation) > maxFindingInterp {
		return errors.New("AI 审查 Finding 文本无效")
	}
	if len(item.EvidenceRefs) == 0 || len(item.EvidenceRefs) > maxFindingRefs {
		return errors.New("AI 审查 Finding 必须引用至少一个证据")
	}
	if err := refErr(item.EvidenceRefs); err != nil {
		return err
	}
	if err := refErr(item.CounterEvidenceRefs); err != nil {
		return err
	}
	if len(item.PlausibleBenignExplanations) > maxExplanation || anyTooLong(item.PlausibleBenignExplanations, 200) || len(item.VerificationSteps) > maxExplanation || anyTooLong(item.VerificationSteps, 200) {
		return errors.New("AI 审查 Finding 解释或核查项无效")
	}
	if (item.Severity == "high" || item.Severity == "critical") && !item.NeedsVerification {
		unique := map[string]bool{}
		for _, ref := range item.EvidenceRefs {
			for _, category := range categories[ref] {
				unique[category] = true
			}
		}
		if len(categories) > 0 && len(unique) < 2 {
			return errors.New("高风险 Finding 必须引用两个独立证据类别或标记需要进一步验证")
		}
	}
	return nil
}

func validateRefs(refs []string, allowed map[string]bool, categories map[string][]string) error {
	return validateRefsWithPrefixes(refs, allowed, nil)
}

func validateRefsWithPrefixes(refs []string, allowed map[string]bool, prefixes map[string]bool) error {
	for _, ref := range refs {
		if runeLen(ref) > 256 || !allowed[ref] {
			if runeLen(ref) > 256 {
				return errors.New("AI 返回了不存在的证据引用")
			}
			matched := false
			for prefix := range prefixes {
				if strings.HasPrefix(ref, prefix) {
					matched = true
					break
				}
			}
			if !matched {
				return errors.New("AI 返回了不存在的证据引用")
			}
		}
	}
	return nil
}

func validateProfile(profile model.AuditBehaviorProfile) error {
	if len(profile.UsualPattern) > 8 || anyTooLong(profile.UsualPattern, maxProfileItem) || len(profile.CurrentPattern) > 8 || anyTooLong(profile.CurrentPattern, maxProfileItem) || len(profile.KeyChanges) > 8 || anyTooLong(profile.KeyChanges, maxProfileItem) {
		return errors.New("AI 审查行为画像无效")
	}
	return nil
}

// packRefs returns the set of refs valid for a single-subject pack, including
// the context evidence ref.
func packRefs(pack *model.AuditEvidencePack, subjectRef string) map[string]bool {
	out := map[string]bool{subjectRef + ":context": true}
	prefix := subjectRef + "/"
	for _, feature := range pack.Features {
		out[feature.EvidenceID] = true
	}
	for _, signal := range pack.Signals {
		out[signal.SignalID] = true
	}
	for _, counter := range pack.CounterEvidence {
		out[counter.Ref] = true
	}
	for _, item := range pack.Timeline {
		out[item.EvidenceID] = true
	}
	if len(out) > 512 {
		for ref := range out {
			if !strings.HasPrefix(ref, prefix) {
				delete(out, ref)
			}
		}
	}
	return out
}

// packRefCategories maps each evidence ref to its evidence categories so high
// severity findings can be checked for independent evidence categories.
func packRefCategories(pack *model.AuditEvidencePack) map[string][]string {
	out := map[string][]string{}
	for _, feature := range pack.Features {
		if feature.Category != "" {
			out[feature.EvidenceID] = []string{feature.Category}
		}
	}
	for _, signal := range pack.Signals {
		if signal.Kind != "" {
			out[signal.SignalID] = append(out[signal.SignalID], signal.Kind)
		}
	}
	return out
}

func subjectRefSet(subjects []string) map[string]bool {
	out := map[string]bool{}
	for _, subject := range subjects {
		if subject != "" {
			out[subject+"/"] = true
		}
	}
	return out
}

// ValidateSubjectRefs checks refs against a subject-prefix allowlist. Context
// refs use the ":context" suffix form.
func ValidateSubjectRefs(refs []string, subjects []string) error {
	prefixes := subjectRefSet(subjects)
	contexts := map[string]bool{}
	for _, subject := range subjects {
		contexts[subject+":context"] = true
	}
	for _, ref := range refs {
		if runeLen(ref) > 256 {
			return errors.New("AI 返回了不存在的证据引用")
		}
		valid := false
		for prefix := range prefixes {
			if strings.HasPrefix(ref, prefix) {
				valid = true
				break
			}
		}
		if !valid && !contexts[ref] {
			return errors.New("AI 返回了不存在的证据引用")
		}
	}
	return nil
}

func decodeStrict(raw json.RawMessage, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	return nil
}

func closeFloat(left, right float64) bool {
	diff := left - right
	if diff < 0 {
		diff = -diff
	}
	return diff <= 0.005
}

func runeLen(value string) int {
	return len([]rune(value))
}

func anyTooLong(values []string, limit int) bool {
	for _, value := range values {
		if runeLen(value) > limit {
			return true
		}
	}
	return false
}
