package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// InitTaskOperations must be called by the schema initializer before using this API.
// Targets and attempt outcomes survive task/server retention; no payload or result is copied.
func (s *Store) InitTaskOperations(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
create table if not exists task_operations (
 id text primary key, kind text not null, source text not null,
 actor_principal text not null, actor_user_id integer, created_at text not null
);
create index if not exists task_operations_created on task_operations(created_at,id);
create table if not exists task_operation_targets (
 operation_id text not null references task_operations(id) on delete cascade,
 target_type text not null, target_id text not null, state text not null, cause_code text not null default '',
 desired_revision integer, updated_at text not null,
 primary key(operation_id,target_type,target_id)
);
create index if not exists task_operation_targets_resource on task_operation_targets(target_type,target_id,operation_id);
create table if not exists task_operation_links (
 operation_id text not null, target_type text not null, target_id text not null,
 task_id integer not null, attempt integer not null, applied_revision integer,
 state text not null,
 primary key(operation_id,target_type,target_id,attempt),
 unique(operation_id,target_type,target_id,task_id),
 foreign key(operation_id,target_type,target_id) references task_operation_targets(operation_id,target_type,target_id) on delete cascade
);
create index if not exists task_operation_links_task on task_operation_links(task_id);
drop trigger if exists task_operations_task_update;
drop trigger if exists task_operations_task_delete;
create trigger if not exists task_operations_task_update after update of status,result_json on agent_tasks begin
 update task_operation_links set state=case
 when new.status='succeeded' and json_extract(case when json_valid(new.result_json) then new.result_json else '{}' end,'$.superseded')=1 then 'superseded'
 else new.status end where task_id=new.id and operation_id not in (select id from task_operations where kind='servers.update');
 update task_operation_targets set state=(select l.state from task_operation_links l
 where l.operation_id=task_operation_targets.operation_id and l.target_type=task_operation_targets.target_type and l.target_id=task_operation_targets.target_id order by l.attempt desc limit 1), updated_at=new.updated_at
 where (operation_id,target_type,target_id) in (select operation_id,target_type,target_id from task_operation_links where task_id=new.id and operation_id not in (select id from task_operations where kind='servers.update'));
end;
create trigger if not exists task_operations_task_delete before delete on agent_tasks begin
 update task_operation_links set state='unknown' where task_id=old.id and operation_id not in (select id from task_operations where kind='servers.update') and state not in ('succeeded','failed','rollback_failed','superseded');
 update task_operation_targets set state=(select l.state from task_operation_links l
 where l.operation_id=task_operation_targets.operation_id and l.target_type=task_operation_targets.target_type and l.target_id=task_operation_targets.target_id order by l.attempt desc limit 1)
 where (operation_id,target_type,target_id) in (select operation_id,target_type,target_id from task_operation_links where task_id=old.id and operation_id not in (select id from task_operations where kind='servers.update'));
end;`)
	return err
}

// OperationTask binds a server target to a newly enqueued task. The caller must
// authorize the entire target set and derive the actor from its trusted principal.
type OperationTask struct {
	Target model.TaskOperationTarget
	Task   model.AgentTask
	DNSRun *model.DNSBenchmarkRun
}

func operationTaskInsert(ctx context.Context, tx *sql.Tx, task model.AgentTask, ts string) (int64, error) {
	if task.Status != "pending" && task.Status != "failed" {
		return 0, errors.New("operation tasks must start pending or explicitly failed")
	}
	claimed, err := serverDeletionClaimedTx(ctx, tx, task.ServerID)
	if err != nil {
		return 0, err
	}
	if claimed {
		return 0, ErrServerDeleting
	}
	var completed any
	if task.Status == "failed" {
		completed = ts
	}
	res, err := tx.ExecContext(ctx, `insert into agent_tasks(server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at) values(?,?,?,?,?,?,?,?,?,?)`, task.ServerID, task.Type, task.PayloadJSON, task.Status, task.ResultJSON, task.ConfigVersion, task.Nonce, ts, ts, completed)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func validateOperationTask(item OperationTask) error {
	if item.Target.Type != "server" || item.Task.ServerID <= 0 || item.Target.ID != strconv.FormatInt(item.Task.ServerID, 10) {
		return errors.New("operation target must match task server")
	}
	return nil
}

// CreateTaskOperation atomically persists the intent, complete target set and tasks.
// Input/output task values are untouched on rollback.
func (s *Store) CreateTaskOperation(ctx context.Context, op model.TaskOperation, items []OperationTask) (*model.TaskOperation, []int64, error) {
	if op.Kind == "" || op.Source == "" || op.ActorPrincipal == "" || len(items) == 0 || len(items) > 1000 {
		return nil, nil, errors.New("invalid task operation")
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, nil, err
	}
	op.ID = hex.EncodeToString(b)
	ts := now()
	op.CreatedAt = parseTime(ts)
	op.Targets = nil
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `insert into task_operations(id,kind,source,actor_principal,actor_user_id,created_at) values(?,?,?,?,?,?)`, op.ID, op.Kind, op.Source, op.ActorPrincipal, op.ActorUserID, ts)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		if err = validateOperationTask(item); err != nil {
			return nil, nil, err
		}
		target := item.Target
		target.State = item.Task.Status
		if item.Task.Type == "" {
			if item.Target.State != "failed" && item.Target.State != "skipped" {
				return nil, nil, errors.New("target without task must be failed or skipped")
			}
			target.State = item.Target.State
		}
		target.UpdatedAt = op.CreatedAt
		_, err = tx.ExecContext(ctx, `insert into task_operation_targets(operation_id,target_type,target_id,state,cause_code,desired_revision,updated_at) values(?,?,?,?,?,?,?)`, op.ID, target.Type, target.ID, target.State, target.CauseCode, target.DesiredRevision, ts)
		if err != nil {
			return nil, nil, err
		}
		op.Targets = append(op.Targets, target)
		if item.Task.Type == "" {
			ids = append(ids, 0)
			continue
		}
		id, err := operationTaskInsert(ctx, tx, item.Task, ts)
		if err != nil {
			return nil, nil, err
		}
		_, err = tx.ExecContext(ctx, `insert into task_operation_links(operation_id,target_type,target_id,task_id,attempt,state) values(?,?,?,?,1,?)`, op.ID, target.Type, target.ID, id, target.State)
		if err != nil {
			return nil, nil, err
		}
		if item.DNSRun != nil {
			v := item.DNSRun
			if item.Task.Type != model.AgentTaskTypeBenchmarkDNS || v.ServerID != item.Task.ServerID || v.ApplyOnSuccess {
				return nil, nil, errors.New("invalid diagnostic DNS run")
			}
			status := "running"
			var completed any
			if target.State == "failed" {
				status = "failed"
				completed = ts
			}
			_, err = tx.ExecContext(ctx, `insert into dns_benchmark_runs(request_id,server_id,policy_revision,encrypted_list_id,encrypted_list_revision,bootstrap_list_id,bootstrap_list_revision,trigger,apply_on_success,requested_by,task_id,status,error,started_at,completed_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.RequestID, v.ServerID, v.PolicyRevision, v.EncryptedListID, v.EncryptedListRevision, v.BootstrapListID, v.BootstrapListRevision, v.Trigger, 0, v.RequestedBy, id, status, v.Error, ts, completed, ts, ts)
			if err != nil {
				return nil, nil, err
			}
		}
		ids = append(ids, id)
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, err
	}
	return &op, ids, nil
}

