package auditcontract

import "github.com/OboardProject/oboard/internal/model"

func stringItem() map[string]any { return map[string]any{"type": "string"} }

func textList(maxItems, maxLength int) map[string]any {
	return map[string]any{"type": "array", "maxItems": maxItems, "items": map[string]any{"type": "string", "maxLength": maxLength}}
}

func refsList(maxItems int) map[string]any {
	return map[string]any{"type": "array", "maxItems": maxItems, "items": stringItem()}
}

func baselineSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{}, "properties": map[string]any{
		"current":          map[string]any{"type": "number"},
		"baseline_p95":     map[string]any{"type": "number"},
		"threshold":        map[string]any{"type": "number"},
		"duration_seconds": map[string]any{"type": "integer", "minimum": 0},
	}}
}

func findingSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"finding_id", "title", "severity", "observation", "baseline_comparison", "interpretation", "evidence_refs", "counter_evidence_refs", "plausible_benign_explanations", "verification_steps"}, "properties": map[string]any{
		"finding_id":                    stringItem(),
		"title":                         map[string]any{"type": "string", "maxLength": 200},
		"severity":                      map[string]any{"type": "string", "enum": []string{"low", "medium", "high", "critical"}},
		"observation":                   map[string]any{"type": "string", "maxLength": 500},
		"baseline_comparison":           baselineSchema(),
		"interpretation":                map[string]any{"type": "string", "maxLength": 800},
		"evidence_refs":                 refsList(16),
		"counter_evidence_refs":         refsList(16),
		"plausible_benign_explanations": textList(8, 200),
		"verification_steps":            textList(8, 200),
		"needs_verification":            map[string]any{"type": "boolean"},
	}}
}

func behaviorSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"usual_pattern", "current_pattern", "key_changes"}, "properties": map[string]any{
		"usual_pattern":   textList(8, 300),
		"current_pattern": textList(8, 300),
		"key_changes":     textList(8, 300),
	}}
}

// UserFindingSchema is the strict JSON schema sent to providers for stage-0
// finding extraction jobs.
func UserFindingSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"schema_version", "subject_ref", "behavior_profile", "findings", "counter_evidence", "data_gaps"}, "properties": map[string]any{
		"schema_version":   map[string]any{"type": "string", "enum": []string{model.AuditUserFindingSchemaVersion}},
		"subject_ref":      stringItem(),
		"behavior_profile": behaviorSchema(),
		"findings":         map[string]any{"type": "array", "maxItems": 16, "items": findingSchema()},
		"counter_evidence": textList(8, 300),
		"data_gaps":        textList(8, 300),
	}}
}

func executiveSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"verdict", "risk_score", "health_score", "evidence_confidence", "one_line_conclusion"}, "properties": map[string]any{
		"verdict":             map[string]any{"type": "string", "enum": []string{"normal", "attention", "high_risk", "insufficient_evidence"}},
		"risk_score":          map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		"health_score":        map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
		"evidence_confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		"one_line_conclusion": map[string]any{"type": "string", "maxLength": 300},
	}}
}

func dataQualitySchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"coverage", "baseline_days", "dropped_buckets", "identity_quality"}, "properties": map[string]any{
		"coverage":         map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		"baseline_days":    map[string]any{"type": "integer", "minimum": 0},
		"dropped_buckets":  map[string]any{"type": "integer", "minimum": 0},
		"identity_quality": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
	}}
}

func methodologySchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"feature_version", "scoring_version", "baseline_version", "evidence_schema_version", "prompt_version", "report_schema_version", "provider_profile_version", "structured_output", "output_mode", "model"}, "properties": map[string]any{
		"feature_version":          map[string]any{"type": "integer", "minimum": 0},
		"scoring_version":          stringItem(),
		"baseline_version":         stringItem(),
		"evidence_schema_version":  stringItem(),
		"prompt_version":           stringItem(),
		"report_schema_version":    stringItem(),
		"provider_profile_version": stringItem(),
		"structured_output":        map[string]any{"type": "string", "enum": []string{"json_schema", "json_object", "prompted_json"}},
		"output_mode":              map[string]any{"type": "string", "enum": []string{"strict_schema", "json_object", "text"}},
		"model":                    map[string]any{"type": "string", "maxLength": 512},
	}}
}

func timelineItemSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"timeline_id", "kind", "title"}, "properties": map[string]any{
		"timeline_id":   stringItem(),
		"kind":          stringItem(),
		"title":         map[string]any{"type": "string", "maxLength": 200},
		"detail":        map[string]any{"type": "string", "maxLength": 400},
		"started_at":    stringItem(),
		"ended_at":      stringItem(),
		"evidence_refs": refsList(16),
	}}
}

func counterSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"counter_id", "text"}, "properties": map[string]any{
		"counter_id":    stringItem(),
		"text":          map[string]any{"type": "string", "maxLength": 400},
		"evidence_refs": refsList(16),
	}}
}

func actionSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action"}, "properties": map[string]any{
		"action": map[string]any{"type": "string", "enum": []string{"notify_admin", "request_manual_review", "continue_observation", "inspect_user", "inspect_server", "propose_temporary_subscription_suspension"}},
		"reason": map[string]any{"type": "string", "maxLength": 300},
	}}
}

// ReportSchema is the strict JSON schema for the final stage-1 report.
func ReportSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"schema_version", "executive", "behavior_profile", "findings", "timeline", "counter_evidence", "recommended_actions", "data_quality", "data_gaps", "methodology"}, "properties": map[string]any{
		"schema_version":      map[string]any{"type": "string", "enum": []string{model.AuditReportSchemaVersion}},
		"executive":           executiveSchema(),
		"behavior_profile":    behaviorSchema(),
		"findings":            map[string]any{"type": "array", "maxItems": 32, "items": findingSchema()},
		"timeline":            map[string]any{"type": "array", "maxItems": 24, "items": timelineItemSchema()},
		"counter_evidence":    map[string]any{"type": "array", "maxItems": 16, "items": counterSchema()},
		"recommended_actions": map[string]any{"type": "array", "maxItems": 8, "items": actionSchema()},
		"data_quality":        dataQualitySchema(),
		"data_gaps":           textList(16, 300),
		"methodology":         methodologySchema(),
	}}
}
