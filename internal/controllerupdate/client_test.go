package controllerupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestClientPreservesPlainTextHTTPErrorStatus(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/prepare" {
			t.Fatalf("unexpected updater path %q", request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("404 page not found\n")),
			Request:    request,
		}, nil
	})}}

	_, err := client.Prepare(context.Background())
	var statusErr *UpdaterStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("plain-text updater error type = %T, want *UpdaterStatusError", err)
	}
	if statusErr.Code != http.StatusNotFound || statusErr.Message != "404 page not found" {
		t.Fatalf("unexpected updater error: %#v", statusErr)
	}
}
