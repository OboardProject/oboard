package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMetricReportJSONContract(t *testing.T) {
	encoded, err := json.Marshal(MetricReport{ReportID: "report-1", SampledAt: time.Unix(1, 0).UTC(), CPUUsagePercent: 12.5, DiskUsedBytes: 7, NetworkDownloadBPS: 9})
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, key := range []string{`"report_id"`, `"sampled_at"`, `"cpu_usage_percent"`, `"memory_used_bytes"`, `"memory_total_bytes"`, `"disk_used_bytes"`, `"disk_total_bytes"`, `"tcp_connection_count"`, `"udp_connection_count"`, `"process_count"`, `"network_upload_bps"`, `"network_download_bps"`} {
		if !strings.Contains(text, key) {
			t.Fatalf("metric report JSON missing %s: %s", key, text)
		}
	}
	if strings.Contains(text, `"server_id"`) || strings.Contains(text, `"status"`) {
		t.Fatalf("metric report JSON widened contract: %s", text)
	}
}
