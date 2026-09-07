package capability

func serverMonitoringDisplaySchema() map[string]any {
	return closedObject(map[string]any{
		"name":    map[string]any{"type": "string"},
		"enabled": map[string]any{"type": "boolean"},
		"samples": map[string]any{"type": "array", "maxItems": 20, "items": closedObject(map[string]any{
			"available":     map[string]any{"type": "boolean"},
			"latency_ms":    map[string]any{"type": []string{"number", "null"}},
			"sample_count":  map[string]any{"type": "integer"},
			"success_count": map[string]any{"type": "integer"},
			"checked_at":    map[string]any{"type": "string"},
		}, "available", "latency_ms", "sample_count", "success_count", "checked_at")},
	}, "name", "enabled", "samples")
}
