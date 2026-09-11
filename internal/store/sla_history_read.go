package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/OboardProject/oboard/internal/model"
	"sort"
	"time"
)

type SLAHistoryRead struct {
	Parts           []SLAProjectionBucket
	Source          string
	ObservedThrough *time.Time
}

func (s *Store) QuerySLAHistory(ctx context.Context, id int64, from, to time.Time, resolution time.Duration, build func(SLAProjectionWork) (SLAProjectionOutput, error)) (SLAHistoryRead, error) {
	result := SLAHistoryRead{Parts: []SLAProjectionBucket{}, Source: "raw_bounded"}
	if id <= 0 || !to.After(from) || to.Sub(from) > 30*24*time.Hour || resolution < 5*time.Minute {
		return result, ErrHistoryCoverage
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return result, err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	var cursor, frontier, coverage, floor int64
	var dirty sql.NullInt64
	var version int
	err = conn.QueryRowContext(ctx, `select s.event_cursor,q.frontier,q.coverage_from,s.retention_floor,q.dirty_from,s.algorithm_version from sla_projection_state s join sla_projection_servers q on q.server_id=? where s.id=1`, id).Scan(&cursor, &frontier, &coverage, &floor, &dirty, &version)
	useSummary := err == nil && version == SLAProjectionVersion && (!dirty.Valid || dirty.Int64 >= to.Unix())
	if err != nil && err != sql.ErrNoRows {
		return result, err
	}
	var newest int64
	if err := conn.QueryRowContext(ctx, `select coalesce(max(id),0) from server_connectivity_events`).Scan(&newest); err != nil {
		return result, err
	}
	if useSummary && newest > cursor {
		rows, err := conn.QueryContext(ctx, `select server_id,effective_at from server_connectivity_events where id>? order by id limit 501`, cursor)
		if err != nil {
			return result, err
		}
		count := 0
		for rows.Next() {
			var server int64
			var at string
			if err := rows.Scan(&server, &at); err != nil {
				rows.Close()
				return result, err
			}
			count++
			if server == id && parseTime(at).Before(time.Unix(frontier, 0)) {
				useSummary = false
			}
		}
		readErr := rows.Err()
		rows.Close()
		if readErr != nil {
			return result, readErr
		}
		if count > 500 {
			useSummary = false
		}
	}
	buckets := map[int64]SLAProjectionBucket{}
	if useSummary {
		rows, err := conn.QueryContext(ctx, `select bucket_start,stats_json,end_checkpoint,outages_json from sla_projection_buckets where server_id=? and bucket_start>=? and bucket_start<? and algorithm_version=? and details_complete=1 order by bucket_start limit 8641`, id, max(from.Unix(), coverage, floor), min(to.Unix(), frontier), SLAProjectionVersion)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var bucket SLAProjectionBucket
			var stats, checkpoint, outages string
			if err := rows.Scan(&bucket.Start, &stats, &checkpoint, &outages); err != nil {
				rows.Close()
				return result, err
			}
			if err := json.Unmarshal([]byte(stats), &bucket.Stats); err != nil {
				rows.Close()
				return result, err
			}
			if err := json.Unmarshal([]byte(outages), &bucket.Outages); err != nil {
				rows.Close()
				return result, err
			}
			bucket.Checkpoint = json.RawMessage(checkpoint)
			if bucket.Start+300 <= to.Unix() {
				buckets[bucket.Start] = bucket
			}
		}
		readErr := rows.Err()
		rows.Close()
		if readErr != nil {
			return result, readErr
		}
		if len(buckets) > 8640 {
			return result, ErrHistoryCoverage
		}
	}
	budget := 50000
	rawUsed, summaryUsed := false, false
	var checkpoint json.RawMessage
	for at := from.Unix(); at < to.Unix(); {
		if bucket, ok := buckets[at]; ok {
			result.Parts = append(result.Parts, bucket)
			checkpoint = bucket.Checkpoint
			at += 300
			summaryUsed = true
			continue
		}
		end := min(to.Unix(), (at/300+1)*300)
		for end < to.Unix() {
			if _, ok := buckets[end]; ok {
				break
			}
			end = min(to.Unix(), end+300)
		}
		work := SLAProjectionWork{ServerID: id, From: at, To: end, Checkpoint: checkpoint, ReadStepSeconds: int64(resolution / time.Second)}
		if len(checkpoint) == 0 {
			for _, kind := range connectivityBaselineKinds {
				event, err := s.latestConnectivityEventBeforeOn(ctx, conn, id, time.Unix(at, 0), []model.ConnectivityEventKind{kind}, newest)
				if err == sql.ErrNoRows {
					continue
				}
				if err != nil {
					return result, err
				}
				work.Baseline = append(work.Baseline, event)
			}
			sort.Slice(work.Baseline, func(i, j int) bool { return slaEventLess(work.Baseline[i], work.Baseline[j]) })
		}
		rows, err := conn.QueryContext(ctx, `select id,server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at from server_connectivity_events where server_id=? and effective_at>=? and effective_at<? order by effective_at,id limit ?`, id, connectivityTimeBound(time.Unix(at, 0)), connectivityTimeBound(time.Unix(end, 0)), budget+1)
		if err != nil {
			return result, err
		}
		work.Events, err = scanConnectivityEvents(rows)
		if err != nil {
			return result, err
		}
		budget -= len(work.Events)
		if budget < 0 {
			return result, ErrHistoryCoverage
		}
		sort.Slice(work.Events, func(i, j int) bool { return slaEventLess(work.Events[i], work.Events[j]) })
		built, err := build(work)
		if err != nil {
			return result, err
		}
		result.Parts = append(result.Parts, built.Buckets...)
		if len(built.Buckets) > 0 {
			checkpoint = built.Buckets[len(built.Buckets)-1].Checkpoint
		}
		rawUsed = true
		at = end
	}
	for _, kind := range connectivityBaselineKinds {
		event, err := s.latestConnectivityEventBeforeOn(ctx, conn, id, to, []model.ConnectivityEventKind{kind}, newest)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return result, err
		}
		if result.ObservedThrough == nil || event.EffectiveAt.After(*result.ObservedThrough) {
			at := event.EffectiveAt
			result.ObservedThrough = &at
		}
	}
	if summaryUsed {
		result.Source = "summary"
		if rawUsed {
			result.Source = "mixed"
		}
	}
	return result, nil
}
