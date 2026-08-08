package aiprovider

import (
	"errors"
	"net/http"
	"strings"
)

var deniedHeaders = map[string]struct{}{
	"authorization": {}, "proxy-authorization": {}, "cookie": {}, "set-cookie": {},
	"host": {}, "content-length": {}, "connection": {}, "transfer-encoding": {}, "x-api-key": {},
}

func ApplyHeaders(request *http.Request, endpoint RuntimeEndpoint) error {
	for key, value := range endpoint.Headers {
		canonical := strings.ToLower(strings.TrimSpace(key))
		if canonical == "" || strings.ContainsAny(key, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return errors.New("invalid custom header")
		}
		if _, denied := deniedHeaders[canonical]; denied {
			return errors.New("custom header is reserved")
		}
		request.Header.Set(key, value)
	}
	switch endpoint.AuthMode {
	case AuthModeBearer:
		if endpoint.Credential == "" {
			return errors.New("endpoint credential is required")
		}
		request.Header.Set("Authorization", "Bearer "+endpoint.Credential)
	case AuthModeXAPIKey:
		if endpoint.Credential == "" {
			return errors.New("endpoint credential is required")
		}
		request.Header.Set("x-api-key", endpoint.Credential)
	case AuthModeNone:
	default:
		return errors.New("unsupported endpoint authentication mode")
	}
	return nil
}
