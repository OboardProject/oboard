package controller

import (
	"testing"

	"github.com/OboardProject/oboard/internal/store"
)

func TestLaneCompletionRanksFailuresOverOffline(t *testing.T) {
	failed := store.AuthorizationState{LastError: "boom", Retryable: false, DesiredRevision: 2, ConfirmedRevision: 1}
	offline := store.RuntimeUserState{PendingReason: store.RuntimeUsersPendingAgentOffline, DesiredRevision: 2, ConfirmedRevision: 1}
	if got := laneCompletion(failed, offline); got != "failed" {
		t.Fatalf("failed lane ranked %q, want failed", got)
	}
	upgrade := store.AuthorizationState{PendingReason: store.AuthorizationPendingAgentUpgrade, DesiredRevision: 2}
	if got := laneCompletion(upgrade, store.RuntimeUserState{}); got != "upgrade_required" {
		t.Fatalf("upgrade ranked %q", got)
	}
	if !lanePending(store.AuthorizationState{DesiredRevision: 3, ConfirmedRevision: 2}, store.RuntimeUserState{}) {
		t.Fatal("unconfirmed authorization should be pending")
	}
	if lanePending(store.AuthorizationState{DesiredRevision: 1, ConfirmedRevision: 1}, store.RuntimeUserState{DesiredRevision: 2, PendingReason: store.RuntimeUsersPendingAgentUpgrade}) {
		t.Fatal("upgrade-required users lane is not a live pending delivery")
	}
}
