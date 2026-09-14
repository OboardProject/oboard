package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/OboardProject/oboard/internal/model"
)

// Failed and pending tasks remain a version boundary even after SSH state is
// cleared, so a late result cannot reinstate an older credential deployment.
func (s *Store) LatestSSHDeploymentTask(ctx context.Context, serverID int64) (*model.AgentTask, error) {
	rows, err := s.db.QueryContext(ctx, `select id,server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at from agent_tasks where server_id=? and type in (?,?) and json_type(payload_json,'$.ssh_inbounds')='object' order by config_version desc,id desc limit 1`, serverID, model.AgentTaskTypeApplyDeployment, model.AgentTaskTypeApplyCoreConfig)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, sql.ErrNoRows
	}
	return &tasks[0], nil
}

// sshDeploymentTaskTypes are the task types whose payload can carry an
// ssh_inbounds plan.
//
// The two queries below run once per type rather than once with "type in
// (?,?)". With two types matched, SQLite cannot use the
// (server_id, type, config_version desc) index for ordering, so it collected
// every deployment task for the server, evaluated json_type on each payload -
// the whole generated kernel configuration - and only then sorted to apply the
// limit. On a production Controller that was 4,325 payloads parsed per call, at
// 106 ms, on a panel read path. Pinned to one type the index supplies the
// order, so the walk stops at the first row that carries the plan: the same two
// answers came back in 0.13 ms and 0.11 ms.
var sshDeploymentTaskTypes = [...]string{model.AgentTaskTypeApplyDeployment, model.AgentTaskTypeApplyCoreConfig}

// LatestSSHDeploymentTaskTermination reports the version boundary of the newest
// SSH deployment task without transferring its payload. The payload of an
// apply_deployment task carries the whole generated kernel configuration, so
// callers that only need the version and status must not read the row through
// LatestSSHDeploymentTask.
func (s *Store) LatestSSHDeploymentTaskTermination(ctx context.Context, serverID int64) (int64, string, error) {
	var configVersion, id int64
	var status string
	found := false
	for _, taskType := range sshDeploymentTaskTypes {
		var candidateVersion, candidateID int64
		var candidateStatus string
		err := s.db.QueryRowContext(ctx, `select config_version,id,status from agent_tasks
			where server_id=? and type=? and json_type(payload_json,'$.ssh_inbounds')='object'
			order by config_version desc,id desc limit 1`, serverID, taskType).Scan(&candidateVersion, &candidateID, &candidateStatus)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, "", err
		}
		if !found || candidateVersion > configVersion || (candidateVersion == configVersion && candidateID > id) {
			configVersion, id, status, found = candidateVersion, candidateID, candidateStatus, true
		}
	}
	if !found {
		return 0, "", sql.ErrNoRows
	}
	return configVersion, status, nil
}

func sshDeploymentWriteCurrent(ctx context.Context, tx *sql.Tx, serverID, version, taskID int64) (bool, error) {
	var latestID, latestVersion int64
	found := false
	for _, taskType := range sshDeploymentTaskTypes {
		var candidateID, candidateVersion int64
		err := tx.QueryRowContext(ctx, `select id,config_version from agent_tasks
			where server_id=? and type=? and json_type(payload_json,'$.ssh_inbounds')='object'
			order by config_version desc,id desc limit 1`, serverID, taskType).Scan(&candidateID, &candidateVersion)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		if !found || candidateVersion > latestVersion || (candidateVersion == latestVersion && candidateID > latestID) {
			latestID, latestVersion, found = candidateID, candidateVersion, true
		}
	}
	if !found {
		return true, nil
	}
	return latestVersion < version || latestVersion == version && (taskID == 0 || latestID <= taskID), nil
}
