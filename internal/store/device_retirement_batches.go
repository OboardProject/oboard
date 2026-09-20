package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

// DeviceRetirementBatch contains no subscription or credential material.
type DeviceRetirementBatch struct {
	ID                int64  `json:"id"`
	State             string `json:"state"`
	Deadline          string `json:"deadline"`
	Actor             string `json:"actor"`
	Reason            string `json:"reason"`
	CreatedAt         string `json:"created_at"`
	RevokedAt         string `json:"revoked_at,omitempty"`
	DeploymentVersion int64  `json:"deployment_version"`
}

func (s *Store) DeviceRetirementBatch(ctx context.Context, id int64) (DeviceRetirementBatch, error) {
	var b DeviceRetirementBatch
	if id == 0 {
		err := s.db.QueryRowContext(ctx, `select coalesce(max(id),0) from device_retirement_batches`).Scan(&id)
		if err != nil || id == 0 {
			return b, err
		}
	}
	err := s.db.QueryRowContext(ctx, `select id,state,deadline,actor,reason,created_at,coalesce(revoked_at,''),deployment_version from device_retirement_batches where id=?`, id).Scan(&b.ID, &b.State, &b.Deadline, &b.Actor, &b.Reason, &b.CreatedAt, &b.RevokedAt, &b.DeploymentVersion)
	return b, err
}

