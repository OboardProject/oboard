package store

import "context"

type DeviceRetirementAccount struct {
	UserID   int64  `json:"user_id"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}
type DeviceRetirementNode struct {
	ServerID                    int64  `json:"server_id"`
	TargetConfigVersion         int64  `json:"target_config_version"`
	TargetAuthorizationRevision int64  `json:"target_authorization_revision"`
	TargetUsersRevision         int64  `json:"target_users_revision"`
	Status                      string `json:"status"`
	ConfigurationState          string `json:"configuration_state"`
	ConfigVersion               int64  `json:"config_version"`
	AuthorizationConfirmed      bool   `json:"authorization_confirmed"`
	UsersConfirmed              bool   `json:"users_confirmed"`
}

// Paged reads are suitable for large fleets and do not compute or mutate state.
func (s *Store) DeviceRetirementAccounts(ctx context.Context, id int64, offset int) ([]DeviceRetirementAccount, error) {
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `select user_id,decision,reason from device_retirement_accounts where batch_id=? order by user_id limit 100 offset ?`, id, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeviceRetirementAccount{}
	for rows.Next() {
		var item DeviceRetirementAccount
		if err := rows.Scan(&item.UserID, &item.Decision, &item.Reason); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Store) DeviceRetirementNodes(ctx context.Context, id int64, offset int) ([]DeviceRetirementNode, error) {
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `select n.server_id,b.deployment_version,coalesce(t.authorization_revision,0),coalesce(t.users_revision,0),coalesce(s.status,'missing'),coalesce(c.state,'missing'),coalesce(c.last_config_version,0),
 case when a.server_id is null then 0 else a.confirmed_revision>=max(a.desired_revision,coalesce(t.authorization_revision,0)) and a.confirmed_digest=a.desired_digest and coalesce(a.confirmed_at,'')>=coalesce(b.revoked_at,'~') end,
 case when u.server_id is null then 0 else u.confirmed_revision>=max(u.desired_revision,coalesce(t.users_revision,0)) and u.confirmed_digest=u.desired_digest and coalesce(u.confirmed_at,'')>=coalesce(b.revoked_at,'~') end
 from device_retirement_nodes n join device_retirement_batches b on b.id=n.batch_id left join device_retirement_targets t on t.batch_id=n.batch_id and t.server_id=n.server_id left join servers s on s.id=n.server_id left join configuration_sync_states c on c.server_id=n.server_id left join authorization_states a on a.server_id=n.server_id left join runtime_user_states u on u.server_id=n.server_id where n.batch_id=? order by n.server_id limit 100 offset ?`, id, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeviceRetirementNode{}
	for rows.Next() {
		var item DeviceRetirementNode
		if err := rows.Scan(&item.ServerID, &item.TargetConfigVersion, &item.TargetAuthorizationRevision, &item.TargetUsersRevision, &item.Status, &item.ConfigurationState, &item.ConfigVersion, &item.AuthorizationConfirmed, &item.UsersConfirmed); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
