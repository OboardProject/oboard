package aiprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	MaxResponseBytes = 2 << 20
	MaxModelsBytes   = 4 << 20
	MaxErrorBytes    = 64 << 10
)

func ReadBounded(body io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, NewError(ErrorResponseTooLarge, false, 0, "upstream response exceeds the allowed size", nil)
	}
	return data, nil
}

func DecodeProviderError(response *http.Response, credential string) error {
	body, readErr := ReadBounded(response.Body, MaxErrorBytes)
	if readErr != nil {
		return readErr
	}
	message := strings.TrimSpace(string(body))
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		if envelope.Message != "" {
			message = envelope.Message
		} else if len(envelope.Error) > 0 {
			var object struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			}
			if json.Unmarshal(envelope.Error, &object) == nil {
				message = object.Message
				if message == "" {
					message = object.Type
				}
			}
		}
	}
	message = Bounded(Redact(strings.Join(strings.Fields(message), " "), credential), 1024)
	providerErr := ErrorForStatus(response.StatusCode, message)
	providerErr.ProviderRequestID = response.Header.Get("x-request-id")
	providerErr.RetryAfter = ParseRetryAfter(response.Header.Get("Retry-After"), time.Now())
	return providerErr
}

func RequestError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewError(ErrorTimeout, true, 0, "upstream request timed out", err)
	}
	return NewError(ErrorNetwork, true, 0, "upstream network request failed", err)
}
