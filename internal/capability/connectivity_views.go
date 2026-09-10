package capability

import "encoding/json"

func connectivityDetailsInputSchema(events bool) json.RawMessage {
	fields := map[string]any{"server_id": map[string]any{"type": "integer", "minimum": 1}, "window": map[string]any{"type": "string", "enum": []string{"1h", "6h", "12h", "24h", "7d", "30d"}}}
	if events {
		fields["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 200}
		fields["cursor"] = map[string]any{"type": "string", "maxLength": 2048}
	}
	return schemaObject(fields, "server_id")
}

func connectivityDetailsOutputSchema(events bool) json.RawMessage {
	text := map[string]any{"type": "string"}
	number := map[string]any{"type": "number"}
	integer := map[string]any{"type": "integer"}
	boolean := map[string]any{"type": "boolean"}
	nullableTime := map[string]any{"type": []string{"string", "null"}}
	nullableNumber := map[string]any{"type": []string{"number", "null"}}
	window := closedObject(map[string]any{"key": text, "from": text, "to": text, "bucket_seconds": integer}, "key", "from", "to", "bucket_seconds")
	metadata := closedObject(map[string]any{"generated_at": text, "observed_through": nullableTime, "source": text, "retention_clipped": boolean, "statistics_basis": text}, "generated_at", "observed_through", "source", "retention_clipped", "statistics_basis")
	fields := map[string]any{"server_id": integer, "retention_days": integer, "window": window, "metadata": metadata}
	required := []string{"server_id", "retention_days", "window", "metadata"}
	if events {
		event := closedObject(map[string]any{"id": integer, "server_id": integer, "kind": text, "available": map[string]any{"type": []string{"boolean", "null"}}, "latency_ms": integer, "error": text, "source": text, "effective_at": text, "event_key": text, "created_at": text}, "id", "server_id", "kind", "available", "latency_ms", "error", "source", "effective_at", "event_key", "created_at")
		fields["events"] = arrayOf(event)
		fields["next_cursor"] = text
		fields["has_more"] = boolean
		required = append(required, "events", "next_cursor", "has_more")
	} else {
		fields["summary"] = closedObject(map[string]any{"sla_percent": nullableNumber, "available_seconds": number, "unavailable_seconds": number, "unknown_seconds": number, "observed_seconds": number, "coverage_percent": number, "outage_count": integer, "longest_outage_seconds": number}, "sla_percent", "available_seconds", "unavailable_seconds", "unknown_seconds", "observed_seconds", "coverage_percent", "outage_count", "longest_outage_seconds")
		fields["buckets"] = arrayOf(closedObject(map[string]any{"start_at": text, "end_at": text, "sla_percent": nullableNumber, "available_seconds": number, "unavailable_seconds": number, "unknown_seconds": number, "avg_latency_ms": nullableNumber}, "start_at", "end_at", "sla_percent", "available_seconds", "unavailable_seconds", "unknown_seconds", "avg_latency_ms"))
		fields["outages"] = arrayOf(closedObject(map[string]any{"started_at": text, "ended_at": nullableTime, "duration_seconds": number, "cause": text, "started_before_window": boolean}, "started_at", "ended_at", "duration_seconds", "cause", "started_before_window"))
		required = append(required, "summary", "buckets", "outages")
	}
	return schemaObject(fields, required...)
}
