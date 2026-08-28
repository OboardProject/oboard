package controller

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/store"
)

func TestQueryControllerNTPMedianRequiresQuorumAndUsesMedian(t *testing.T) {
	offsets := map[string]time.Duration{
		"one.example":   10 * time.Second,
		"two.example":   30 * time.Second,
		"three.example": 20 * time.Second,
	}
	reference, err := queryControllerNTPMedian(context.Background(), []string{"one.example", "two.example", "three.example"}, func(_ context.Context, server string) (time.Duration, error) {
		return offsets[server], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := reference.reference.Sub(reference.localAnchor); got != 20*time.Second {
		t.Fatalf("Controller NTP median offset = %s, want 20s", got)
	}
	if !strings.HasPrefix(reference.source, "ntp:") {
		t.Fatalf("Controller NTP source = %q", reference.source)
	}

	_, err = queryControllerNTPMedian(context.Background(), []string{"one.example", "two.example", "three.example"}, func(_ context.Context, server string) (time.Duration, error) {
		if server != "one.example" {
			return 0, errors.New("offline")
		}
		return offsets[server], nil
	})
	if err == nil || !strings.Contains(err.Error(), "至少需要两个") {
		t.Fatalf("Controller NTP quorum error = %v", err)
	}
}

func TestRefreshControllerTimeUsesConfiguredSources(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.SetSetting(ctx, settingTimeCheckNTPServers, `["one.example","two.example","three.example"]`); err != nil {
		t.Fatal(err)
	}
	srv := New(db, "test-secret", "", "", nil)
	queried := map[string]bool{}
	var queriedMu sync.Mutex
	srv.controllerNTPQuery = func(_ context.Context, server string) (time.Duration, error) {
		queriedMu.Lock()
		queried[server] = true
		queriedMu.Unlock()
		return 7 * time.Second, nil
	}
	srv.refreshControllerTime(ctx)
	queriedMu.Lock()
	queriedCount := len(queried)
	queriedMu.Unlock()
	if queriedCount != 3 {
		t.Fatalf("Controller queried NTP sources = %#v", queried)
	}
	current, source, ok := srv.controllerTimeNow()
	if !ok || current.Sub(time.Now()) < 6*time.Second || current.Sub(time.Now()) > 8*time.Second {
		t.Fatalf("Controller NTP time = %s, source=%q, available=%t", current, source, ok)
	}

	original := map[string]any{"type": "hello", "ts": time.Unix(1, 0)}
	message := srv.withControllerTime(original).(map[string]any)
	if message["ts_source"] != source {
		t.Fatalf("Controller timestamp source = %#v, want %q", message["ts_source"], source)
	}
	if original["ts"].(time.Time) != time.Unix(1, 0) {
		t.Fatal("Controller timestamp attachment mutated the caller payload")
	}
}

func TestControllerTimeOmitsExpiredOrUnavailableReference(t *testing.T) {
	srv := &Server{}
	payload := map[string]any{"type": "heartbeat", "ts": time.Now(), "ts_source": "system"}
	message := srv.withControllerTime(payload).(map[string]any)
	if _, exists := message["ts"]; exists {
		t.Fatalf("unavailable Controller reference kept timestamp: %#v", message)
	}

	srv.controllerNTPState = controllerNTPState{
		reference:   time.Now().UTC(),
		localAnchor: time.Now().Add(-controllerNTPMaxAge - time.Second),
		source:      "ntp:test",
	}
	message = srv.withControllerTime(payload).(map[string]any)
	if _, exists := message["ts"]; exists {
		t.Fatalf("expired Controller reference kept timestamp: %#v", message)
	}
}

func TestControllerNTPRefreshFailureKeepsFreshReference(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := New(db, "test-secret", "", "", nil)
	srv.controllerNTPState = controllerNTPState{
		reference:   time.Now().UTC().Add(3 * time.Second),
		localAnchor: time.Now(),
		source:      "ntp:previous",
	}
	srv.controllerNTPQuery = func(context.Context, string) (time.Duration, error) {
		return 0, errors.New("offline")
	}
	srv.refreshControllerTime(context.Background())
	_, source, ok := srv.controllerTimeNow()
	if !ok || source != "ntp:previous" {
		t.Fatalf("fresh Controller NTP reference was discarded: source=%q available=%t", source, ok)
	}
	srv.controllerNTPMu.RLock()
	lastError := srv.controllerNTPState.lastError
	srv.controllerNTPMu.RUnlock()
	if !strings.Contains(lastError, "至少需要两个") {
		t.Fatalf("Controller NTP refresh error = %q", lastError)
	}
}
