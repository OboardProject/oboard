package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/OboardProject/oboard/internal/model"
)

// ServerConfigurationIntent is supplied only by authenticated management adapters.
// Fields are request field names, never a caller-selected operation identity.
type ServerConfigurationIntent struct {
	ActorPrincipal string
	ActorUserID    *int64
	Source         string
	Fields         []string
	OperationID    string
}

func (s *Store) initConfigurationOperations(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
create table if not exists configuration_operation_fields (
 operation_id text not null references task_operations(id) on delete cascade,
 field text not null, value_hash text not null,
 primary key(operation_id,field)
);
create table if not exists configuration_operation_attempts (
 operation_id text not null, target_type text not null, target_id text not null, attempt integer not null,
 config_version integer not null, actual_revision integer not null, execution_kind text not null,
 primary key(operation_id,target_type,target_id,attempt),
 foreign key(operation_id,target_type,target_id,attempt) references task_operation_links(operation_id,target_type,target_id,attempt) on delete cascade
);
create index if not exists configuration_operation_attempts_version on configuration_operation_attempts(target_id,config_version,actual_revision);
create trigger if not exists configuration_operations_sync_result after update of state on configuration_sync_states
when old.state in ('queued','running') and new.state in ('synced','failed') and new.sync_strategy!='semantic_noop'
 and new.last_config_version=old.last_config_version and new.wanted_revision=old.wanted_revision
begin
 update task_operation_links set state=case when new.state='synced' then 'succeeded' else 'failed' end
 where (operation_id,target_type,target_id,attempt) in
 (select operation_id,target_type,target_id,attempt from configuration_operation_attempts
 where target_id=cast(new.server_id as text) and config_version=new.last_config_version and actual_revision=new.wanted_revision);
 update task_operation_targets set state=case when new.state='synced' and cause_code='partial_scope' then 'evidence_insufficient' else (select state from task_operation_links l where l.operation_id=task_operation_targets.operation_id and l.target_type=task_operation_targets.target_type and l.target_id=task_operation_targets.target_id order by attempt desc limit 1) end,updated_at=new.updated_at
 where state!='superseded' and (operation_id,target_type,target_id) in
 (select a.operation_id,a.target_type,a.target_id from configuration_operation_attempts a where a.target_id=cast(new.server_id as text) and a.config_version=new.last_config_version and a.actual_revision=new.wanted_revision
 and a.attempt=(select max(l.attempt) from task_operation_links l where l.operation_id=a.operation_id and l.target_type=a.target_type and l.target_id=a.target_id));
end;
create trigger if not exists configuration_operations_preparation_failure after update of state on configuration_sync_states
when old.state='preparing' and new.state='failed'
begin
 update task_operation_targets set state='failed',updated_at=new.updated_at
 where target_type='server' and target_id=cast(new.server_id as text) and desired_revision<=new.wanted_revision
 and state in ('pending','queued','evidence_insufficient')
 and operation_id in (select id from task_operations where kind='servers.update');
end;
create trigger if not exists configuration_operations_superseded after update of state on configuration_sync_states
when old.state in ('queued','running') and new.state='pending' and new.trigger_reason='superseded'
begin
 update task_operation_links set state='superseded' where (operation_id,target_type,target_id,attempt) in
 (select operation_id,target_type,target_id,attempt from configuration_operation_attempts
 where target_id=cast(new.server_id as text) and config_version=old.last_config_version and actual_revision=old.wanted_revision);
 update task_operation_targets set state='evidence_insufficient',updated_at=new.updated_at
 where state='queued' and (operation_id,target_type,target_id) in
 (select operation_id,target_type,target_id from configuration_operation_attempts
 where target_id=cast(new.server_id as text) and config_version=old.last_config_version and actual_revision=old.wanted_revision);
end;
create trigger if not exists configuration_operations_semantic_noop after update of state on configuration_sync_states
when new.state='synced' and new.sync_strategy='semantic_noop'
begin
 update task_operation_targets set state='evidence_insufficient',updated_at=new.updated_at
 where target_type='server' and target_id=cast(new.server_id as text) and desired_revision<=new.wanted_revision
 and state in ('pending','queued','failed','evidence_insufficient')
 and operation_id in (select id from task_operations where kind='servers.update');
end;`)
	return err
}

func configurationFieldHashes(v *model.Server) map[string]string {
	values := map[string]string{"ip_stack": string(v.IPStack), "listen_mode": string(v.ListenMode), "listen_ip": v.ListenIP, "udp_inbound_mode": string(v.UDPInboundMode)}
	hashes := make(map[string]string, len(values))
	for k, v := range values {
		sum := sha256.Sum256([]byte(v))
		hashes[k] = hex.EncodeToString(sum[:])
	}
	return hashes
}

func recordServerConfigurationIntent(ctx context.Context, tx *sql.Tx, v, previous *model.Server, intent *ServerConfigurationIntent) (string, error) {
	if intent == nil {
		return "", nil
	}
	if intent.ActorPrincipal == "" || intent.Source == "" || len(intent.Fields) == 0 {
		return "", errors.New("configuration intent requires authenticated actor and fields")
	}
	var revision uint64
	if err := tx.QueryRowContext(ctx, `select revision from configuration_revision where id=1`).Scan(&revision); err != nil {
		return "", err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(token[:])
	ts := now()
	if _, err := tx.ExecContext(ctx, `insert into task_operations(id,kind,source,actor_principal,actor_user_id,created_at) values(?,'servers.update',?,?,?,?)`, id, intent.Source, intent.ActorPrincipal, intent.ActorUserID, ts); err != nil {
		return "", err
	}
	values := configurationFieldHashes(v)
	tracked := map[string]string{}
	partial := false
	for _, field := range intent.Fields {
		if value, ok := values[field]; ok {
			tracked[field] = value
		} else {
			partial = true
		}
	}
	state, cause := "pending", "tracked_fields_only"
	if len(tracked) == 0 {
		state, cause = "evidence_insufficient", "local_saved"
	} else if partial {
		cause = "partial_scope"
	}
	if len(tracked) > 0 && previous != nil {
		old := configurationFieldHashes(previous)
		changed := false
		for field, value := range tracked {
			changed = changed || old[field] != value
		}
		if !changed {
			state = "evidence_insufficient"
			if !partial {
				confirmed := true
				for field, value := range tracked {
					var found bool
					// A topology-only no-op or a later untracked revision cannot
					// extend this evidence to settings that were never delivered.
					err := tx.QueryRowContext(ctx, `select exists(
 select 1 from configuration_operation_fields f
 join task_operation_targets t on t.operation_id=f.operation_id
 join configuration_operation_attempts a on a.operation_id=t.operation_id and a.target_type=t.target_type and a.target_id=t.target_id
 join task_operation_links l on l.operation_id=a.operation_id and l.target_type=a.target_type and l.target_id=a.target_id and l.attempt=a.attempt
 join configuration_sync_states s on cast(s.server_id as text)=a.target_id
 where t.target_type='server' and t.target_id=? and t.state='succeeded'
 and f.field=? and f.value_hash=? and l.state='succeeded'
 and a.actual_revision=? and s.wanted_revision=a.actual_revision
 and s.state='synced' and s.last_config_version=a.config_version
)`, fmt.Sprint(v.ID), field, value, revision).Scan(&found)
					if err != nil {
						return "", err
					}
					confirmed = confirmed && found
				}
				if confirmed {
					state, cause = "no_change", "confirmed_fields"
				}
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into task_operation_targets(operation_id,target_type,target_id,state,cause_code,desired_revision,updated_at) values(?,'server',?,?,?,?,?)`, id, fmt.Sprint(v.ID), state, cause, revision, ts); err != nil {
		return "", err
	}
	for field, value := range tracked {
		// Only an explicitly conflicting value supersedes an earlier intent. Unrelated
		// configuration revisions are not evidence of replacement.
		if _, err := tx.ExecContext(ctx, `update task_operation_targets set state='superseded',updated_at=? where target_type='server' and target_id=? and state not in ('succeeded','no_change','superseded') and operation_id in (select operation_id from configuration_operation_fields where field=? and value_hash!=?)`, ts, fmt.Sprint(v.ID), field, value); err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `insert into configuration_operation_fields(operation_id,field,value_hash) values(?,?,?)`, id, field, value); err != nil {
			return "", err
		}
	}
	return id, nil
}

