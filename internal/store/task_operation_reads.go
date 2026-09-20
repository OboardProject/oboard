package store

import (
	"context"
	"errors"
	"github.com/OboardProject/oboard/internal/model"
)

// ListTaskOperationRecords pages intents first, then reads every target in one
// query. Authorization covers the whole intent, including deleted servers.
func (s *Store) ListTaskOperationRecords(ctx context.Context, allowed []int64, unrestricted bool, id, beforeTime, beforeID string, limit int) ([]model.TaskOperation, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("operation page limit must be 1..100")
	}
	scope, err := operationScope(allowed)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `with page as (
 select o.* from task_operations o where (? or `+operationVisibleSQL+`) and (?='' or id=?) and (?='' or (created_at,id)<(?,?)) order by created_at desc,id desc limit ?)
 select p.id,p.kind,p.source,p.actor_principal,p.actor_user_id,p.created_at,t.target_type,t.target_id,t.state,t.cause_code,t.desired_revision,t.updated_at
 from page p join task_operation_targets t on t.operation_id=p.id order by p.created_at desc,p.id desc,t.target_type,t.target_id`, unrestricted, scope, id, id, beforeTime, beforeTime, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.TaskOperation{}
	for rows.Next() {
		var op model.TaskOperation
		var target model.TaskOperationTarget
		var created, updated string
		if err := rows.Scan(&op.ID, &op.Kind, &op.Source, &op.ActorPrincipal, &op.ActorUserID, &created, &target.Type, &target.ID, &target.State, &target.CauseCode, &target.DesiredRevision, &updated); err != nil {
			return nil, err
		}
		op.CreatedAt = parseTime(created)
		target.UpdatedAt = parseTime(updated)
		if len(result) == 0 || result[len(result)-1].ID != op.ID {
			result = append(result, op)
		}
		result[len(result)-1].Targets = append(result[len(result)-1].Targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if id != "" && len(result) == 1 {
		// Details load all attempts in one join, never one query per target.
		// Recheck the whole-operation scope, including deleted targets.
		attempts, err := s.db.QueryContext(ctx, `select l.target_type,l.target_id,task.id,l.attempt,a.config_version,a.actual_revision,
 coalesce(a.execution_kind,case when l.attempt>1 then 'original_retry' else 'original_apply' end),l.state
 from task_operations o join task_operation_links l on l.operation_id=o.id
 left join agent_tasks task on task.id=l.task_id
 left join configuration_operation_attempts a on a.operation_id=l.operation_id and a.target_type=l.target_type and a.target_id=l.target_id and a.attempt=l.attempt
 where o.id=? and (? or `+operationVisibleSQL+`) order by l.target_type,l.target_id,l.attempt`, id, unrestricted, scope)
		if err != nil {
			return nil, err
		}
		defer attempts.Close()
		for attempts.Next() {
			var attempt model.TaskOperationAttempt
			if err := attempts.Scan(&attempt.TargetType, &attempt.TargetID, &attempt.TaskID, &attempt.Attempt, &attempt.ConfigVersion, &attempt.ActualRevision, &attempt.ExecutionKind, &attempt.State); err != nil {
				return nil, err
			}
			result[0].Attempts = append(result[0].Attempts, attempt)
		}
		if err := attempts.Err(); err != nil {
			return nil, err
		}
	}
	return result, nil
}
