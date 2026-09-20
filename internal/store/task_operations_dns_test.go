package store

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestTaskOperationDNSAtomicTargetsAndSummaries(t *testing.T) {
	ctx := context.Background()
	s, openErr := Open(filepath.Join(t.TempDir(), "operations.db"))
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer s.Close()
	one := model.Server{Name: "dns-one", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000}
	if err := s.CreateServer(ctx, &one); err != nil {
		t.Fatal(err)
	}
	two := one
	two.ID = 0
	two.Name = "dns-two"
	if err := s.CreateServer(ctx, &two); err != nil {
		t.Fatal(err)
	}
	item := OperationTask{Target: model.TaskOperationTarget{Type: "server", ID: strconv.FormatInt(one.ID, 10)}, Task: model.AgentTask{ServerID: one.ID, Type: model.AgentTaskTypeBenchmarkDNS, Status: "pending", PayloadJSON: "{}", ResultJSON: "{}"}, DNSRun: &model.DNSBenchmarkRun{RequestID: "atomic-dns-run", ServerID: one.ID, Trigger: "manual"}}
	failed := OperationTask{Target: model.TaskOperationTarget{Type: "server", ID: strconv.FormatInt(two.ID, 10), State: "failed", CauseCode: "dns_plan_invalid"}, Task: model.AgentTask{ServerID: two.ID}}
	op, ids, err := s.CreateTaskOperation(ctx, model.TaskOperation{Kind: "dns.test", Source: "ui", ActorPrincipal: "trusted-user"}, []OperationTask{item, failed})
	if err != nil {
		t.Fatal(err)
	}
	if ids[0] <= 0 || ids[1] != 0 || op.Targets[1].State != "failed" {
		t.Fatalf("incorrect targets: %+v %v", op, ids)
	}
	stored, err := s.GetTaskOperation(ctx, op.ID, []int64{one.ID, two.ID})
	if err != nil || stored.Targets[1].CauseCode != "dns_plan_invalid" {
		t.Fatalf("preparation cause lost: %+v %v", stored, err)
	}
	var runTask int64
	if err = s.db.QueryRowContext(ctx, `select task_id from dns_benchmark_runs where request_id=?`, item.DNSRun.RequestID).Scan(&runTask); err != nil || runTask != ids[0] {
		t.Fatalf("run link: %v %d", err, runTask)
	}
	tasks := []model.AgentTask{{ID: ids[0], ServerID: one.ID}}
	if err = s.AttachTaskOperations(ctx, tasks, []int64{one.ID}, false); err != nil {
		t.Fatal(err)
	}
	if len(tasks[0].Operations) != 0 {
		t.Fatal("partial target scope leaked association")
	}
	if err = s.AttachTaskOperations(ctx, tasks, []int64{one.ID, two.ID}, false); err != nil {
		t.Fatal(err)
	}
	if len(tasks[0].Operations) != 1 || tasks[0].Operations[0].Total != 2 || tasks[0].Operations[0].Failed != 1 || tasks[0].Operations[0].Pending != 1 {
		t.Fatalf("page-derived summary: %+v", tasks)
	}
	var beforeTasks, beforeOps int
	s.db.QueryRowContext(ctx, `select count(*) from agent_tasks`).Scan(&beforeTasks)
	s.db.QueryRowContext(ctx, `select count(*) from task_operations`).Scan(&beforeOps)
	if _, _, err = s.CreateTaskOperation(ctx, model.TaskOperation{Kind: "dns.test", Source: "ui", ActorPrincipal: "trusted-user"}, []OperationTask{item}); err == nil {
		t.Fatal("duplicate run must roll back entire transaction")
	}
	var afterTasks, afterOps int
	s.db.QueryRowContext(ctx, `select count(*) from agent_tasks`).Scan(&afterTasks)
	s.db.QueryRowContext(ctx, `select count(*) from task_operations`).Scan(&afterOps)
	if beforeTasks != afterTasks || beforeOps != afterOps {
		t.Fatal("failed run insert left tasks or operation")
	}
}
