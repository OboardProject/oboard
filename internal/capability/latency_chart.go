package capability

import "encoding/json"

func latencyChartOutputSchema() json.RawMessage {
	text := map[string]any{"type": "string"}
	number := map[string]any{"type": "number"}
	integer := map[string]any{"type": "integer"}
	boolean := map[string]any{"type": "boolean"}
	nullableNumber := map[string]any{"type": []string{"number", "null"}}
	nullableTime := map[string]any{"type": []string{"string", "null"}}
	point := closedObject(map[string]any{"at": text, "avg_ms": number, "min_ms": integer, "max_ms": integer, "count": integer}, "at", "avg_ms", "min_ms", "max_ms", "count")
	failure := closedObject(map[string]any{"at": text, "count": integer}, "at", "count")
	regional := closedObject(map[string]any{"kind": text, "task_id": integer, "task_name": text, "province": text, "carrier": text, "available": boolean, "latency_ms": number, "min_latency_ms": integer, "max_latency_ms": integer, "count": integer, "checked_at": text}, "kind", "province", "carrier", "available", "latency_ms", "min_latency_ms", "max_latency_ms", "count", "checked_at")
	stats := map[string]any{"key": text, "kind": text, "task_id": integer, "task_name": text, "mode": text, "province": text, "carrier": text, "sample_count": integer, "success_count": integer, "report_count": integer, "available_count": integer, "peak_latency_at": nullableTime, "peak_loss_at": nullableTime}
	for _, key := range []string{"avg_ms", "min_ms", "max_ms", "jitter_ms", "loss_percent", "success_percent", "peak_latency_ms", "peak_loss_percent"} {
		stats[key] = nullableNumber
	}
	coverage := closedObject(map[string]any{"source": text, "retention_clipped": boolean, "has_samples": boolean, "legacy_curve_reports": boolean}, "source", "retention_clipped", "has_samples", "legacy_curve_reports")
	metadata := closedObject(map[string]any{"requested_from": text, "requested_to": text, "effective_from": text, "effective_to": text, "resolution_seconds": integer, "generated_at": text, "observed_through": nullableTime, "aggregation_state": map[string]any{"type": "string", "enum": []string{"ready", "catching_up", "unavailable"}}, "coverage": coverage, "statistics_basis": text, "stale": boolean}, "requested_from", "requested_to", "effective_from", "effective_to", "resolution_seconds", "generated_at", "observed_through", "aggregation_state", "coverage", "statistics_basis", "stale")
	return schemaObject(map[string]any{"server_id": integer, "retention_days": integer, "window": closedObject(map[string]any{"key": text, "from": text, "to": text, "bucket_seconds": integer}, "key", "from", "to", "bucket_seconds"), "latency_points": arrayOf(point), "failed_probe_points": arrayOf(failure), "regional_latency_points": arrayOf(regional), "probe_target_stats": arrayOf(closedObject(stats, "key", "kind", "avg_ms", "min_ms", "max_ms", "jitter_ms", "sample_count", "success_count", "report_count", "available_count", "loss_percent", "success_percent")), "regional_data_start_at": nullableTime, "metadata": metadata}, "server_id", "retention_days", "window", "latency_points", "failed_probe_points", "regional_latency_points", "probe_target_stats", "regional_data_start_at", "metadata")
}