// Bind only a fresh projection at exactly the revision still present in SQLite.
// Reusing an older task or a topology-only no-op cannot prove field delivery.
func bindConfigurationOperations(ctx context.Context, tx *sql.Tx, serverID int64, revision uint64, version, taskID int64, fresh bool, previousTaskID int64) error {
	if !fresh && previousTaskID <= 0 {
		return nil
	}
	var current uint64
	if err := tx.QueryRowContext(ctx, `select revision from configuration_revision where id=1`).Scan(&current); err != nil {
		return err
	}
	if current != revision {
		if !fresh {
			return errors.New("configuration changed while binding retry evidence")
		}
		return nil
	}
	var v model.Server
	if err := tx.QueryRowContext(ctx, `select ip_stack,listen_mode,listen_ip,udp_inbound_mode from servers where id=?`, serverID).Scan(&v.IPStack, &v.ListenMode, &v.ListenIP, &v.UDPInboundMode); err != nil {
		return err
	}
	if !fresh {
		var identical bool
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from agent_tasks old join agent_tasks next on next.server_id=old.server_id
 where old.id=? and next.id=? and old.server_id=? and old.type='apply_deployment' and next.type=old.type
 and old.status in ('failed','rollback_failed') and old.payload_json=next.payload_json and next.config_version=?)`, previousTaskID, taskID, serverID, version).Scan(&identical); err != nil {
			return err
		}
		if !identical {
			return errors.New("configuration retry must copy the failed source deployment")
		}
	}
	hashes := configurationFieldHashes(&v)
	rows, err := tx.QueryContext(ctx, `select t.operation_id,t.desired_revision,f.field,f.value_hash from task_operation_targets t join configuration_operation_fields f on f.operation_id=t.operation_id where t.target_type='server' and t.target_id=? and t.desired_revision<=? and t.state in ('pending','failed','queued','evidence_insufficient')
 and (? or exists(select 1 from configuration_operation_attempts a join task_operation_links l
 on l.operation_id=a.operation_id and l.target_type=a.target_type and l.target_id=a.target_id and l.attempt=a.attempt
 where a.operation_id=t.operation_id and a.target_type=t.target_type and a.target_id=t.target_id
 and l.task_id=? and a.actual_revision=?))`, fmt.Sprint(serverID), revision, fresh, previousTaskID, revision)
	if err != nil {
		return err
	}
	type candidate struct {
		revision uint64
		matches  bool
	}
	candidates := map[string]candidate{}
	for rows.Next() {
		var id, field, hash string
		var r uint64
		if err = rows.Scan(&id, &r, &field, &hash); err != nil {
			rows.Close()
			return err
		}
		c, ok := candidates[id]
		if !ok {
			c = candidate{r, true}
		}
		c.matches = c.matches && hashes[field] == hash
		candidates[id] = c
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for id, c := range candidates {
		if !c.matches {
			continue
		}
		result, err := tx.ExecContext(ctx, `insert or ignore into task_operation_links(operation_id,target_type,target_id,task_id,attempt,applied_revision,state) select ?,'server',?,?,coalesce(max(attempt),0)+1,?,'queued' from task_operation_links where operation_id=? and target_type='server' and target_id=?`, id, fmt.Sprint(serverID), taskID, revision, id, fmt.Sprint(serverID))
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		kind := "original_apply"
		var attempt int
		if err = tx.QueryRowContext(ctx, `select attempt from task_operation_links where operation_id=? and task_id=?`, id, taskID).Scan(&attempt); err != nil {
			return err
		}
		if attempt > 1 {
			kind = "original_retry"
		}
		if c.revision != revision {
			kind = "later_modified"
		}
		if _, err = tx.ExecContext(ctx, `insert into configuration_operation_attempts(operation_id,target_type,target_id,attempt,config_version,actual_revision,execution_kind) select operation_id,target_type,target_id,attempt,?,?,? from task_operation_links where operation_id=? and task_id=?`, version, revision, kind, id, taskID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `update task_operation_targets set state='queued',updated_at=? where operation_id=? and target_type='server' and target_id=? and state!='superseded'`, now(), id, fmt.Sprint(serverID)); err != nil {
			return err
		}
	}
	return nil
}
