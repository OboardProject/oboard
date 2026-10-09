package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestServerIDReusesSmallestGapAfterDeletionCompletes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "servers.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	create := func(want int64) *model.Server {
		t.Helper()
		v := &model.Server{Name: fmt.Sprintf("server-%d", want)}
		if err := db.CreateServer(ctx, v); err != nil {
			t.Fatal(err)
		}
		if v.ID != want {
			t.Fatalf("ID=%d, want %d", v.ID, want)
		}
		return v
	}
	first := create(1)
	create(2)
	create(3)
	if err := db.DeleteServer(ctx, 2); err != nil {
		t.Fatal(err)
	}
	create(2)
	if _, _, err := db.BeginServerDeletion(ctx, 1, first.Name, `{}`); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteServer(ctx, 1); err != nil {
		t.Fatal(err)
	}
	create(4)
	if err := db.CompleteServerDeletion(ctx, 1); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := create(1)
	if replacement.ChainSecret == first.ChainSecret {
		t.Fatal("replacement inherited old chain secret")
	}
	if err := db.UpdateServer(ctx, first); err != ErrServerRevisionConflict {
		t.Fatalf("stale server update error=%v, want revision conflict", err)
	}
	if err := db.DeleteServer(ctx, 4); err != nil {
		t.Fatal(err)
	}
	create(4)
}

func TestServerIDConcurrentAllocation(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "servers.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var wg sync.WaitGroup
	ids := make(chan int64, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := &model.Server{Name: fmt.Sprintf("concurrent-%d", i)}
			if err := db.CreateServer(context.Background(), v); err != nil {
				t.Error(err)
				return
			}
			ids <- v.ID
		}(i)
	}
	wg.Wait()
	close(ids)
	seen := map[int64]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate ID %d", id)
		}
		seen[id] = true
	}
	for id := int64(1); id <= 12; id++ {
		if !seen[id] {
			t.Errorf("missing ID %d", id)
		}
	}
}

func TestServerIDReservesBasePathMigrationTargets(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(ctx, "controller_base_path_migration_targets", `[{"server_id":1,"server_name":"old"}]`); err != nil {
		t.Fatal(err)
	}
	v := &model.Server{Name: "during-migration"}
	if err := db.CreateServer(ctx, v); err != nil {
		t.Fatal(err)
	}
	if v.ID != 2 {
		t.Fatalf("reserved ID was reused: %d", v.ID)
	}
	if err := db.SetSetting(ctx, "controller_base_path_migration_targets", `[]`); err != nil {
		t.Fatal(err)
	}
	v = &model.Server{Name: "after-migration"}
	if err := db.CreateServer(ctx, v); err != nil {
		t.Fatal(err)
	}
	if v.ID != 1 {
		t.Fatalf("released ID not reused: %d", v.ID)
	}
}

func TestServerIDRemovesPersistedAccess(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	v := &model.Server{Name: "old"}
	if err := db.CreateServer(ctx, v); err != nil {
		t.Fatal(err)
	}
	op, _, err := db.CreateTaskOperation(ctx, model.TaskOperation{Kind: "dns.test", Source: "ui", ActorPrincipal: "user:test"}, []OperationTask{{Target: model.TaskOperationTarget{Type: "server", ID: strconv.FormatInt(v.ID, 10)}, Task: model.AgentTask{ServerID: v.ID, Type: model.AgentTaskTypeBenchmarkDNS, Status: "failed", PayloadJSON: `{}`, ResultJSON: `{}`}}})
	if err != nil {
		t.Fatal(err)
	}
	p := &model.APIPrincipal{ID: "scoped", Name: "scoped", Type: model.APIPrincipalServiceAccount, Enabled: true, ResourceFilter: json.RawMessage(`{"servers":{"mode":"selected","ids":[1,2]}}`)}
	if err := db.CreateAPIPrincipal(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteServer(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.db.QueryRowContext(ctx, `select resource_filter_json from api_principals where id='scoped'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var filter struct {
		Servers struct {
			IDs []int64 `json:"ids"`
		} `json:"servers"`
	}
	if err := json.Unmarshal([]byte(raw), &filter); err != nil {
		t.Fatal(err)
	}
	if len(filter.Servers.IDs) != 1 || filter.Servers.IDs[0] != 2 {
		t.Fatalf("old access survives deletion: %s", raw)
	}
	replacement := &model.Server{Name: "replacement"}
	if err := db.CreateServer(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.ID != v.ID {
		t.Fatalf("ID=%d, want %d", replacement.ID, v.ID)
	}
	if _, err := db.GetTaskOperation(ctx, op.ID, []int64{replacement.ID}); err != sql.ErrNoRows {
		t.Fatalf("replacement scope sees old operation: %v", err)
	}
	if _, err := db.RetryTaskOperationTarget(ctx, op.ID, OperationTask{Target: model.TaskOperationTarget{Type: "server", ID: strconv.FormatInt(v.ID, 10)}, Task: model.AgentTask{ServerID: v.ID, Type: model.AgentTaskTypeBenchmarkDNS, Status: "pending", PayloadJSON: `{}`, ResultJSON: `{}`}}); err != sql.ErrNoRows {
		t.Fatalf("old task can retry against replacement: %v", err)
	}
}

func TestRemoveServerAccessNeverWidensEmptySelection(t *testing.T) {
	for _, tt := range []struct{ kind, raw, want string }{
		{"filter", `{"server_ids":[1]}`, `{"server_ids":[]}`},
		{"filter", `{"servers":{"mode":"selected","ids":[1],"allow_create":true}}`, `{"servers":{"mode":"none","ids":[],"allow_create":true}}`},
		{"boundary", `{"resources":{"server":{"selection":"all","ids":["1"]}}}`, `{"resources":{"server":{"selection":"none","ids":[]}}}`},
		{"boundary", `{"resources":{"server":{"selection":"all","include_future":true,"ids":["1"]}}}`, `{"resources":{"server":{"selection":"all","include_future":true,"ids":[]}}}`},
		{"plugin", `{"capabilities":{"servers.read":{"servers":[1]},"network.ping":{"servers":[1,2]}}}`, `{"capabilities":{"network.ping":{"servers":[2]}}}`},
	} {
		t.Run(tt.kind+tt.raw, func(t *testing.T) {
			got, changed := removeServerAccess(tt.raw, tt.kind, 1)
			var wantValue, gotValue any
			json.Unmarshal([]byte(tt.want), &wantValue)
			json.Unmarshal([]byte(got), &gotValue)
			wantJSON, _ := json.Marshal(wantValue)
			gotJSON, _ := json.Marshal(gotValue)
			if !changed || string(wantJSON) != string(gotJSON) {
				t.Fatalf("got %s changed=%v; want %s", got, changed, tt.want)
			}
			if _, changed := removeServerAccess(got, tt.kind, 1); changed {
				t.Fatal("cleanup is not idempotent")
			}
		})
	}
}
