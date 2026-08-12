package controller

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// pageDataSlowThresholdMs is the page-data total duration above which the
// request is logged as slow. The threshold only gates logging; responses are
// never altered by it.
const pageDataSlowThresholdMs = 250

// pageStageTimer measures page-data stage durations for the Server-Timing
// response header and slow request logging. It never carries user data, IDs,
// tokens, or any payload content.
type pageStageTimer struct {
	page    string
	started time.Time
	stages  []pageStage
}

type pageStage struct {
	name string
	dur  time.Duration
}

func newPageStageTimer(page string) *pageStageTimer {
	return &pageStageTimer{page: page, started: time.Now()}
}

// run records the wall-clock duration of fn under the given stage name.
func (t *pageStageTimer) run(name string, fn func() error) error {
	start := time.Now()
	err := fn()
	t.stages = append(t.stages, pageStage{name: name, dur: time.Since(start)})
	return err
}

// total is the elapsed time since the timer was created.
func (t *pageStageTimer) total() time.Duration {
	return time.Since(t.started)
}

// serverTiming renders the Server-Timing header value, e.g.
// "summary;dur=8.2, servers;dur=3.1, total;dur=410.4".
func (t *pageStageTimer) serverTiming() string {
	var builder strings.Builder
	for _, stage := range t.stages {
		fmt.Fprintf(&builder, "%s;dur=%.1f, ", stage.name, float64(stage.dur.Microseconds())/1000.0)
	}
	fmt.Fprintf(&builder, "total;dur=%.1f", float64(t.total().Microseconds())/1000.0)
	return builder.String()
}

// logSlowIfNeeded records slow page-data requests with stage durations and no
// payload content.
func (t *pageStageTimer) logSlowIfNeeded() {
	total := t.total()
	if total.Milliseconds() < pageDataSlowThresholdMs {
		return
	}
	var builder strings.Builder
	for _, stage := range t.stages {
		fmt.Fprintf(&builder, " %s=%dms", stage.name, stage.dur.Milliseconds())
	}
	log.Printf("page-data slow page=%s total=%dms stages=[%s]", t.page, total.Milliseconds(), strings.TrimSpace(builder.String()))
}
