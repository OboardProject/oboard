package confighealth

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func syncLaneReport(in Input) Report {
	in.Servers = append(in.Servers, model.Server{ID: 7, Name: "tokyo-1"})
	return Evaluate(in)
}

func findingWithCode(report Report, code string) (Finding, bool) {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return finding, true
		}
	}
	return Finding{}, false
}

// A node holding the desired revision with different content is the state
// neither side can leave on its own, so it has to be reported.
func TestUsersLaneContentDivergenceIsBlocking(t *testing.T) {
	report := syncLaneReport(Input{UsersLanes: []UsersLane{{
		ServerID: 7, DesiredRevision: 2332, DesiredDigest: "content-a",
		AppliedRevision: 2332, AppliedContentDigest: "content-b",
	}}})
	finding, ok := findingWithCode(report, CodeUsersRevisionConflict)
	if !ok {
		t.Fatalf("divergence was not reported: %+v", report.Findings)
	}
	if finding.Severity != SeverityBlocking || finding.Scope != ScopeSyncLane {
		t.Fatalf("unexpected finding shape: %+v", finding)
	}
	if finding.ResourceID != 7 || finding.ResourceName != "tokyo-1" || finding.ServerName != "tokyo-1" {
		t.Fatalf("finding does not name the server: %+v", finding)
	}
	if finding.Remedy.Kind != RemedyResync || finding.Remedy.Destructive {
		t.Fatalf("resync must be the offered remedy and must not be destructive: %+v", finding.Remedy)
	}
	if report.BlockingByServer[7] != 1 {
		t.Fatalf("blocking count not attributed to the server: %+v", report.BlockingByServer)
	}
}

// An acknowledged refusal is reported before the node's own report catches up.
func TestUsersLaneRecordedConflictIsReported(t *testing.T) {
	report := syncLaneReport(Input{UsersLanes: []UsersLane{{
		ServerID: 7, DesiredRevision: 5, DesiredDigest: "content-a",
		AppliedRevision: 5, AppliedContentDigest: "content-a",
		PendingReason: "revision_conflict", Conflicted: true,
	}}})
	if _, ok := findingWithCode(report, CodeUsersRevisionConflict); !ok {
		t.Fatalf("recorded conflict was not reported: %+v", report.Findings)
	}
}

// Everything that is not a disagreement about one version must stay silent: a
// lane in normal motion, and a node too old to report a content identity.
func TestUsersLaneQuietStates(t *testing.T) {
	for name, lane := range map[string]UsersLane{
		"converged":      {ServerID: 7, DesiredRevision: 5, DesiredDigest: "a", AppliedRevision: 5, AppliedContentDigest: "a"},
		"behind":         {ServerID: 7, DesiredRevision: 6, DesiredDigest: "b", AppliedRevision: 5, AppliedContentDigest: "a"},
		"never reported": {ServerID: 7, DesiredRevision: 5, DesiredDigest: "a"},
		"no identity":    {ServerID: 7, DesiredRevision: 5, DesiredDigest: "a", AppliedRevision: 5},
		"not evaluated":  {ServerID: 7},
	} {
		report := syncLaneReport(Input{UsersLanes: []UsersLane{lane}})
		if _, ok := findingWithCode(report, CodeUsersRevisionConflict); ok {
			t.Fatalf("%s must not be reported as a conflict", name)
		}
	}
}

func TestProbeLaneVersionConflict(t *testing.T) {
	for name, lane := range map[string]ProbeLane{
		"same version, different plan": {ServerID: 7, PlanVersion: 900, PlanDigest: "a", AppliedVersion: 900, AppliedDigest: "b"},
		// A node ahead of the ledger refuses every plan the Controller can
		// issue from it, which is the same dead end.
		"node ahead of the ledger": {ServerID: 7, PlanVersion: 900, PlanDigest: "a", AppliedVersion: 1200, AppliedDigest: "b"},
	} {
		report := syncLaneReport(Input{ProbeLanes: []ProbeLane{lane}})
		finding, ok := findingWithCode(report, CodeProbeVersionConflict)
		if !ok {
			t.Fatalf("%s was not reported: %+v", name, report.Findings)
		}
		if finding.Severity != SeverityWarning || finding.Remedy.Kind != RemedyResync {
			t.Fatalf("%s: unexpected finding shape: %+v", name, finding)
		}
	}
}

func TestProbeLaneQuietStates(t *testing.T) {
	for name, lane := range map[string]ProbeLane{
		"converged":      {ServerID: 7, PlanVersion: 900, PlanDigest: "a", AppliedVersion: 900, AppliedDigest: "a"},
		"behind":         {ServerID: 7, PlanVersion: 900, PlanDigest: "a", AppliedVersion: 800, AppliedDigest: "b"},
		"never reported": {ServerID: 7, PlanVersion: 900, PlanDigest: "a"},
		"no binding":     {ServerID: 7, AppliedVersion: 900, AppliedDigest: "a"},
	} {
		report := syncLaneReport(Input{ProbeLanes: []ProbeLane{lane}})
		if _, ok := findingWithCode(report, CodeProbeVersionConflict); ok {
			t.Fatalf("%s must not be reported as a conflict", name)
		}
	}
}
