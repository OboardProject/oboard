package controller

import (
	"context"
	"testing"
	"time"
)

func TestLatencyRollupScheduleIsOptInAndBounded(t *testing.T) {
	t.Setenv("OBOARD_LATENCY_ROLLUP_WRITE", "")
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
