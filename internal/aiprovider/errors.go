package aiprovider

import (
	"errors"
	"fmt"
	"time"
)

const (
	ErrorNetwork            = "network_error"
	ErrorTimeout            = "timeout"
	ErrorRateLimited        = "rate_limited"
	ErrorAuthFailed         = "auth_failed"
	ErrorForbidden          = "forbidden"
	ErrorNotFound           = "not_found"
	ErrorInvalidRequest     = "invalid_request"
	ErrorUnsupportedFeature = "unsupported_feature"
	ErrorUpstream5xx        = "upstream_5xx"
	ErrorParse              = "parse_error"
	ErrorSchemaValidation   = "schema_validation_failed"
	ErrorResponseTooLarge   = "response_too_large"
	ErrorCircuitOpen        = "circuit_open"
	ErrorAllEndpoints       = "all_endpoints_failed"
	ErrorNoEligible         = "no_eligible_endpoint"
)

type ProviderError struct {
	Kind              string
	Retryable         bool
	HTTPStatus        int
	ProviderID        string
	EndpointID        string
	ProviderRequestID string
	RetryAfter        time.Duration
	Message           string
	Cause             error
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("%s (HTTP %d): %s", e.Kind, e.HTTPStatus, e.Message)
	}
	return e.Kind + ": " + e.Message
}

func (e *ProviderError) Unwrap() error { return e.Cause }

func NewError(kind string, retryable bool, status int, message string, cause error) *ProviderError {
	return &ProviderError{Kind: kind, Retryable: retryable, HTTPStatus: status, Message: message, Cause: cause}
}

func AsProviderError(err error) *ProviderError {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return providerErr
	}
	return NewError(ErrorNetwork, true, 0, "upstream request failed", err)
}

func ErrorForStatus(status int, message string) *ProviderError {
	switch status {
	case 400, 422:
		return NewError(ErrorInvalidRequest, false, status, message, nil)
	case 401:
		return NewError(ErrorAuthFailed, false, status, message, nil)
	case 403:
		return NewError(ErrorForbidden, false, status, message, nil)
	case 404:
		return NewError(ErrorNotFound, false, status, message, nil)
	case 408:
		return NewError(ErrorTimeout, true, status, message, nil)
	case 429:
		return NewError(ErrorRateLimited, true, status, message, nil)
	default:
		if status >= 500 {
			return NewError(ErrorUpstream5xx, true, status, message, nil)
		}
		return NewError(ErrorInvalidRequest, false, status, message, nil)
	}
}
