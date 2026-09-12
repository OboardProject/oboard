package controller

import (
	"context"
	"testing"
	"time"
)

func TestLatencyRollupScheduleIsOptInAndBounded(t *testing.T) {
	t.Setenv("OBOARD_LATENCY_ROLLUP_WRITE", "")
	t.Setenv("OBOARD_SLA_PROJECTION_WRITE", "")
	if newLatencyRollupSchedule() != nil {
		t.Fatal("summary worker enabled by default")
	}
	t.Setenv("OBOARD_LATENCY_ROLLUP_WRITE", "1")
	schedule := newLatencyRollupSchedule()
	if schedule.rows != 500 {
		t.Fatal("wrong initial row budget")
	}
	if delay := schedule.next(500, 100*time.Millisecond, nil); delay < 5*time.Second {
		t.Fatal("work duration not rate limited")
	}
	for i := 0; i < 20; i++ {
		schedule.next(0, 0, context.DeadlineExceeded)
	}
	if schedule.rows != 1 || schedule.delay > 5*time.Minute {
		t.Fatal("timeouts not bounded/backed off")
	}
	schedule.delay = 2 * time.Second
	for i := 0; i < 20; i++ {
		schedule.next(0, 0, nil)
	}
	if schedule.delay != 30*time.Second {
		t.Fatal("idle loop does not back off")
	}
}

func TestSLAProjectionUsesExistingMaintenanceBudget(t *testing.T) {
	t.Setenv("OBOARD_LATENCY_ROLLUP_WRITE", "")
	t.Setenv("OBOARD_SLA_PROJECTION_WRITE", "1")
	state := newLatencyRollupSchedule()
	if state == nil || !state.slaEnabled || state.latencyEnabled || state.rows != 500 {
		t.Fatal("SLA-only schedule not enabled")
	}
	if state.next(12, 100*time.Millisecond, nil) < 5*time.Second {
		t.Fatal("SLA bypassed shared wall-time budget")
	}
}

func TestLatencyRollupRecoversBudgetAfterTransientTimeout(t *testing.T) {
	state := &latencyRollupSchedule{rows: 500, delay: 2 * time.Second}
	for i := 0; i < 10; i++ {
		state.next(0, time.Second, context.DeadlineExceeded)
	}
	if state.rows != 1 {
		t.Fatal(state.rows)
	}
	for i := 0; i < 7; i++ {
		state.next(1, time.Millisecond, nil)
	}
	if state.rows != 1 {
		t.Fatal("recovered too early")
	}
	for i := 0; i < 200; i++ {
		if state.next(1, time.Millisecond, nil) < 2*time.Second {
			t.Fatal("lost delay budget")
		}
	}
	if state.rows != 500 {
		t.Fatalf("stuck at %d rows", state.rows)
	}
	state.next(0, time.Second, context.DeadlineExceeded)
	if state.rows != 250 || state.successfulBatches != 0 {
		t.Fatal("new timeout did not back off")
	}
}
