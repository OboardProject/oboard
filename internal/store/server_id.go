package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
)

// Run both on deletion and before allocation: databases may contain references
// to IDs deleted before recycling was introduced. Never widen an empty scope.
func releaseServerReferencesTx(ctx context.Context, tx *sql.Tx, serverID int64) error {
	for _, spec := range []struct {
		table, key, column, suffix, kind string
	}{
		{"api_principals", "id", "resource_filter_json", "", "filter"},
		{"approval_policies", "id", "resource_filter_json", " where mode!='denied'", "filter"},
		{"oauth_grants", "id", "resource_boundary_v2_json", "", "boundary"},
		{"mcp_privileged_grants", "id", "resource_boundary_json", "", "boundary"},
		{"plugin_grants", "instance_id", "grant_json", "", "plugin"},
	} {
		rows, err := tx.QueryContext(ctx, `select `+spec.key+`,`+spec.column+` from `+spec.table+spec.suffix)
		if err != nil {
			return err
		}
		type change struct {
			key   any
			value string
		}
		var changes []change
		for rows.Next() {
			var key any
			var raw string
			if err := rows.Scan(&key, &raw); err != nil {
				rows.Close()
				return err
			}
			if value, changed := removeServerAccess(raw, spec.kind, serverID); changed {
				changes = append(changes, change{key, value})
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, change := range changes {
			extra := ""
			if spec.kind == "plugin" || spec.table == "mcp_privileged_grants" {
				extra = ",revision=revision+1"
			}
			if _, err := tx.ExecContext(ctx, `update `+spec.table+` set `+spec.column+`=?`+extra+` where `+spec.key+`=?`, change.value, change.key); err != nil {
				return err
			}
		}
	}
	// Targets retain their historical ID, but can no longer retry or bind a new
	// deployment to the replacement server. Scoped readers cannot see them.
	for _, table := range []string{"traffic_tail_streams", "device_retirement_targets", "device_retirement_nodes", "configuration_sync_intents"} {
		if _, err := tx.ExecContext(ctx, `delete from `+table+` where server_id=?`, serverID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `update task_operation_targets set state=case when state in ('succeeded','no_change','failed','rollback_failed','unknown') then state else 'superseded' end,cause_code='server_deleted' where target_type='server' and target_id=?`, strconv.FormatInt(serverID, 10))
	return err
}

func removeServerAccess(raw, kind string, serverID int64) (string, bool) {
	var root map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &root) != nil || root == nil {
		return raw, false
	}
	changed := false
	switch kind {
	case "filter":
		if ids, removed := removeServerID(root["server_ids"], serverID); removed {
			root["server_ids"] = ids
			changed = true
		}
		if scope, removed := removeServerSelection(root["servers"], "mode", serverID); removed {
			root["servers"] = scope
			changed = true
		}
	case "boundary":
		var resources map[string]json.RawMessage
		if json.Unmarshal(root["resources"], &resources) == nil {
			if scope, removed := removeServerSelection(resources["server"], "selection", serverID); removed {
				resources["server"] = scope
				root["resources"], _ = json.Marshal(resources)
				changed = true
			}
		}
	case "plugin":
		var capabilities map[string]map[string]json.RawMessage
		if json.Unmarshal(root["capabilities"], &capabilities) == nil {
			for name, scope := range capabilities {
				if ids, removed := removeServerID(scope["servers"], serverID); removed {
					if string(ids) == "[]" {
						delete(capabilities, name)
					} else {
						scope["servers"] = ids
					}
					changed = true
				}
			}
			if changed {
				root["capabilities"], _ = json.Marshal(capabilities)
			}
		}
	}
	if !changed {
		return raw, false
	}
	encoded, _ := json.Marshal(root)
	return string(encoded), true
}

func removeServerSelection(raw json.RawMessage, modeKey string, serverID int64) (json.RawMessage, bool) {
	var scope map[string]json.RawMessage
	if json.Unmarshal(raw, &scope) != nil || scope == nil {
		return raw, false
	}
	ids, removed := removeServerID(scope["ids"], serverID)
	if !removed {
		return raw, false
	}
	scope["ids"] = ids
	// all+include_future is intentionally a grant to newly created servers.
	if string(ids) == "[]" && (string(scope[modeKey]) != `"all"` || (modeKey == "selection" && string(scope["include_future"]) != "true")) {
		scope[modeKey] = json.RawMessage(`"none"`)
	}
	encoded, _ := json.Marshal(scope)
	return encoded, true
}

func removeServerID(raw json.RawMessage, serverID int64) (json.RawMessage, bool) {
	var ids []json.RawMessage
	if json.Unmarshal(raw, &ids) != nil {
		return raw, false
	}
	want := strconv.FormatInt(serverID, 10)
	kept := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		var value string
		if json.Unmarshal(id, &value) != nil {
			value = string(id)
		}
		if value != want {
			kept = append(kept, id)
		}
	}
	if len(kept) == len(ids) {
		return raw, false
	}
	encoded, _ := json.Marshal(kept)
	return encoded, true
}
