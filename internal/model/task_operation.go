package model

import "time"

// TaskOperation identifies a business intent, not an authorization or idempotency key.
type TaskOperation struct {
	ID             string                 `json:"id"`
	Kind           string                 `json:"kind"`
	Source         string                 `json:"source"`
	ActorPrincipal string                 `json:"actor_principal"`
	ActorUserID    *int64                 `json:"actor_user_id,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	Targets        []TaskOperationTarget  `json:"targets,omitempty"`
	Attempts       []TaskOperationAttempt `json:"attempts,omitempty"`
}

type TaskOperationAttempt struct {
	TargetType     string `json:"target_type"`
	TargetID       string `json:"target_id"`
	TaskID         *int64 `json:"task_id"`
	Attempt        int    `json:"attempt"`
	ConfigVersion  *int64 `json:"config_version,omitempty"`
	ActualRevision *int64 `json:"actual_revision,omitempty"`
	ExecutionKind  string `json:"execution_kind"`
	State          string `json:"state"`
}

type TaskOperationTarget struct {
	Type            string    `json:"target_type"`
	ID              string    `json:"target_id"`
	State           string    `json:"state"`
	CauseCode       string    `json:"cause_code,omitempty"`
	DesiredRevision *int64    `json:"desired_revision,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type TaskOperationSummary struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at,omitempty"`
	Total     int    `json:"total"`
	Pending   int    `json:"pending"`
	Running   int    `json:"running"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
	Unknown   int    `json:"unknown"`
}

type TaskOperationLink struct {
	OperationID     string `json:"operation_id"`
	TargetType      string `json:"target_type"`
	TargetID        string `json:"target_id"`
	TaskID          int64  `json:"task_id"`
	Attempt         int    `json:"attempt"`
	AppliedRevision *int64 `json:"applied_revision,omitempty"`
	State           string `json:"state"`
}
