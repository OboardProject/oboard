package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestAccountSubscriptionIngressChargesEveryAuthenticatedRequestOnce(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful", true: "invalid_format"}[invalid], func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "audit.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			policy := store.DefaultAuditPolicy()
			policy.RawRequestsPer60Seconds.Soft = 2
			policy.RawRequestsPer60Seconds.Hard = 4
			raw, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.SetSetting(ctx, settingAuditPolicy, string(raw)); err != nil {
				t.Fatal(err)
			}
			srv := newTestServer(db, "test-secret", "")
			user := &model.User{Username: "ingress-budget", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", SubscriptionToken: "ingress-budget-token"}
			if err := db.CreateUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			db.AllowSubscriptionIngress("1.1.1.1", time.Now().Add(-time.Minute))
			format, want := "mihomo", http.StatusOK
			if invalid {
				format, want = "unsupported", http.StatusBadRequest
			}
			for i := 0; i < 5; i++ {
				r := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/ingress-budget-token?format="+format, nil)
				r.RemoteAddr = "1.1.1.1:3456"
				w := httptest.NewRecorder()
				srv.subscription(w, r)
				if i == 4 {
					want = http.StatusTooManyRequests
					if w.Header().Get("Retry-After") == "" {
						t.Fatal("missing retry deadline")
					}
				}
				if w.Code != want {
					t.Fatalf("request %d: got %d, want %d: %s", i+1, w.Code, want, w.Body.String())
				}
			}
		})
	}
}

type failedSubscriptionWriter struct{ header http.Header }

func (w *failedSubscriptionWriter) Header() http.Header       { return w.header }
func (w *failedSubscriptionWriter) WriteHeader(int)           {}
func (w *failedSubscriptionWriter) Write([]byte) (int, error) { return 0, errors.New("disconnected") }

func TestAccountSubscriptionHTTPDeliveryOnly(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "audit.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	enableTestAudit(t, db)
	srv := newTestServer(db, "test-secret", "")
	user := &model.User{Username: "subscription-activity", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "activity-uuid", ProxyPassword: "activity-password", SubscriptionToken: "activity-token"}
	if err = db.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	// Prewarm using the explicit event clock rather than sleeping through cold start.
	db.AllowSubscriptionIngress("1.1.1.1", time.Now().Add(-time.Minute))
	if err = db.SetAccountSubscriptionCoverage(context.Background(), true, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	fetch := func(etag string, w http.ResponseWriter) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/activity-token?format=mihomo", nil)
		r.RemoteAddr = "1.1.1.1:3456"
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		srv.subscription(w, r)
	}
	first := httptest.NewRecorder()
	fetch("", first)
	if first.Code != 200 {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	retry := httptest.NewRecorder()
	fetch(first.Header().Get("ETag"), retry)
	if retry.Code != 304 {
		t.Fatalf("conditional retry: %d %s", retry.Code, retry.Body.String())
	}
	fetch("", &failedSubscriptionWriter{header: make(http.Header)})
	got, err := db.LoadAccountSubscriptionFeatures(context.Background(), user.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got.State.RawRequests != 2 || got.State.LogicalUpdates != 1 {
		t.Fatalf("retry/write failure: %+v", got.State)
	}
	if len(got.State.Seen) != 1 {
		t.Fatalf("sources: %+v", got.State.Seen)
	}
}
