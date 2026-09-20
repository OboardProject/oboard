package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestConfigurationOperationEvidence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "intent.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Start from the actual preceding operation schema, without configuration provenance.
	_, err = s.db.Exec(`drop trigger configuration_operations_sync_result; drop trigger configuration_operations_preparation_failure; drop trigger configuration_operations_superseded; drop trigger configuration_operations_semantic_noop; drop table configuration_operation_attempts; drop table configuration_operation_fields`)
	must(err)
	must(s.Close())
	s, err = Open(path)
	must(err)
	v := model.Server{Name: "intent", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline, AgentID: "test-agent"}
	must(s.CreateServer(ctx, &v))
	save := func(fields ...string) string {
		t.Helper()
		in := &ServerConfigurationIntent{ActorPrincipal: "user:test", Source: "web", Fields: fields}
		must(s.UpdateServerSettings(ctx, &v, ServerUpdateOptions{ConfigurationIntent: in}))
		if in.OperationID == "" {
			t.Fatal("missing committed operation")
		}
		return in.OperationID
	}
	state := func(id, want string) {
		t.Helper()
		var got string
		must(s.db.QueryRow(`select state from task_operation_targets where operation_id=?`, id).Scan(&got))
		if got != want {
			t.Fatalf("%s state=%s want %s", id, got, want)
		}
	}
	revision := func() uint64 { r, e := s.ConfigurationRevision(ctx); must(e); return r }
	queue := func(version int64, fresh bool) int64 {
		t.Helper()
		r := revision()
		_, e := s.MarkConfigurationSyncPending(ctx, r, []int64{v.ID})
		must(e)
		ok, e := s.ClaimConfigurationSync(ctx, v.ID, r)
		must(e)
		if !ok {
			t.Fatal("claim failed")
		}
		task := model.AgentTask{ServerID: v.ID, Type: "apply_deployment", Status: "pending", ConfigVersion: version, PayloadJSON: `{"config":"unchanged-by-tracking"}`, ResultJSON: `{}`}
		must(s.CreateTask(ctx, &task))
		if fresh {
			must(s.MarkConfigurationSyncDeploymentQueued(ctx, v.ID, r, version, task.ID, "fixed-digest"))
		} else {
			must(s.MarkConfigurationSyncQueued(ctx, v.ID, r, version, task.ID, "fixed-digest"))
		}
		return task.ID
	}
	historical := save("ip_stack")
	state(historical, "evidence_insufficient")
	v.IPStack = "ipv4"
	a := save("ip_stack")
	v.ListenMode = "ipv4_only"
	b := save("listen_mode")
	task := queue(101, true)
	state(a, "queued")
	state(b, "queued")
	var mergedKind string
	must(s.db.QueryRow(`select execution_kind from configuration_operation_attempts where operation_id=?`, a).Scan(&mergedKind))
	if mergedKind != "later_modified" {
		t.Fatal("merged delivery did not disclose newer content", mergedKind)
	}
	var count int
	must(s.db.QueryRow(`select count(*) from task_operation_links where task_id=?`, task).Scan(&count))
	if count != 2 {
		t.Fatalf("merge lost sources: %d", count)
	}
	must(s.CompleteTask(ctx, task, "succeeded", `{}`))
	state(a, "queued") // task success alone is not convergence
	must(s.MarkConfigurationSyncResult(ctx, v.ID, 101, false, "failure"))
	state(a, "failed")
	queue(102, true)
	var kind string
	must(s.db.QueryRow(`select execution_kind from configuration_operation_attempts where operation_id=? order by attempt desc limit 1`, b).Scan(&kind))
	if kind != "original_retry" {
		t.Fatal(kind)
	}
	must(s.MarkConfigurationSyncResult(ctx, v.ID, 101, true, ""))
	state(a, "queued")
	v.IPStack = "ipv6"
	c := save("ip_stack")
	state(a, "superseded")
	state(b, "queued")
	// A result for the older delivery cannot revive the overwritten intent.
	must(s.MarkConfigurationSyncResult(ctx, v.ID, 102, true, ""))
	state(a, "superseded")
	final := queue(103, true)
	must(s.db.QueryRow(`select execution_kind from configuration_operation_attempts where operation_id=? order by attempt desc limit 1`, c).Scan(&kind))
	if kind != "original_apply" {
		t.Fatal(kind)
	}
	must(s.MarkConfigurationSyncResult(ctx, v.ID, 103, true, ""))
	state(c, "succeeded")
	var payload string
	must(s.db.QueryRow(`select payload_json from agent_tasks where id=?`, final).Scan(&payload))
	if payload != `{"config":"unchanged-by-tracking"}` {
		t.Fatal("tracking changed payload")
	}
	var old string
	must(s.db.QueryRow(`select state from task_operation_links where operation_id=? and task_id=?`, a, task).Scan(&old))
	if old != "failed" {
		t.Fatal("old failure overwritten", old)
	}
	same := save("ip_stack")
	state(same, "no_change")
	must(s.MarkConfigurationSyncNoop(ctx, v.ID, revision(), "same"))
	state(same, "no_change")
	details, err := s.ListTaskOperationRecords(ctx, []int64{v.ID}, false, b, "", "", 25)
	must(err)
	if len(details) != 1 || len(details[0].Attempts) != 2 {
		t.Fatalf("missing public attempts: %+v", details)
	}
	attempts := details[0].Attempts
	if attempts[0].State != "failed" || attempts[0].TaskID == nil || *attempts[0].TaskID != task || attempts[1].ExecutionKind != "original_retry" || attempts[1].ActualRevision == nil || uint64(*attempts[1].ActualRevision) >= revision() || attempts[1].ConfigVersion == nil || *attempts[1].ConfigVersion != 102 {
		t.Fatalf("incorrect public retry evidence: %+v", attempts)
	}
	merged, err := s.ListTaskOperationRecords(ctx, []int64{v.ID}, false, a, "", "", 25)
	must(err)
	if len(merged) != 1 || len(merged[0].Attempts) != 2 || merged[0].Attempts[0].ExecutionKind != "later_modified" {
		t.Fatal("missing public merged revision evidence")
	}
	denied, err := s.ListTaskOperationRecords(ctx, []int64{v.ID + 1}, false, b, "", "", 25)
	must(err)
	if len(denied) != 0 {
		t.Fatal("unauthorized operation detail")
	}
	// A later untracked configuration change invalidates the earlier proof,
	// even if the tracked field itself and topology digest remain unchanged.
	v.Name = "intent-renamed"
	save("name")
	unconfirmed := save("ip_stack")
	state(unconfirmed, "evidence_insufficient")
	v.ListenIP = "::"
	reused := save("listen_ip")
	queue(104, false)
	must(s.MarkConfigurationSyncResult(ctx, v.ID, 104, true, ""))
	state(reused, "pending")
	local := save("name")
	state(local, "evidence_insufficient")
	// Transaction failure must preserve both desired state and the externally visible ID.
	before := revision()
	in := &ServerConfigurationIntent{ActorPrincipal: "user:test", Source: "web", Fields: []string{"ip_stack"}}
	_, err = s.db.Exec(`create trigger reject_intent before insert on task_operations begin select raise(abort,'test rollback'); end`)
	must(err)
	v.IPStack = "ipv4"
	if s.UpdateServerSettings(ctx, &v, ServerUpdateOptions{ConfigurationIntent: in}) == nil {
		t.Fatal("expected rollback")
	}
	if in.OperationID != "" || revision() != before {
		t.Fatal("rollback leaked intent or revision")
	}
	_, err = s.db.Exec(`drop trigger reject_intent`)
	must(err)
	must(s.Close())
	s, err = Open(path)
	must(err)
	state(a, "superseded")
	state(c, "succeeded")
	state(same, "no_change")
	state(local, "evidence_insufficient")
	details, err = s.ListTaskOperationRecords(ctx, []int64{v.ID}, false, b, "", "", 25)
	must(err)
	if len(details) != 1 || len(details[0].Attempts) != 2 {
		t.Fatal("attempts lost on reopen")
	}
	mustExec := func(query string, args ...any) { _, err := s.db.Exec(query, args...); must(err) }
	mustExec(`delete from agent_tasks where id=?`, task)
	details, err = s.ListTaskOperationRecords(ctx, []int64{v.ID}, false, b, "", "", 25)
	must(err)
	if len(details) != 1 || len(details[0].Attempts) != 2 || details[0].Attempts[0].TaskID != nil || details[0].Attempts[0].State != "failed" {
		t.Fatal("retained attempt did not disclose deleted task")
	}

}
