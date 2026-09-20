package store

import (
	"context"
	"encoding/json"
	"github.com/OboardProject/oboard/internal/auditrisk"
	"testing"
	"time"
)

func TestAccountAuditMissingEvidencePreservesLastOccurrence(t *testing.T) {
	s, id := accountAuditFixture(t)
	ctx := context.Background()
	var last string
	for minute := 1; minute <= 16; minute++ {
		snap := accountAuditSnapshot(id, minute, 80)
		snap.Status = "evaluated"
		snap.Features.EvidenceCutoff = snap.AsOf.Add(-10 * time.Second)
		if minute >= 3 && minute <= 12 {
			snap.Status = "stale"
			snap.Quality.Freshness.State = auditrisk.Unsatisfied
		}
		if minute == 14 {
			snap.Activity.Lower = 0
			snap.Activity.Upper = 0
		}
		if minute == 15 {
			snap.Features.EvidenceCutoff = snap.Features.EvidenceCutoff.Add(-5 * time.Minute)
		}
		if minute == 16 {
			snap.Features.EvidenceCutoff = time.Time{}
		}
		if err := s.SaveAccountAuditSnapshot(ctx, snap, int64(minute)); err != nil {
			t.Fatal(err)
		}
		if minute == 1 {
			continue
		}
		page, err := s.ListAccountAuditEvents(ctx, AccountAuditQuery{})
		if err != nil {
			t.Fatal(err)
		}
		row := page.Items.([]AccountAuditEvent)[0]
		if minute == 2 {
			last = row.LastSeenAt
		}
		if minute >= 3 && minute <= 12 && (row.LastSeenAt != last || row.EvaluationStatus != "stale" || row.Status != "pending") {
			t.Fatalf("missing evidence refreshed occurrence: %+v", row)
		}
		if minute == 13 {
			if row.LastSeenAt != snap.Features.EvidenceCutoff.UTC().Format("2006-01-02T15:04:05.000000000Z") {
				t.Fatal("new evidence did not advance occurrence")
			}
			last = row.LastSeenAt
		}
		if minute > 13 && row.LastSeenAt != last {
			t.Fatal("low, older or absent evidence refreshed occurrence")
		}
	}
}

func TestAccountAuditReadSnapshotStatus(t *testing.T) {
	s, id := accountAuditFixture(t)
	ctx := context.Background()
	for i, status := range []string{"pending", "evaluated", "not_evaluable", "stale"} {
		snap := accountAuditSnapshot(id, i+1, 0)
		snap.Status = status
		if err := s.SaveAccountAuditSnapshot(ctx, snap, int64(i+1)); err != nil {
			t.Fatal(err)
		}
		page, err := s.ListAccountAuditSnapshots(ctx, AccountAuditQuery{UserID: id})
		if err != nil {
			t.Fatal(err)
		}
		row := page.Items.([]AccountAuditRow)[0]
		if row.EvaluationStatus != status {
			t.Fatalf("want %s got %s", status, row.EvaluationStatus)
		}
	}
}

func TestAccountAuditEventSummaryAndAuthorizedDetail(t *testing.T) {
	s, id := accountAuditFixture(t)
	ctx := context.Background()
	var expected []byte
	for i := 1; i <= 2; i++ {
		snap := accountAuditSnapshot(id, i, 80)
		snap.Status = "evaluated"
		if err := s.SaveAccountAuditSnapshot(ctx, snap, int64(i)); err != nil {
			t.Fatal(err)
		}
		expected, _ = json.Marshal(snap)
	}
	page, err := s.ListAccountAuditEvents(ctx, AccountAuditQuery{})
	if err != nil {
		t.Fatal(err)
	}
	rows := page.Items.([]AccountAuditEvent)
	if len(rows) != 1 || rows[0].Snapshot != nil || rows[0].EvaluationStatus != "evaluated" {
		t.Fatalf("not a summary: %+v", rows)
	}
	eventID := rows[0].ID
	page, err = s.ListAccountAuditEvents(ctx, AccountAuditQuery{EventID: eventID, AllowedUserIDs: []int64{id}})
	if err != nil {
		t.Fatal(err)
	}
	rows = page.Items.([]AccountAuditEvent)
	if len(rows) != 1 || string(rows[0].Snapshot) != string(expected) {
		t.Fatalf("detail differs: %+v", rows)
	}
	for _, query := range []AccountAuditQuery{
		{EventID: eventID, AllowedUserIDs: []int64{}},
		{EventID: eventID, AllowedUserIDs: []int64{id + 1}},
		{EventID: eventID, UserID: id + 1},
		{EventID: eventID + 1},
	} {
		page, err = s.ListAccountAuditEvents(ctx, query)
		if err != nil || len(page.Items.([]AccountAuditEvent)) != 0 {
			t.Fatalf("detail isolation: %+v %v", page, err)
		}
	}
}
