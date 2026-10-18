package pluginruntime

import (
	"context"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/pluginsandbox"
)

func TestRunnerFailureDoesNotInventResourceExhaustion(t *testing.T) {
	if got := runnerFailure(context.Background(), &pluginsandbox.Command{}); got.Code != "RUNTIME_UNAVAILABLE" {
		t.Fatalf("unexpected exit classified as %+v", got)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := runnerFailure(cancelled, nil); got.Code != "CANCELLED" {
		t.Fatalf("cancelled run classified as %+v", got)
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if got := runnerFailure(expired, nil); got.Code != "RUN_TIMEOUT" {
		t.Fatalf("expired run classified as %+v", got)
	}
}