func (s *Store) StartDeviceRetirement(ctx context.Context, actor, reason string, deadline, at time.Time) (DeviceRetirementBatch, error) {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" || len(reason) > 500 || !deadline.After(at) || deadline.After(at.Add(14*24*time.Hour)) {
		return DeviceRetirementBatch{}, errors.New("actor, reason and a future deadline within 14 days are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeviceRetirementBatch{}, err
	}
	defer tx.Rollback()
	var active int
	if err = tx.QueryRowContext(ctx, `select count(*) from device_retirement_batches where state<>'complete'`).Scan(&active); err != nil {
		return DeviceRetirementBatch{}, err
	}
	if active != 0 {
		return DeviceRetirementBatch{}, errors.New("a retirement batch already exists")
	}
	var previous int
	if err = tx.QueryRowContext(ctx, `select count(*) from device_retirement_batches`).Scan(&previous); err != nil {
		return DeviceRetirementBatch{}, err
	}
	if previous != 0 {
		return DeviceRetirementBatch{}, errors.New("retirement has already completed; its deadline cannot be renewed")
	}
	var grace string
	if err := tx.QueryRowContext(ctx, `select coalesce(min(deadline),'') from device_retirement_initial_grace`).Scan(&grace); err != nil {
		return DeviceRetirementBatch{}, err
	}
	if grace != "" {
		end, err := time.Parse(time.RFC3339Nano, grace)
		if err != nil {
			return DeviceRetirementBatch{}, err
		}
		if end.Before(deadline) {
			deadline = end
		}
	}
	result, err := tx.ExecContext(ctx, `insert into device_retirement_batches(state,deadline,actor,reason,created_at) values('review',?,?,?,?)`, deadline.UTC().Format(time.RFC3339Nano), actor, reason, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return DeviceRetirementBatch{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return DeviceRetirementBatch{}, err
	}
	_, err = tx.ExecContext(ctx, `insert into device_retirement_accounts(batch_id,user_id,decision) select ?,user_id,'pending' from (select user_id from user_devices union select user_id from proxy_credentials where device_id_hash<>'')`, id)
	if err != nil {
		return DeviceRetirementBatch{}, err
	}
	// All enrolled nodes are included: the current topology cannot prove where historical credentials were installed.
	_, err = tx.ExecContext(ctx, `insert into device_retirement_nodes(batch_id,server_id) select ?,id from servers where coalesce(agent_id,'')<>''`, id)
	if err != nil {
		return DeviceRetirementBatch{}, err
	}
	if err = tx.Commit(); err != nil {
		return DeviceRetirementBatch{}, err
	}
	return s.DeviceRetirementBatch(ctx, id)
}

// Review accepts an explicit administrator decision, not a device-token claim.
// retain_restriction keeps the account issuance hold; account_authorized only
// releases that hold, never creates an authorization or changes user status.
func (s *Store) ReviewDeviceRetirement(ctx context.Context, id, userID int64, decision, reason string) error {
	if (decision != "account_authorized" && decision != "retain_restriction") || strings.TrimSpace(reason) == "" || len(reason) > 500 {
		return errors.New("explicit decision and reason are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `update device_retirement_accounts set decision=?,reason=? where batch_id=? and user_id=? and exists(select 1 from device_retirement_batches where id=? and state='review')`, decision, reason, id, userID, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("account is not awaiting review")
	}
	if _, err = tx.ExecContext(ctx, `delete from device_retirement_scopes where batch_id=? and user_id=?`, id, userID); err != nil {
		return err
	}
	if decision == "account_authorized" {
		if _, err = tx.ExecContext(ctx, `insert into device_retirement_scopes(batch_id,user_id,credential_id) select ?,user_id,id from proxy_credentials where user_id=? and device_id_hash<>'' and status='active'`, id, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) BeginDeviceRetirementTransition(ctx context.Context, id int64, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var deadline string
	var pending int
	if err = tx.QueryRowContext(ctx, `select deadline from device_retirement_batches where id=? and state='review'`, id).Scan(&deadline); err != nil {
		return err
	}
	end, err := time.Parse(time.RFC3339Nano, deadline)
	if err != nil {
		return err
	}
	if !end.After(at) {
		return errors.New("transition deadline has expired")
	}
	if err = tx.QueryRowContext(ctx, `select count(*) from device_retirement_accounts where batch_id=? and decision='pending'`, id).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return errors.New("all accounts require explicit review")
	}
	if _, err = tx.ExecContext(ctx, `delete from device_retirement_reviews where user_id in(select user_id from device_retirement_accounts where batch_id=? and decision='account_authorized')`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `insert into device_retirement_reviews(user_id,reason_code,created_at) select user_id,'administrator_retained_restriction',? from device_retirement_accounts where batch_id=? and decision='retain_restriction' on conflict(user_id) do update set reason_code=excluded.reason_code`, at.UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update device_retirement_batches set state='transition' where id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update routing_cache_revision set revision=revision+1 where id=1; update configuration_revision set revision=revision+1 where id=1`); err != nil {
		return err
	}
	return tx.Commit()
}

// Revoke is irreversible. It never restores a tombstone or modifies account restrictions.
func (s *Store) RevokeDeviceRetirement(ctx context.Context, id int64, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRowContext(ctx, `select state from device_retirement_batches where id=?`, id).Scan(&state); err != nil {
		return err
	}
	if state == "credential_revoking" || state == "awaiting_node_confirmation" || state == "complete" {
		return nil
	}
	if state != "transition" {
		return errors.New("batch is not in transition")
	}
	stamp := at.UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `insert into device_retirement_nodes(batch_id,server_id) select ?,id from servers where coalesce(agent_id,'')<>'' on conflict do nothing`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update proxy_credentials set status='revoked',material_encrypted='',revoked_at=? where device_id_hash<>'' and user_id in(select user_id from device_retirement_accounts where batch_id=?)`, stamp, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `insert into device_retirement_targets(batch_id,server_id,authorization_revision,users_revision)
 select n.batch_id,n.server_id,case when a.server_id is null then 0 else a.desired_revision+1 end,case when u.server_id is null then 0 else u.desired_revision+1 end
 from device_retirement_nodes n left join authorization_states a on a.server_id=n.server_id left join runtime_user_states u on u.server_id=n.server_id where n.batch_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update authorization_states set desired_digest='',evaluated_routing_revision=0,confirmed_revision=0,confirmed_digest='',confirmed_at=null where server_id in(select server_id from device_retirement_nodes where batch_id=?);
 update runtime_user_states set desired_digest='',evaluated_routing_revision=0,confirmed_revision=0,confirmed_digest='',confirmed_at=null where server_id in(select server_id from device_retirement_nodes where batch_id=?);
 update routing_cache_revision set revision=revision+1 where id=1;
 update configuration_revision set revision=revision+1 where id=1`, id, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update device_retirement_batches set state='credential_revoking',revoked_at=? where id=?`, stamp, id); err != nil {
		return err
	}
	return tx.Commit()
}

// BindDeployment accepts only a version produced by the Controller's signed
// full-deployment workflow. Callers must not take a version from HTTP input.
func (s *Store) BindDeviceRetirementDeployment(ctx context.Context, id, version int64) error {
	if version <= 0 {
		return errors.New("positive deployment version required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `update device_retirement_batches set deployment_version=?,state='awaiting_node_confirmation' where id=? and state in ('credential_revoking','awaiting_node_confirmation') and deployment_version<?`, version, id, version)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("batch is not awaiting deployment")
	}
	if _, err = tx.ExecContext(ctx, `insert into device_retirement_nodes(batch_id,server_id) select ?,id from servers where coalesce(agent_id,'')<>'' on conflict do nothing`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `insert into device_retirement_targets(batch_id,server_id,authorization_revision,users_revision) select n.batch_id,n.server_id,coalesce(a.desired_revision,0),coalesce(u.desired_revision,0) from device_retirement_nodes n left join authorization_states a on a.server_id=n.server_id left join runtime_user_states u on u.server_id=n.server_id where n.batch_id=? on conflict do nothing`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinalizeDeviceRetirement(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var version int64
	var revoked string
	if err = tx.QueryRowContext(ctx, `select state,deployment_version,coalesce(revoked_at,'') from device_retirement_batches where id=?`, id).Scan(&state, &version, &revoked); err != nil {
		return err
	}
	if state == "complete" {
		return nil
	}
	if state != "awaiting_node_confirmation" || version <= 0 {
		return errors.New("node confirmation has not started")
	}
	var pending int
	// A desired-state write or semantic no-op is not runtime evidence. Nodes
	// lacking either acknowledgement lane remain pending until upgraded.
	err = tx.QueryRowContext(ctx, `select count(*) from device_retirement_nodes n
 left join servers s on s.id=n.server_id
 left join device_retirement_targets t on t.batch_id=n.batch_id and t.server_id=n.server_id
 left join configuration_sync_states c on c.server_id=n.server_id
 left join authorization_states a on a.server_id=n.server_id
 left join runtime_user_states u on u.server_id=n.server_id
 where n.batch_id=? and (t.server_id is null or s.id is null or s.status<>'online'
 or a.server_id is null or u.server_id is null or a.confirmed_revision<=0 or u.confirmed_revision<=0
 or not exists(select 1 from agent_tasks task where task.server_id=n.server_id and task.type='apply_deployment' and task.status='succeeded' and task.config_version=c.last_config_version) or coalesce(a.confirmed_revision,0)<t.authorization_revision or coalesce(u.confirmed_revision,0)<t.users_revision or coalesce(c.state,'')<>'synced' or coalesce(c.last_config_version,0)<?
 or (a.server_id is not null and (a.confirmed_revision<a.desired_revision or a.confirmed_digest<>a.desired_digest or coalesce(a.confirmed_at,'')<?))
 or (u.server_id is not null and (u.confirmed_revision<u.desired_revision or u.confirmed_digest<>u.desired_digest or coalesce(u.confirmed_at,'')<?)))`, id, version, revoked, revoked).Scan(&pending)
	if err != nil {
		return err
	}
	if pending != 0 {
		return errors.New("node revocation confirmation is incomplete")
	}
	if err = tx.QueryRowContext(ctx, `select count(*) from servers s where coalesce(s.agent_id,'')<>'' and not exists(select 1 from device_retirement_nodes n where n.batch_id=? and n.server_id=s.id)`, id).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return errors.New("fleet changed after revocation; a new signed deployment is required")
	}
	if _, err = tx.ExecContext(ctx, `update device_retirement_batches set state='complete' where id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Destructive contraction remains unavailable while direct upgrades/restores
// can recreate device state. Completing revocation is not a DROP-table gate.
func (s *Store) ContractDeviceRetirement(context.Context, int64) error {
	return errors.New("contraction blocked: oldest direct-upgrade and backup-restore schema boundaries are not enforced; retain read-only history")
}
