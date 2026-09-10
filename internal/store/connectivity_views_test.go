package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestConnectivityEventPagesBoundedStableAndFractional(t *testing.T) {
	db, server := newConnectivityTestStore(t)
	ctx := context.Background()
	db.db.db.SetMaxOpenConns(1)
	from := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	to := from.Add(time.Hour)
	for i, at := range []time.Time{from.Add(-time.Second), from, from.Add(100 * time.Millisecond), from.Add(time.Minute), from.Add(time.Minute), to.Add(-100 * time.Millisecond), to} {
		if _, err := insertConnectivityEvent(ctx, db.db, model.ServerConnectivityEvent{ServerID: server.ID, Kind: model.ConnectivityEventProbeResult, EffectiveAt: at, EventKey: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := db.ListConnectivityEventPage(ctx, server.ID, from, to, 2, "", 0, 0)
	if err != nil || len(page.Events) != 2 || !page.HasMore {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	seen := map[int64]bool{}
	for _, event := range page.Events {
		seen[event.ID] = true
	}
	// New late events cannot enter an already opened sequence.
	if _, err := insertConnectivityEvent(ctx, db.db, model.ServerConnectivityEvent{ServerID: server.ID, Kind: model.ConnectivityEventProbeResult, EffectiveAt: from.Add(30 * time.Second), EventKey: "late"}); err != nil {
		t.Fatal(err)
	}
	for page.HasMore {
		page, err = db.ListConnectivityEventPage(ctx, server.ID, from, to, 2, page.BeforeTime, page.BeforeID, page.SnapshotID)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range page.Events {
			if seen[event.ID] || event.EventKey == "late" || event.EffectiveAt.Before(from) || !event.EffectiveAt.Before(to) {
				t.Fatalf("bad event: %+v", event)
			}
			seen[event.ID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("seen=%v", seen)
	}
	var detail string
	rows, err := db.db.Query(`explain query plan select id,server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at from server_connectivity_events where server_id=? and effective_at>=? and effective_at<? and id<=? and (effective_at,id)<(?,?) order by effective_at desc,id desc limit 101`, server.ID, connectivityTimeBound(from), connectivityTimeBound(to), 999999, connectivityTimeBound(to), 999999)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a, b, c int
		var d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		detail += d
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "idx_server_connectivity_events_server_time") || strings.Contains(detail, "TEMP B-TREE") {
		t.Fatal(detail)
	}
	t.Log(detail)
}

func TestConnectivitySLAHistoryBoundsPriorityAndBudget(t *testing.T) {
	db, server := newConnectivityTestStore(t)
	ctx := context.Background()
	from := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	enabled := true
	for i, item := range []struct {
		at   time.Time
		kind model.ConnectivityEventKind
	}{
		{from.Add(-time.Second), model.ConnectivityEventProbeEnabled},
		{from, model.ConnectivityEventControllerDisconnected},
		{from, model.ConnectivityEventProbeResult},
		{from, model.ConnectivityEventProbeDisabled},
		{from.Add(100 * time.Millisecond), model.ConnectivityEventControllerConnected},
		{from.Add(time.Hour), model.ConnectivityEventProbeResult},
	} {
		if _, err := insertConnectivityEvent(ctx, db.db, model.ServerConnectivityEvent{ServerID: server.ID, Kind: item.kind, Available: &enabled, EffectiveAt: item.at, EventKey: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	history, err := db.ListConnectivitySLAHistory(ctx, server.ID, from, from.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Events) != 4 || len(history.Baseline) != 1 || history.Events[0].Kind != model.ConnectivityEventProbeDisabled || history.Events[1].Kind != model.ConnectivityEventProbeResult || history.Events[2].Kind != model.ConnectivityEventControllerDisconnected || history.Events[3].Kind != model.ConnectivityEventControllerConnected {
		t.Fatalf("history=%+v", history)
	}
	if _, err := db.listConnectivityHistory(ctx, server.ID, from, from.Add(time.Hour), 2); !errors.Is(err, ErrConnectivityEventBudget) {
		t.Fatalf("budget err=%v", err)
	}
}
