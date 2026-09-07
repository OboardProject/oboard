package scripting

import "errors"

var (
	ErrPermissionDenied      = errors.New(codePermissionDenied)
	ErrApprovalRequired      = errors.New(codeApprovalRequired)
	ErrResourceOutOfScope    = errors.New(codeResourceOutOfScope)
	ErrConditionChanged      = errors.New(codeConditionChanged)
	ErrTargetOffline         = errors.New(codeTargetOffline)
	ErrCapabilityUnsupported = errors.New(codeCapabilityUnsupported)
	ErrOperationExpired      = errors.New(codeOperationExpired)
	ErrIdempotencyConflict   = errors.New(codeIdempotencyConflict)
	ErrResultUnknown         = errors.New(codeResultUnknown)
	ErrRuntimeUnavailable    = errors.New(codeRuntimeUnavailable)
	ErrLimitExceeded         = errors.New(codeLimitExceeded)
	ErrInvalidInput          = errors.New(codeInvalidInput)
	ErrCancelled             = errors.New(codeCancelled)
	ErrNotFound              = errors.New("not_found")
	ErrConflict              = errors.New("conflict")
)

const (
	codePermissionDenied      = "permission_denied"
	codeApprovalRequired      = "approval_required"
	codeResourceOutOfScope    = "resource_out_of_scope"
	codeConditionChanged      = "condition_changed"
	codeTargetOffline         = "target_offline"
	codeCapabilityUnsupported = "capability_unsupported"
	codeOperationExpired      = "operation_expired"
	codeIdempotencyConflict   = "idempotency_conflict"
	codeResultUnknown         = "result_unknown"
	codeRuntimeUnavailable    = "runtime_unavailable"
	codeLimitExceeded         = "limit_exceeded"
	codeInvalidInput          = "invalid_input"
	codeCancelled             = "cancelled"
)

type Error struct {
	Code    string
	Message string
}

func (e Error) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

func Coded(code, message string) Error {
	return Error{Code: code, Message: message}
}

func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	var coded Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	switch {
	case errors.Is(err, ErrPermissionDenied):
		return codePermissionDenied
	case errors.Is(err, ErrApprovalRequired):
		return codeApprovalRequired
	case errors.Is(err, ErrResourceOutOfScope):
		return codeResourceOutOfScope
	case errors.Is(err, ErrConditionChanged):
		return codeConditionChanged
	case errors.Is(err, ErrTargetOffline):
		return codeTargetOffline
	case errors.Is(err, ErrCapabilityUnsupported):
		return codeCapabilityUnsupported
	case errors.Is(err, ErrOperationExpired):
		return codeOperationExpired
	case errors.Is(err, ErrIdempotencyConflict):
		return codeIdempotencyConflict
	case errors.Is(err, ErrResultUnknown):
		return codeResultUnknown
	case errors.Is(err, ErrRuntimeUnavailable):
		return codeRuntimeUnavailable
	case errors.Is(err, ErrLimitExceeded):
		return codeLimitExceeded
	case errors.Is(err, ErrInvalidInput):
		return codeInvalidInput
	case errors.Is(err, ErrCancelled):
		return codeCancelled
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrConflict):
		return "conflict"
	default:
		return "internal_error"
	}
}
