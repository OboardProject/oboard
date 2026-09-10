package store

import "context"

const latencyRollupSchemaVersion = 1

func (s *Store) ensureLatencyRollupSchema(ctx context.Context) error {
	statements := []string{
		`create table if not exists latency_rollup_state (
   id integer primary key check(id=1), schema_version integer not null,
   generation integer not null default 1, live_cursor integer not null default -1,
   historical_boundary integer not null default -1, backfill_cursor integer not null default -1,
   expired_unprocessed integer not null default 0, retention_floor integer not null default 0,
   initialized_at text not null default '', updated_at text not null default '')`,
		`insert into latency_rollup_state(id,schema_version) values(1,1) on conflict(id) do nothing`,
		`create table if not exists latency_rollup_buckets (
   server_id integer not null references servers(id) on delete cascade,
   series_key text not null, measurement_revision text not null,
   revision_basis text not null default 'reported_endpoint_v1',
   resolution_seconds integer not null check(resolution_seconds in (300,3600)), bucket_start integer not null,
   kind text not null, task_id integer not null, probe_id text not null,
   mode text not null, task_name text not null, province text not null, carrier text not null,
   latency_sum integer not null, latency_value_count integer not null,
   latency_min integer, latency_max integer, latency_min_at integer, latency_max_at integer,
   curve_sum integer not null, curve_value_count integer not null,
   curve_min integer, curve_max integer, curve_min_at integer, curve_max_at integer,
   attempt_count integer not null, success_count integer not null,
   report_count integer not null, available_report_count integer not null,
   first_sample_at integer not null, last_sample_at integer not null,
   summary_schema_version integer not null, updated_at text not null,
   primary key(server_id,series_key,measurement_revision,resolution_seconds,bucket_start))`,
		`create index if not exists idx_latency_rollup_bucket_time on latency_rollup_buckets(bucket_start)`,
		`create index if not exists idx_latency_rollup_task on latency_rollup_buckets(task_id,server_id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
