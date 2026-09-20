package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccountActivityRetiredRiskReadsNeverScore(t *testing.T) {
	// A nil store makes any accidental historical query fail this test.
	s := &Server{}
	for _, tc := range []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"/api/v1/audit/overview", s.connectionAuditOverview},
		{"/api/v1/audit/users/1", s.connectionAuditUser},
		{"/api/v1/audit/subscriptions/overview", s.subscriptionAuditOverview},
		{"/api/v1/audit/subscriptions/users/1", s.subscriptionAuditUser},
		{"/api/v1/audit/risk", s.combinedAuditOverview},
	} {
		rr := httptest.NewRecorder()
		tc.handler(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rr.Code != http.StatusGone || rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d", tc.path, rr.Code)
		}
	}
}
