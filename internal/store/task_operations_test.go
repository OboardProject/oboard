package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestTaskOperationsPersistenceAndBoundaries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "operations.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	// Recreate the previous schema: tasks exist, operations do not.
	if _, err = s.db.ExecContext(ctx, `drop trigger task_operations_task_update; drop trigger task_operations_task_delete; drop table task_operation_links; drop table task_operation_targets; drop table task_operations`); err != nil {
		t.Fatal(err)
	}
	server := model.Server{Name: "operations-one", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline, AgentID: "operations-agent"}
	if err = s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	second := server
	second.ID = 0
	second.Name = "operations-two"
	second.AgentID = "operations-agent-two"
	if err = s.CreateServer(ctx, &second); err != nil {
		t.Fatal(err)
	}
	item := func(id int64) OperationTask {
		return OperationTask{Target: model.TaskOperationTarget{Type: "server", ID: strconv.FormatInt(id, 10)}, Task: model.AgentTask{ServerID: id, Type: "benchmark_dns", Status: "pending", PayloadJSON: `{}`, ResultJSON: `{}`}}
	}
	legacy := item(server.ID).Task
	if err = s.CreateTask(ctx, &legacy); err != nil {
		t.Fatal(err)
	}
	if err = s.InitTaskOperations(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.InitTaskOperations(ctx); err != nil {
		t.Fatal(err)
	}
	intent := model.TaskOperation{Kind: "dns.test", Source: "ui", ActorPrincipal: "user:test"}
	a, ids, err := s.CreateTaskOperation(ctx, intent, []OperationTask{item(server.ID), item(second.ID)})
	if err != nil {
		t.Fatal(err)
	}
	b, bids, err := s.CreateTaskOperation(ctx, intent, []OperationTask{item(server.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("distinct business intents merged")
	}
	scope := []int64{server.ID, second.ID}
	if _, err = s.GetTaskOperation(ctx, a.ID, []int64{server.ID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("partial scope leaked intent: %v", err)
	}
	if _, err = s.GetTaskOperation(ctx, a.ID, nil); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty scope leaked intent: %v", err)
	}
	// One task can belong to two operations without copying or altering its payload.
	if err = s.LinkTaskOperationTarget(ctx, b.ID, item(second.ID).Target, ids[1]); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteTask(ctx, ids[0], "failed", `{"error":"sensitive raw failure"}`); err != nil {
		t.Fatal(err)
	}
	retry, err := s.RetryTaskOperationTarget(ctx, a.ID, item(server.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RetryTaskOperationTarget(ctx, a.ID, item(server.ID)); err == nil {
		t.Fatal("concurrent pending retry accepted")
	}
	var old string
	if err = s.db.QueryRowContext(ctx, `select state from task_operation_links where task_id=?`, ids[0]).Scan(&old); err != nil || old != "failed" {
		t.Fatalf("original failure lost: %q %v", old, err)
	}
	if _, err = s.db.ExecContext(ctx, `update task_operations set created_at=? where id=?`, time.Now().Add(-10*time.Minute).UTC().Format(time.RFC3339Nano), a.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteTask(ctx, retry, "succeeded", `{}`); err != nil {
		t.Fatal(err)
	}
	// Changing an older attempt must not overwrite the latest target state.
	if _, err = s.db.ExecContext(ctx, `update agent_tasks set result_json='late old failure' where id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListTaskOperationIDs(ctx, scope, "", "", 1)
	if err != nil || len(page) != 1 {
		t.Fatalf("page: %v %v", page, err)
	}
	detail, err := s.GetTaskOperation(ctx, a.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Targets) != 2 {
		t.Fatal("target aggregation depends on task page")
	}
	states := map[string]string{}
	for _, target := range detail.Targets {
		states[target.ID] = target.State
	}
	if states[strconv.FormatInt(server.ID, 10)] != "succeeded" || states[strconv.FormatInt(second.ID, 10)] != "pending" {
		t.Fatalf("incorrect partial result: %v", states)
	}
	if err = s.CompleteTask(ctx, ids[1], "succeeded", `{"superseded":true}`); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteTask(ctx, bids[0], "succeeded", `not-json`); err != nil {
		t.Fatal(err)
	}
	// Duplicate targets fail the complete transaction, including task inserts.
	var before, after int
	_ = s.db.QueryRowContext(ctx, `select count(*) from agent_tasks`).Scan(&before)
	if _, _, err = s.CreateTaskOperation(ctx, intent, []OperationTask{item(server.ID), item(server.ID)}); err == nil {
		t.Fatal("duplicate target accepted")
	}
	_ = s.db.QueryRowContext(ctx, `select count(*) from agent_tasks`).Scan(&after)
	if before != after {
		t.Fatal("failed transaction leaked tasks")
	}
	var count int
	_ = s.db.QueryRowContext(ctx, `select count(*) from task_operations`).Scan(&count)
	if count != 2 {
		t.Fatalf("failed transaction leaked operation: %d", count)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	detail, err = s.GetTaskOperation(ctx, a.ID, scope)
	if err != nil || len(detail.Targets) != 2 {
		t.Fatalf("reopen lost operation: %+v %v", detail, err)
	}
	var links int
	_ = s.db.QueryRowContext(ctx, `select count(*) from task_operation_links where task_id=?`, ids[1]).Scan(&links)
	if links != 2 {
		t.Fatal("many-to-many link lost")
	}
	// Ordinary task retention preserves both operations and terminal outcomes.
	if _, err = s.db.ExecContext(ctx, `delete from agent_tasks where id=?`, ids[1]); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		d, e := s.GetTaskOperation(ctx, id, scope)
		if e != nil {
			t.Fatal(e)
		}
		for _, target := range d.Targets {
			if target.ID == strconv.FormatInt(second.ID, 10) && target.State != "superseded" {
				t.Fatalf("cleanup lost outcome: %+v", target)
			}
		}
	}
	n, err := s.PruneTaskOperations(ctx, time.Now().Add(time.Hour), 500)
	if err != nil || n != 0 {
		t.Fatalf("pruned active task references: %d %v", n, err)
	}
	if _, err = s.db.ExecContext(ctx, `delete from agent_tasks`); err != nil {
		t.Fatal(err)
	}
	n, err = s.PruneTaskOperations(ctx, time.Now().Add(time.Hour), 1)
	if err != nil || n != 1 {
		t.Fatalf("bounded retention: %d %v", n, err)
	}
	_ = s.db.QueryRowContext(ctx, `select count(*) from task_operations`).Scan(&count)
	if count != 1 {
		t.Fatal("retention exceeded bound")
	}
}

func TestTaskOperationsDeletedPendingIsUnknown(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.InitTaskOperations(ctx); err != nil {
		t.Fatal(err)
	}
	server := model.Server{Name: "delete-pending", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline, AgentID: "delete-agent"}
	if err := s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	op, ids, err := s.CreateTaskOperation(ctx, model.TaskOperation{Kind: "dns.test", Source: "ui", ActorPrincipal: "user:test"}, []OperationTask{{Target: model.TaskOperationTarget{Type: "server", ID: strconv.FormatInt(server.ID, 10)}, Task: model.AgentTask{ServerID: server.ID, Type: "benchmark_dns", Status: "pending", PayloadJSON: `{}`, ResultJSON: `{}`}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `delete from agent_tasks where id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTaskOperation(ctx, op.ID, []int64{server.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Targets[0].State != "unknown" {
		t.Fatalf("lost evidence claimed success: %+v", got)
	}
	n, err := s.PruneTaskOperations(ctx, time.Now().Add(time.Hour), 500)
	if err != nil || n != 1 {
		t.Fatalf("expired unknown intent without live tasks retained: %d %v", n, err)
	}
}
