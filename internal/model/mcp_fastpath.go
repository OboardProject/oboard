package model

import (
	"encoding/json"
	"time"
)

type MCPPreparedPlan struct {
	ID                string          `json:"id"`
	PrincipalID       string          `json:"principal_id"`
	GrantID           string          `json:"grant_id"`
	RecipeID          string          `json:"recipe_id"`
	RecipeVersion     string          `json:"recipe_version"`
	Operations        json.RawMessage `json:"-"`
	ExpectedRevisions json.RawMessage `json:"-"`
	PlanHash          string          `json:"plan_hash"`
	RiskClass         int             `json:"risk_class"`
	ApprovalMode      string          `json:"approval_mode"`
	Summary           json.RawMessage `json:"summary"`
	Verification      json.RawMessage `json:"verification"`
	CommitKey         string          `json:"-"`
	ClaimedAt         *time.Time      `json:"-"`
	ConsumedAt        *time.Time      `json:"consumed_at,omitempty"`
	ChangesetID       string          `json:"changeset_id,omitempty"`
	WorkflowID        string          `json:"workflow_id,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	ExpiresAt         time.Time       `json:"expires_at"`
}

type MCPTaskContinuation struct {
	ID            string          `json:"id"`
	PrincipalID   string          `json:"principal_id"`
	GrantID       string          `json:"grant_id"`
	RecipeID      string          `json:"recipe_id"`
	RecipeVersion string          `json:"recipe_version"`
	Goal          string          `json:"goal"`
	Params        json.RawMessage `json:"params"`
	TargetRefs    json.RawMessage `json:"target_refs"`
	State         json.RawMessage `json:"state"`
	CreatedAt     time.Time       `json:"created_at"`
	ExpiresAt     time.Time       `json:"expires_at"`
}

type MCPFastPathMetric struct {
	ID                       string    `json:"id"`
	PrincipalID              string    `json:"principal_id"`
	GrantID                  string    `json:"grant_id"`
	RecipeID                 string    `json:"recipe_id,omitempty"`
	RecipeVersion            string    `json:"recipe_version,omitempty"`
	FastPathUsed             bool      `json:"fast_path_used"`
	Phase                    string    `json:"phase"`
	Status                   string    `json:"status"`
	DurationMS               int64     `json:"duration_ms"`
	FallbackReason           string    `json:"fallback_reason,omitempty"`
	NeedsInputCount          int       `json:"needs_input_count"`
	CandidateResolutionCount int       `json:"candidate_resolution_count"`
	ValidationFailure        bool      `json:"validation_failure"`
	ChangesetID              string    `json:"changeset_id,omitempty"`
	WorkflowID               string    `json:"workflow_id,omitempty"`
	FinalWorkflowStatus      string    `json:"final_workflow_status,omitempty"`
	CreatedAt                time.Time `json:"created_at"`
}
