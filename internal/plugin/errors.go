package plugin

import (
	"errors"
	"net/http"
)

// Error codes are the plugin-facing contract. Plugins branch on these codes and
// never on the message text, which stays short, secret-free and localizable.
const (
	CodeCapabilityDenied         = "CAPABILITY_DENIED"
	CodeResourceDenied           = "RESOURCE_DENIED"
	CodeInvalidEnvironment       = "INVALID_ENVIRONMENT"
	CodeConfigurationRequired    = "CONFIGURATION_REQUIRED"
	CodePermissionReviewRequired = "PERMISSION_REVIEW_REQUIRED"
	CodeServerNotFound           = "SERVER_NOT_FOUND"
	CodeServerOffline            = "SERVER_OFFLINE"
	CodeServerBusy               = "SERVER_BUSY"
	CodeAgentPolicyDenied        = "AGENT_POLICY_DENIED"
	CodeUnsupportedCapability    = "UNSUPPORTED_CAPABILITY"
	CodeTargetNotAllowed         = "TARGET_NOT_ALLOWED"
	CodeHTTPHostDenied           = "HTTP_HOST_DENIED"
	CodeHTTPPrivateAddressDenied = "HTTP_PRIVATE_ADDRESS_DENIED"
	CodeHTTPTimeout              = "HTTP_TIMEOUT"
	CodeHTTPFailed               = "HTTP_FAILED"
	CodeHTTPResponseTooLarge     = "HTTP_RESPONSE_TOO_LARGE"
	CodeOperationTimeout         = "OPERATION_TIMEOUT"
	CodeOperationFailed          = "OPERATION_FAILED"
	CodeRunTimeout               = "RUN_TIMEOUT"
	CodeRateLimited              = "RATE_LIMITED"
	CodeLimitExceeded            = "LIMIT_EXCEEDED"
	CodeStateQuotaExceeded       = "STATE_QUOTA_EXCEEDED"
	CodeStateConflict            = "STATE_CONFLICT"
	CodeSecretNotConfigured      = "SECRET_NOT_CONFIGURED"
	CodePluginDisabled           = "PLUGIN_DISABLED"
	CodeRuntimeUnavailable       = "RUNTIME_UNAVAILABLE"
	CodeInvalidArgument          = "INVALID_ARGUMENT"
	CodeInvalidManifest          = "INVALID_MANIFEST"
	CodeInvalidPackage           = "INVALID_PACKAGE"
	CodeSignatureInvalid         = "SIGNATURE_INVALID"
	CodePublisherChanged         = "PUBLISHER_CHANGED"
	CodeRunInProgress            = "RUN_IN_PROGRESS"
	CodeCancelled                = "CANCELLED"
	CodeScriptError              = "SCRIPT_ERROR"
	CodeResourceLimit            = "RESOURCE_LIMIT"
	CodePermissionDenied         = "PERMISSION_DENIED"
	CodeNotFound                 = "NOT_FOUND"
	CodeConflict                 = "CONFLICT"
	CodeInternal                 = "INTERNAL_ERROR"
)

// Error carries a stable code and a short message. Messages never include
// secrets, upstream bodies or raw Go error strings from network layers.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Field optionally names the offending manifest, environment or argument
	// field so editors can point at it.
	Field string `json:"field,omitempty"`
	// Issues lists every field problem of a rejected environment save.
	Issues []FieldIssue `json:"issues,omitempty"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

func Fail(code, message string) *Error { return &Error{Code: code, Message: message} }

func FailField(code, field, message string) *Error {
	return &Error{Code: code, Field: field, Message: message}
}

var (
	ErrNotFound         = Fail(CodeNotFound, "not found")
	ErrConflict         = Fail(CodeConflict, "the record changed; reload and retry")
	ErrPermissionDenied = Fail(CodePermissionDenied, "permission denied")
)

// CodeOf returns the stable code for err, INTERNAL_ERROR for unknown errors.
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return CodeInternal
}

// AsError normalizes any error to a plugin Error without leaking internal text.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var coded *Error
	if errors.As(err, &coded) {
		return coded
	}
	return Fail(CodeInternal, "internal error")
}

// HTTPStatus maps a management-surface error to an HTTP status.
func HTTPStatus(err error) int {
	switch CodeOf(err) {
	case CodeNotFound, CodeServerNotFound:
		return http.StatusNotFound
	case CodePermissionDenied, CodeCapabilityDenied, CodeResourceDenied:
		return http.StatusForbidden
	case CodeConflict, CodeRunInProgress, CodePublisherChanged, CodeStateConflict, CodeRuntimeUnavailable, CodePluginDisabled, CodePermissionReviewRequired, CodeConfigurationRequired:
		return http.StatusConflict
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}