// RetryTaskOperationTarget creates another execution attempt, preserving the old
// attempt and its outcome. Authorization and safe-retry policy remain the caller's responsibility.
func (s *Store) RetryTaskOperationTarget(ctx context.Context, operationID string, item OperationTask) (int64, error) {
	if item.DNSRun != nil {
		return 0, errors.New("DNS benchmark run retries are not supported")
	}
	if err := validateOperationTask(item); err != nil {
		return 0, err
	}
	if item.Task.Type != model.AgentTaskTypeBenchmarkDNS || item.Task.Status != "pending" {
		return 0, errors.New("only pending DNS diagnostic retries are supported")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var state string
	err = tx.QueryRowContext(ctx, `select state from task_operation_targets where operation_id=? and target_type=? and target_id=? and operation_id not in (select id from task_operations where kind='servers.update')`, operationID, item.Target.Type, item.Target.ID).Scan(&state)
	if err != nil {
		return 0, err
	}
	if state != "failed" && state != "rollback_failed" {
		return 0, errors.New("target has no retryable failed attempt")
	}
	ts := now()
	id, err := operationTaskInsert(ctx, tx, item.Task, ts)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `insert into task_operation_links(operation_id,target_type,target_id,task_id,attempt,state) select ?,?,?,?,coalesce(max(attempt),0)+1,'pending' from task_operation_links where operation_id=? and target_type=? and target_id=?`, operationID, item.Target.Type, item.Target.ID, id, operationID, item.Target.Type, item.Target.ID)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `update task_operation_targets set state='pending',cause_code='',updated_at=? where operation_id=? and target_type=? and target_id=?`, ts, operationID, item.Target.Type, item.Target.ID)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// LinkTaskOperationTarget allows one existing task to satisfy multiple intents.
