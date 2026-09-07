package scripting

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/OboardProject/oboard/internal/model"
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func ParseTriggerSpec(raw []byte) (model.ScriptTriggerSpec, error) {
	spec := model.ScriptTriggerSpec{}
	if err := strictJSON(raw, &spec); err != nil {
		return spec, err
	}
	if spec.Timezone == "" {
		return spec, Coded(codeInvalidInput, "timezone is required")
	}
	if _, err := time.LoadLocation(spec.Timezone); err != nil {
		return spec, Coded(codeInvalidInput, "timezone must be an IANA name")
	}
	return spec, nil
}

func NextSlots(spec model.ScriptTriggerSpec, from time.Time, count int) ([]time.Time, error) {
	if count <= 0 {
		count = 5
	}
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return nil, Coded(codeInvalidInput, "timezone must be an IANA name")
	}
	cursor := from.In(loc)
	out := make([]time.Time, 0, count)
	switch {
	case spec.OnceAt != "":
		at, err := time.ParseInLocation(time.RFC3339, spec.OnceAt, loc)
		if err != nil {
			at, err = time.ParseInLocation("2006-01-02T15:04:05", spec.OnceAt, loc)
		}
		if err != nil {
			return nil, Coded(codeInvalidInput, "once_at must be RFC3339 or local date-time")
		}
		if !at.Before(cursor) {
			out = append(out, at.UTC())
		}
	case spec.IntervalSeconds > 0:
		if spec.IntervalSeconds < 10 || spec.IntervalSeconds > 86400*30 {
			return nil, Coded(codeInvalidInput, "interval_seconds is out of range")
		}
		step := time.Duration(spec.IntervalSeconds) * time.Second
		next := alignInterval(cursor, step)
		for len(out) < count {
			out = append(out, next.UTC())
			next = next.Add(step)
		}
	case spec.Cron != "":
		schedule, err := cronParser.Parse(spec.Cron)
		if err != nil {
			return nil, Coded(codeInvalidInput, "cron expression is invalid")
		}
		next := cursor
		for len(out) < count {
			next = schedule.Next(next)
			if next.IsZero() {
				break
			}
			out = append(out, next.UTC())
		}
	default:
		return nil, Coded(codeInvalidInput, "timed trigger requires once_at, interval_seconds, or cron")
	}
	return out, nil
}

func SlotKey(bindingID int64, slot time.Time) string {
	return fmt.Sprintf("trig:%d:slot:%s", bindingID, slot.UTC().Format(time.RFC3339))
}

func EventKey(bindingID, serverID, incidentID int64, eventType string) string {
	return fmt.Sprintf("trig:%d:server:%d:incident:%d:event:%s", bindingID, serverID, incidentID, eventType)
}

func DSTGap(spec model.ScriptTriggerSpec, candidate time.Time) bool {
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return false
	}
	local := candidate.In(loc)
	_, offset := local.Zone()
	rewound := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), local.Second(), 0, loc)
	_, rewritten := rewound.Zone()
	return offset != rewritten && rewound.UTC().Equal(candidate.UTC()) == false && local.Hour() != rewound.Hour()
}

func alignInterval(from time.Time, step time.Duration) time.Time {
	unix := from.Unix()
	mod := unix % int64(step.Seconds())
	if mod == 0 && from.Nanosecond() == 0 {
		return from
	}
	return time.Unix(unix-mod+int64(step.Seconds()), 0).In(from.Location())
}
