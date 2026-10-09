package store

import "context"

// Keep retired columns empty for databases that can still be restored from an
// older schema. No current report model reads or writes these columns.
func (s *Store) retireAuditDestinations(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`delete from audit_incidents where latest_snapshot_id in (select id from audit_feature_snapshots
		 where coalesce(json_extract(features_json,'$.destination_count'),0)>0
		 or coalesce(json_extract(features_json,'$.destination_port_count'),0)>0)`,
		`delete from audit_feature_snapshots where coalesce(json_extract(features_json,'$.destination_count'),0)>0
		 or coalesce(json_extract(features_json,'$.destination_port_count'),0)>0`,
		`update audit_feature_snapshots set features_json=json_remove(features_json,'$.destination_count','$.destination_port_count')
		 where json_type(features_json,'$.destination_count') is not null or json_type(features_json,'$.destination_port_count') is not null`,
		`update connection_audit_reports set destination='',destination_port=0,outbound_tag='',outbound_type=''
		 where destination<>'' or destination_port<>0 or outbound_tag<>'' or outbound_type<>''`,
		// AI output may quote its input in free text, so remove the affected
		// review together with its evidence and jobs instead of masking JSON keys.
		`delete from ai_audit_reviews where id in (
		 select r.id from ai_audit_reviews r,json_each(r.evidence_types_json) j where j.value='destination'
		 union select e.review_id from ai_audit_review_evidence e,json_tree(e.payload_json) j
		 where j.key in ('destination','destination_port','outbound_tag','outbound_type','destinations','connection_destinations','destination_count')
		 and j.value not in ('',0,'[]','null')
		 union select q.review_id from ai_audit_review_jobs q,json_tree(q.input_json) j
		 where j.key in ('destination','destination_port','outbound_tag','outbound_type','destinations','connection_destinations','destination_count')
		 and j.value not in ('',0,'[]','null')
		)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
