package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type connectivityViewInput struct {
	ServerID int64  `json:"server_id"`
	Window   string `json:"window,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
}

type connectivityViewMetadata struct {
	GeneratedAt      time.Time  `json:"generated_at"`
	ObservedThrough  *time.Time `json:"observed_through"`
	Source           string     `json:"source"`
	RetentionClipped bool       `json:"retention_clipped"`
	StatisticsBasis  string     `json:"statistics_basis"`
}

type connectivitySLAResponse struct {
	ServerID      int64                    `json:"server_id"`
	RetentionDays int                      `json:"retention_days"`
	Window        connectivityWindow       `json:"window"`
	Summary       connectivitySummary      `json:"summary"`
	Buckets       []connectivityBucket     `json:"buckets"`
	Outages       []connectivityOutage     `json:"outages"`
	Metadata      connectivityViewMetadata `json:"metadata"`
}

type connectivityEventsResponse struct {
	ServerID      int64                           `json:"server_id"`
	RetentionDays int                             `json:"retention_days"`
	Window        connectivityWindow              `json:"window"`
	Events        []model.ServerConnectivityEvent `json:"events"`
	NextCursor    string                          `json:"next_cursor"`
	HasMore       bool                            `json:"has_more"`
	Metadata      connectivityViewMetadata        `json:"metadata"`
}

type connectivityCursor struct {
	ServerID   int64     `json:"server_id"`
	Window     string    `json:"window"`
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	BeforeTime string    `json:"before_time"`
	BeforeID   int64     `json:"before_id"`
	SnapshotID int64     `json:"snapshot_id"`
}

func (s *Server) readConnectivityDetails(ctx context.Context, p application.Principal, view string, input connectivityViewInput) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if input.ServerID <= 0 || !p.AllowsInt64("server_ids", input.ServerID) {
		return nil, errors.New("resource_denied: server access denied")
	}
	if view != "sla" && view != "events" {
		return nil, errors.New("invalid_history_input: unsupported view")
	}
	if view == "sla" && (input.Limit != 0 || input.Cursor != "") {
		return nil, errors.New("invalid_history_input: SLA does not accept pagination")
	}
	if input.Limit == 0 {
		input.Limit = 100
	}
	if input.Limit < 1 || input.Limit > 200 || len(input.Cursor) > 2048 {
		return nil, errors.New("invalid_history_input: invalid limit or cursor")
	}
	now := time.Now().UTC().Truncate(time.Second)
	if s.latencyHistoryNow != nil {
		now = s.latencyHistoryNow().UTC().Truncate(time.Second)
	}
	window, err := parseConnectivityWindow(input.Window, now.Truncate(30*time.Second))
	if err != nil {
		return nil, err
	}
	cursor := connectivityCursor{}
	if input.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(input.Cursor)
		if err != nil || json.Unmarshal(raw, &cursor) != nil {
			return nil, errors.New("invalid_history_input: invalid cursor")
		}
		before, err := time.Parse(time.RFC3339Nano, cursor.BeforeTime)
		if err != nil || cursor.ServerID != input.ServerID || cursor.Window != window.Key || cursor.From.Nanosecond() != 0 || cursor.To.Nanosecond() != 0 || cursor.To.After(now) || !cursor.To.After(cursor.From) || cursor.To.Sub(cursor.From) > window.Duration || before.Before(cursor.From) || !before.Before(cursor.To) || cursor.BeforeID <= 0 || cursor.SnapshotID < cursor.BeforeID {
			return nil, errors.New("invalid_history_input: cursor does not match window")
		}
		window.From, window.To = cursor.From, cursor.To
		window.Duration = window.To.Sub(window.From)
	}
	if _, err := s.store.GetServer(ctx, input.ServerID); err != nil {
		return nil, err
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return nil, err
	}
	days := store.ServerMonitoringRetentionDays(settings)
	requestedDuration := window.Duration
	// Retention follows current time even when an older page cursor fixes the window.
	retainedFrom := now.Add(-time.Duration(days) * 24 * time.Hour)
	if retainedFrom.After(window.From) {
		window.From = retainedFrom
	}
	if !window.To.After(window.From) {
		return nil, errors.New("invalid_history_input: cursor window has expired; restart pagination")
	}
	window.Duration = window.To.Sub(window.From)
	fingerprint, _ := json.Marshal([]any{p, view, input, window.From, window.To, days})
	key := fmt.Sprintf("full:%s:%x", view, sha256.Sum256(fingerprint))
	entry, err := s.historyReads().getOrBuild(ctx, key, 0, func(buildCtx context.Context) (json.RawMessage, time.Time, error) {
		metadata := connectivityViewMetadata{Source: "raw_state_events", RetentionClipped: window.Duration < requestedDuration}
		var response any
		if view == "sla" {
			history, err := s.store.ListConnectivitySLAHistory(buildCtx, input.ServerID, window.From, window.To)
			if err != nil {
				return nil, time.Time{}, err
			}
			segments, _ := buildConnectivitySegments(window.From, window.To, history.Baseline, history.Events)
			summary, outages := connectivityStateSummary(window, segments, history)
			if len(outages) > 10 {
				outages = outages[len(outages)-10:]
			}
			for l, r := 0, len(outages)-1; l < r; l, r = l+1, r-1 {
				outages[l], outages[r] = outages[r], outages[l]
			}
			var observed time.Time
			for _, events := range [][]model.ServerConnectivityEvent{history.Baseline, history.Events} {
				for _, event := range events {
					if event.EffectiveAt.After(observed) {
						observed = event.EffectiveAt
					}
				}
			}
			if !observed.IsZero() {
				metadata.ObservedThrough = &observed
			}
			metadata.GeneratedAt = time.Now().UTC()
			metadata.StatisticsBasis = "online duration / (online + offline duration); unknown excluded, coverage separate; existing connectivity state machine"
			response = connectivitySLAResponse{ServerID: input.ServerID, RetentionDays: days, Window: window, Summary: summary, Buckets: buildConnectivityBuckets(window, segments, nil), Outages: outages, Metadata: metadata}
		} else {
			page, err := s.store.ListConnectivityEventPage(buildCtx, input.ServerID, window.From, window.To, input.Limit, cursor.BeforeTime, cursor.BeforeID, cursor.SnapshotID)
			if err != nil {
				return nil, time.Time{}, err
			}
			next := ""
			if page.HasMore {
				raw, _ := json.Marshal(connectivityCursor{ServerID: input.ServerID, Window: window.Key, From: window.From, To: window.To, BeforeTime: page.BeforeTime, BeforeID: page.BeforeID, SnapshotID: page.SnapshotID})
				next = base64.RawURLEncoding.EncodeToString(raw)
			}
			var observed time.Time
			for _, event := range page.Events {
				if event.EffectiveAt.After(observed) {
					observed = event.EffectiveAt
				}
			}
			if !observed.IsZero() {
				metadata.ObservedThrough = &observed
			}
			metadata.GeneratedAt = time.Now().UTC()
			metadata.StatisticsBasis = "diagnostic events ordered by stored effective_at descending, then id; observation timestamp covers this page only"
			response = connectivityEventsResponse{ServerID: input.ServerID, RetentionDays: days, Window: window, Events: page.Events, NextCursor: next, HasMore: page.HasMore, Metadata: metadata}
		}
		encoded, err := json.Marshal(response)
		if len(encoded) > latencyResponseMaxBytes-4096 {
			return nil, time.Time{}, errHistoryTooLarge
		}
		return encoded, metadata.GeneratedAt, err
	})
	return entry.value, err
}

func connectivityDetailsRequest(r *http.Request, serverID int64) (connectivityViewInput, error) {
	q := r.URL.Query()
	input := connectivityViewInput{ServerID: serverID, Window: q.Get("window"), Cursor: q.Get("cursor")}
	for key, values := range q {
		if len(values) != 1 || (key != "view" && key != "window" && (q.Get("view") != "events" || (key != "limit" && key != "cursor"))) {
			return input, errors.New("invalid_history_input: unsupported parameter")
		}
	}
	if raw, ok := q["limit"]; ok {
		value, err := strconv.Atoi(raw[0])
		if err != nil || value < 1 || value > 200 {
			return input, errors.New("invalid_history_input: limit must be 1..200")
		}
		input.Limit = value
	}
	return input, nil
}
