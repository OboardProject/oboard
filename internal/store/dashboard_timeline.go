package store

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// timelineGroupKeys mirrors the dashboard grouping in web/src/task-groups.ts:
// tasks sharing a positive config_version form a deployment bundle only when
// one member is apply_deployment; other tasks are batched by batchable type
// inside a two-minute creation window or stand alone. Groups are ordered by
// their newest member time (string comparison, matching the frontend's
// localeCompare over RFC3339Nano payloads) and then by group id. It returns at
// most groupLimit group ids for convergence detection.
func timelineGroupKeys(rows []model.AgentTask, groupLimit int) []string {
	groups := timelineGroups(rows)
	if len(groups) > groupLimit {
		groups = groups[:groupLimit]
	}
	keys := make([]string, 0, len(groups))
	for _, group := range groups {
		keys = append(keys, group.id)
	}
	return keys
}

type timelineGroup struct {
	id    string
	time  string
	count int
}

func timelineGroups(rows []model.AgentTask) []timelineGroup {
	byVersion := map[int64][]model.AgentTask{}
	leftover := []model.AgentTask{}
	for _, task := range rows {
		if task.ConfigVersion > 0 {
			byVersion[task.ConfigVersion] = append(byVersion[task.ConfigVersion], task)
			continue
		}
		leftover = append(leftover, task)
	}
	groups := []timelineGroup{}
	for version, tasks := range byVersion {
		if timelineIsDeploymentBundle(tasks) {
			groups = append(groups, timelineGroup{id: fmt.Sprintf("deploy-%d", version), time: timelineMaxTaskTime(tasks), count: len(tasks)})
			continue
		}
		leftover = append(leftover, tasks...)
	}
	batches := map[string][]model.AgentTask{}
	for _, task := range leftover {
		key := "single:" + strconv.FormatInt(task.ID, 10)
		if _, ok := timelineBatchableTaskTypes[task.Type]; ok {
			key = task.Type + ":" + timelineBatchBucket(task)
		}
		batches[key] = append(batches[key], task)
	}
	for key, tasks := range batches {
		servers := map[int64]struct{}{}
		for _, task := range tasks {
			servers[task.ServerID] = struct{}{}
		}
		id := key
		if !strings.HasPrefix(key, "single:") {
			id = "batch-" + key
		}
		groups = append(groups, timelineGroup{id: id, time: timelineMaxTaskTime(tasks), count: len(tasks)})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].time != groups[j].time {
			return groups[i].time > groups[j].time
		}
		return groups[i].id > groups[j].id
	})
	return groups
}

var timelineBatchableTaskTypes = map[string]struct{}{
	"update_agent":            {},
	"update_agent_config":     {},
	"diagnose_network":        {},
	"list_network_interfaces": {},
	"detect_mtu":              {},
	"probe_inbounds":          {},
	"probe_inbounds_external": {},
	"probe_port_forwards":     {},
	"probe_external_egress":   {},
	"collect_logs":            {},
	"manage_logs":             {},
	"check_time":              {},
}

func timelineIsDeploymentBundle(tasks []model.AgentTask) bool {
	for _, task := range tasks {
		if task.Type == model.AgentTaskTypeApplyDeployment {
			return true
		}
	}
	return false
}

// timelineBatchBucket mirrors taskBatchBucket in web/src/task-groups.ts: the
// creation time parsed as milliseconds, floored to a two-minute window.
func timelineBatchBucket(task model.AgentTask) string {
	raw := task.CreatedAt
	if raw.IsZero() {
		raw = task.UpdatedAt
	}
	if raw.IsZero() {
		return "unknown"
	}
	// JavaScript Date.parse truncates fractional seconds to milliseconds.
	return strconv.FormatInt(raw.UnixMilli()/(2*60*1000), 10)
}

// timelineMaxTaskTime mirrors maxTaskTime in web/src/task-groups.ts: the
// lexicographically largest RFC3339Nano payload string wins.
func timelineMaxTaskTime(tasks []model.AgentTask) string {
	best := ""
	for _, task := range tasks {
		raw := task.UpdatedAt
		if raw.IsZero() {
			raw = task.CreatedAt
		}
		if raw.IsZero() {
			continue
		}
		value := raw.UTC().Format(time.RFC3339Nano)
		if value > best {
			best = value
		}
	}
	return best
}
