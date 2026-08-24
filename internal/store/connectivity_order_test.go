package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestConnectivityHistoryOrdersProbeBeforeConnectionTransitionAtSameTime(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "connectivity-order.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "order-node", AgentID: "order-agent"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	if err := db.RecordControllerConnectionEvent(ctx, server.ID, false, at); err != nil {
		t.Fatal(err)
	}
	available := true
	if _, err := insertConnectivityEvent(ctx, db.db, model.ServerConnectivityEvent{ServerID: server.ID, Kind: model.ConnectivityEventProbeResult, Available: &available, LatencyMS: 25, Source: "latency_probe", EffectiveAt: at, EventKey: "same-time-probe"}); err != nil {
		t.Fatal(err)
	}
	history, err := db.ListConnectivityHistory(ctx, server.ID, at.Add(-time.Second), at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ordered := make([]model.ConnectivityEventKind, 0, 2)
	for _, event := range history.Events {
		if event.EffectiveAt.Equal(at) {
			ordered = append(ordered, event.Kind)
		}
	}
	if len(ordered) != 2 || ordered[0] != model.ConnectivityEventProbeResult || ordered[1] != model.ConnectivityEventControllerDisconnected {
		t.Fatalf("same-time order=%v", ordered)
	}
}
