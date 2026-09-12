package store

import (
	"context"
	"database/sql"

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

// LatestSSHDeploymentTaskTermination reports the version boundary of the newest
// SSH deployment task without transferring its payload. The payload of an
// apply_deployment task carries the whole generated kernel configuration, so
// callers that only need the version and status must not read the row through
// LatestSSHDeploymentTask.
func (s *Store) LatestSSHDeploymentTaskTermination(ctx context.Context, serverID int64) (int64, string, error) {
	var configVersion int64
	var status string
	err := s.db.QueryRowContext(ctx, `select config_version,status from agent_tasks where server_id=? and type in (?,?) and json_type(payload_json,'$.ssh_inbounds')='object' order by config_version desc,id desc limit 1`, serverID, model.AgentTaskTypeApplyDeployment, model.AgentTaskTypeApplyCoreConfig).Scan(&configVersion, &status)
	if err != nil {
		return 0, "", err
	}
	return configVersion, status, nil
}

func sshDeploymentWriteCurrent(ctx context.Context, tx *sql.Tx, serverID, version, taskID int64) (bool, error) {
	var latestID, latestVersion int64
	err := tx.QueryRowContext(ctx, `select id,config_version from agent_tasks where server_id=? and type in (?,?) and json_type(payload_json,'$.ssh_inbounds')='object' order by config_version desc,id desc limit 1`, serverID, model.AgentTaskTypeApplyDeployment, model.AgentTaskTypeApplyCoreConfig).Scan(&latestID, &latestVersion)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return latestVersion < version || latestVersion == version && (taskID == 0 || latestID <= taskID), nil
}
