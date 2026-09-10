package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func openPresencePruneStore(t *testing.T) (*Store, *model.Server, *model.User) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "presence-prune.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server := &model.Server{Name: "presence-node", AgentID: "presence-agent", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "presence-account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111177", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	return db, server, user
}

func presenceEvent(server *model.Server, user *model.User, sequence uint64, at time.Time) model.ConnectionPresenceEvent {
	return model.ConnectionPresenceEvent{
		Sequence: sequence, ServerID: server.ID, UserID: user.ID, InboundID: 3, SourceIP: "198.51.100.4",
		Network: "tcp", Event: "first_meaningful_payload", State: "active", ActiveConnections: 1,
		Meaningful: true, PayloadLastAt: at, At: at,
	}
}

// TestPresenceReportDoesNotPruneTheFleet proves an Agent report pays only for
// its own batch: the retention sweep is no longer part of its transaction.
func TestPresenceReportDoesNotPruneTheFleet(t *testing.T) {
	ctx := context.Background()
	db, server, user := openPresencePruneStore(t)
	old := time.Now().UTC().Add(-48 * time.Hour)
	// Seed an expired event directly so the report below cannot be credited
	// with having created it.
	if _, err := db.db.ExecContext(ctx, `insert into connection_presence_events(agent_id,sequence,server_id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,source_ip,route_id,network,event,state,active_connections,meaningful,payload_last_at,event_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		server.AgentID, 1, server.ID, user.ID, 3, 0, "", 0, "198.51.100.4", "", "tcp", "activity_refresh", "active", 1, 0, nil, old.Format(time.RFC3339Nano), old.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.ApplyConnectionPresenceEvents(ctx, server.AgentID, server.ID, 0, []model.ConnectionPresenceEvent{presenceEvent(server, user, 2, now)}); err != nil {
		t.Fatal(err)
	}
	var expired int
	if err := db.db.QueryRowContext(ctx, `select count(*) from connection_presence_events where event_at<?`, now.Add(-connectionPresenceRetention).Format(time.RFC3339Nano)).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired != 1 {
		t.Fatalf("the report transaction pruned the fleet's expired presence (%d expired rows left)", expired)
	}

	// The expired row is invisible to readers regardless, because presence is
	// read through its business validity window.
	presence, err := db.ListConnectionPresenceForUser(ctx, user.ID, now.Add(-2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range presence {
		if item.At.Before(now.Add(-time.Minute)) {
			t.Fatalf("an expired presence row was returned to a reader: %+v", item)
		}
	}

	// Maintenance reclaims it.
	result, err := db.PruneConnectionPresence(ctx, now, 500, 8)
	if err != nil {
		t.Fatal(err)
	}
	if result.EventsDeleted != 1 {
		t.Fatalf("prune deleted %d expired events, want 1", result.EventsDeleted)
	}
	if err := db.db.QueryRowContext(ctx, `select count(*) from connection_presence_events where event_at<?`, now.Add(-connectionPresenceRetention).Format(time.RFC3339Nano)).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired != 0 {
		t.Fatalf("%d expired events survived the prune", expired)
	}
}

// TestPresencePruneIsBounded proves a backlog is worked off in bounded batches
// rather than one fleet-wide DELETE, and that the caller is told to come back.
func TestPresencePruneIsBounded(t *testing.T) {
	ctx := context.Background()
	db, server, user := openPresencePruneStore(t)
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 120 {
		if _, err := tx.ExecContext(ctx, `insert into connection_presence_events(agent_id,sequence,server_id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,source_ip,route_id,network,event,state,active_connections,meaningful,payload_last_at,event_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			server.AgentID, i+1, server.ID, user.ID, 3, 0, "", 0, "198.51.100.4", "", "tcp", "activity_refresh", "active", 1, 0, nil, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	// A budget of two batches of ten cannot finish 120 rows and must say so.
	partial, err := db.PruneConnectionPresence(ctx, now, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	if partial.EventsDeleted != 20 {
		t.Fatalf("bounded pass deleted %d rows, want its 2x10 budget", partial.EventsDeleted)
	}
	if !partial.More {
		t.Fatal("an exhausted budget must report that the backlog continues")
	}
	// Repeated bounded passes catch up.
	for range 20 {
		result, err := db.PruneConnectionPresence(ctx, now, 10, 2)
		if err != nil {
			t.Fatal(err)
		}
		if !result.More {
			break
		}
	}
	var remaining int
	if err := db.db.QueryRowContext(ctx, `select count(*) from connection_presence_events`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("%d expired events survived repeated bounded passes", remaining)
	}
}

// TestPresencePruneKeepsLiveRows proves the sweep only reclaims rows that are
// already outside the window readers use.
func TestPresencePruneKeepsLiveRows(t *testing.T) {
	ctx := context.Background()
	db, server, user := openPresencePruneStore(t)
	now := time.Now().UTC()
	if _, err := db.ApplyConnectionPresenceEvents(ctx, server.AgentID, server.ID, 0, []model.ConnectionPresenceEvent{presenceEvent(server, user, 1, now)}); err != nil {
		t.Fatal(err)
	}
	result, err := db.PruneConnectionPresence(ctx, now, 500, 8)
	if err != nil {
		t.Fatal(err)
	}
	if result.EventsDeleted != 0 || result.StatesDeleted != 0 {
		t.Fatalf("prune removed live presence: %+v", result)
	}
	presence, err := db.ListConnectionPresenceForUser(ctx, user.ID, now.Add(-2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(presence) != 1 {
		t.Fatalf("live presence disappeared: %d rows", len(presence))
	}
}
