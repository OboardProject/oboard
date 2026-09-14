package store

import (
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// Coverage used to answer only "did the kernel drop buckets", so an evidence set
// the loader had truncated was reported as complete. On a production Controller
// the busiest user had 864,676 reports in a 30-day window against a 50,000-row
// limit - the newest 22.6 hours of it - and the panel presented the resulting
// risk assessment as covering the whole month.
//
// CoverageComplete also gates automatic device restriction, so this was not only
// a display problem: a restriction could be decided on evidence that silently
// omitted most of the window.
func TestTruncatedEvidenceIsNotCompleteCoverage(t *testing.T) {
	at := time.Now().UTC()
	reports := []model.ConnectionAuditReport{{
		ServerID: 1, UserID: 1, SourceIP: "198.51.100.1", Network: "tcp",
		BucketCapacity: 8, DroppedBucketCount: 0, CollectionGeneration: 3,
		CollectionStartedAt: at.Add(-time.Minute), CollectionEndedAt: at,
		StartedAt: at.Add(-time.Minute), EndedAt: at,
	}}

	quality, complete := connectionAuditCoverage(reports, false)
	if !complete {
		t.Fatal("an untruncated window with no dropped buckets is complete coverage")
	}
	if quality != 1 {
		t.Fatalf("quality = %v, want 1 when the kernel dropped nothing", quality)
	}

	_, complete = connectionAuditCoverage(reports, true)
	if complete {
		t.Fatal("a truncated evidence set was reported as complete coverage")
	}

	// An empty window is complete only when nothing was cut off.
	if _, complete = connectionAuditCoverage(nil, false); !complete {
		t.Fatal("an empty window with no truncation is complete")
	}
	if _, complete = connectionAuditCoverage(nil, true); complete {
		t.Fatal("an empty truncated window was reported as complete")
	}
}

// The kernel's own dropped buckets must still mark coverage incomplete on their
// own, independently of the loader limit.
func TestDroppedKernelBucketsStillBreakCoverage(t *testing.T) {
	at := time.Now().UTC()
	reports := []model.ConnectionAuditReport{{
		ServerID: 1, UserID: 1, SourceIP: "198.51.100.1", Network: "tcp",
		BucketCapacity: 4, DroppedBucketCount: 6, CollectionGeneration: 1,
		CollectionStartedAt: at.Add(-time.Minute), CollectionEndedAt: at,
		StartedAt: at.Add(-time.Minute), EndedAt: at,
	}}
	quality, complete := connectionAuditCoverage(reports, false)
	if complete {
		t.Fatal("dropped kernel buckets must not read as complete coverage")
	}
	if quality >= 1 {
		t.Fatalf("quality = %v, want it reduced by the dropped buckets", quality)
	}
}