// It only attaches a first attempt with the same server; revision evidence is not inferred.
func (s *Store) LinkTaskOperationTarget(ctx context.Context, operationID string, target model.TaskOperationTarget, taskID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind string
	if err = tx.QueryRowContext(ctx, `select kind from task_operations where id=?`, operationID).Scan(&kind); err != nil {
		return err
	}
	if kind == "servers.update" {
		return errors.New("configuration intent requires reconciler evidence")
	}
	var serverID int64
	var state, result string
	if err = tx.QueryRowContext(ctx, `select server_id,status,result_json from agent_tasks where id=?`, taskID).Scan(&serverID, &state, &result); err != nil {
		return err
	}
	if target.Type != "server" || target.ID != strconv.FormatInt(serverID, 10) {
		return errors.New("operation target must match task server")
	}
	var outcome struct {
		Superseded bool `json:"superseded"`
	}
	_ = json.Unmarshal([]byte(result), &outcome)
	if state == "succeeded" && outcome.Superseded {
		state = "superseded"
	}
	_, err = tx.ExecContext(ctx, `insert into task_operation_targets(operation_id,target_type,target_id,state,desired_revision,updated_at) values(?,?,?,?,?,?)`, operationID, target.Type, target.ID, state, target.DesiredRevision, now())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `insert into task_operation_links(operation_id,target_type,target_id,task_id,attempt,state) values(?,?,?,?,1,?)`, operationID, target.Type, target.ID, taskID, state)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func operationScope(allowedServerIDs []int64) (string, error) {
	if len(allowedServerIDs) > 10000 {
		return "", errors.New("operation scope too large")
	}
	ids := make([]string, 0, len(allowedServerIDs))
	for _, id := range allowedServerIDs {
		if id > 0 {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
	}
	b, err := json.Marshal(ids)
	return string(b), err
}

const operationVisibleSQL = `not exists (select 1 from task_operation_targets t where t.operation_id=o.id and (t.target_type!='server' or t.target_id not in (select value from json_each(?))))`

// GetTaskOperation returns the full persisted target set, never a page-derived
// aggregate. An empty allowlist denies access. Unknown and forbidden IDs both return sql.ErrNoRows.
func (s *Store) GetTaskOperation(ctx context.Context, id string, allowedServerIDs []int64) (*model.TaskOperation, error) {
	scope, err := operationScope(allowedServerIDs)
	if err != nil {
		return nil, err
	}
	if len(allowedServerIDs) == 0 {
		return nil, sql.ErrNoRows
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var op model.TaskOperation
	var ts string
	err = tx.QueryRowContext(ctx, `select id,kind,source,actor_principal,actor_user_id,created_at from task_operations o where id=? and `+operationVisibleSQL, id, scope).Scan(&op.ID, &op.Kind, &op.Source, &op.ActorPrincipal, &op.ActorUserID, &ts)
	if err != nil {
		return nil, err
	}
	op.CreatedAt = parseTime(ts)
	rows, err := tx.QueryContext(ctx, `select target_type,target_id,state,cause_code,desired_revision,updated_at from task_operation_targets where operation_id=? order by target_type,target_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t model.TaskOperationTarget
		if err = rows.Scan(&t.Type, &t.ID, &t.State, &t.CauseCode, &t.DesiredRevision, &ts); err != nil {
			return nil, err
		}
		t.UpdatedAt = parseTime(ts)
		op.Targets = append(op.Targets, t)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return &op, nil
}

// ListTaskOperationIDs uses a stable keyset, not task pagination. Callers may use
// GetTaskOperation for a selected detail; do not invoke it per row for list rendering.
func (s *Store) ListTaskOperationIDs(ctx context.Context, allowedServerIDs []int64, beforeTime, beforeID string, limit int) ([]string, error) {
	scope, err := operationScope(allowedServerIDs)
	if err != nil {
		return nil, err
	}
	if len(allowedServerIDs) == 0 {
		return []string{}, nil
	}
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("operation page limit must be 1..100")
	}
	rows, err := s.db.QueryContext(ctx, `select id from task_operations o where `+operationVisibleSQL+` and (?='' or (created_at,id)<(?,?)) order by created_at desc,id desc limit ?`, scope, beforeTime, beforeTime, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PruneTaskOperations never deletes tasks. Retain unsettled or live-task-linked
// operations; bound maintenance work to at most 500 intents per call.
func (s *Store) PruneTaskOperations(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit < 1 || limit > 500 {
		return 0, errors.New("operation retention limit must be 1..500")
	}
	res, err := s.db.ExecContext(ctx, `delete from task_operations where id in (select o.id from task_operations o where o.created_at<? and not exists(select 1 from task_operation_targets t where t.operation_id=o.id and t.state not in ('succeeded','failed','rollback_failed','superseded','skipped','unknown','evidence_insufficient','no_change')) and not exists(select 1 from task_operation_links l join agent_tasks a on a.id=l.task_id where l.operation_id=o.id) order by o.created_at,o.id limit ?)`, before.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TaskOperationServerIDs is an internal authorization projection. Callers must
// authorize every returned ID before returning any operation data.
func (s *Store) TaskOperationServerIDs(ctx context.Context, id string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `select target_type,target_id from task_operation_targets where operation_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var kind, raw string
		if err = rows.Scan(&kind, &raw); err != nil {
			return nil, err
		}
		value, parseErr := strconv.ParseInt(raw, 10, 64)
		if kind != "server" || parseErr != nil || value <= 0 {
			return nil, errors.New("unsupported operation target")
		}
		ids = append(ids, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, sql.ErrNoRows
	}
	return ids, nil
}

// ListTaskOperationSummaries aggregates the complete persisted targets before
// returning a bounded page. Task pagination never influences the counts.
func (s *Store) ListTaskOperationSummaries(ctx context.Context, allowedServerIDs []int64, beforeTime, beforeID string, limit int) ([]model.TaskOperationSummary, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("operation page limit must be 1..100")
	}
	scope, err := operationScope(allowedServerIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `with page as (select o.id,o.created_at from task_operations o where `+operationVisibleSQL+` and (?='' or (created_at,id)<(?,?)) order by created_at desc,id desc limit ?)
 select p.id,p.created_at,count(*),sum(t.state in ('pending','queued')),sum(t.state='running'),sum(t.state='succeeded'),sum(t.state in ('failed','rollback_failed')),sum(t.state not in ('pending','queued','running','succeeded','failed','rollback_failed')) from page p join task_operation_targets t on t.operation_id=p.id group by p.id,p.created_at order by p.created_at desc,p.id desc`, scope, beforeTime, beforeTime, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.TaskOperationSummary{}
	for rows.Next() {
		var item model.TaskOperationSummary
		if err = rows.Scan(&item.ID, &item.CreatedAt, &item.Total, &item.Pending, &item.Running, &item.Succeeded, &item.Failed, &item.Unknown); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// AttachTaskOperations batches summaries for already authorized task rows. A
// restricted reader sees an association only when every target is authorized.
func (s *Store) AttachTaskOperations(ctx context.Context, tasks []model.AgentTask, allowedServerIDs []int64, unrestricted bool) error {
	if len(tasks) == 0 {
		return nil
	}
	ids := make([]int64, len(tasks))
	for i, t := range tasks {
		ids[i] = t.ID
	}
	raw, _ := json.Marshal(ids)
	scope, err := operationScope(allowedServerIDs)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `with visible as (
 select distinct o.id from task_operations o join task_operation_links l on l.operation_id=o.id
 where l.task_id in (select value from json_each(?)) and (? or `+operationVisibleSQL+`)
 ), summary as (
 select t.operation_id,count(*) total,
 sum(t.state in ('pending','queued')) pending,sum(t.state='running') running,sum(t.state='succeeded') succeeded,
 sum(t.state in ('failed','rollback_failed')) failed,
 sum(t.state not in ('pending','queued','running','succeeded','failed','rollback_failed')) unknown
 from task_operation_targets t join visible v on v.id=t.operation_id group by t.operation_id
 ) select distinct l.task_id,s.operation_id,s.total,s.pending,s.running,s.succeeded,s.failed,s.unknown
 from task_operation_links l join summary s on s.operation_id=l.operation_id
 where l.task_id in (select value from json_each(?)) order by s.operation_id`, string(raw), unrestricted, scope, string(raw))
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := map[int64][]model.TaskOperationSummary{}
	for rows.Next() {
		var id int64
		var summary model.TaskOperationSummary
		if err = rows.Scan(&id, &summary.ID, &summary.Total, &summary.Pending, &summary.Running, &summary.Succeeded, &summary.Failed, &summary.Unknown); err != nil {
			return err
		}
		byID[id] = append(byID[id], summary)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range tasks {
		tasks[i].Operations = byID[tasks[i].ID]
	}
	return nil
}
