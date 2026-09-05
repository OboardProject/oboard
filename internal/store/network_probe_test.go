package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestNetworkProbeTaskMethodsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "network.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, task := range []model.LatencyProbeTask{
		{Name: "TCP", Method: model.LatencyProbeModeTCP, Address: " EXAMPLE.COM ", Port: 8443},
		{Name: "Ping", Method: model.LatencyProbeModeICMP, Address: "1.1.1.1", Port: 443},
		{Name: "HTTP", Method: model.LatencyProbeModeHTTP, Address: "https://EXAMPLE.COM:8443/Health?check=1", Port: 443},
	} {
		task.Enabled = true
		if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
			t.Fatal(err)
		}
		got, err := db.GetLatencyProbeTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Method != task.Method || got.Address != task.Address || got.Port != task.Port || got.Province != "" || got.Carrier != "" {
			t.Fatalf("round trip: %#v", got)
		}
		if got.Method != model.LatencyProbeModeTCP && got.Port != 0 {
			t.Fatalf("non-TCP task has a port: %#v", got)
		}
	}
	task := model.LatencyProbeTask{Address: "https://example.com/" + strings.Repeat("a", 100), Method: model.LatencyProbeModeHTTP}
	if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	if len([]rune(task.Name)) != 60 {
		t.Fatalf("automatic name length = %d", len([]rune(task.Name)))
	}
}

func TestNetworkProbeTaskValidation(t *testing.T) {
	for _, tc := range []struct {
		method  model.LatencyProbeMode
		address string
		port    int
	}{
		{"udp", "example.com", 80}, {"tcp", " ", 80}, {"tcp", "example.com", 65536},
		{"tcp", "localhost", 80}, {"tcp", "127.0.0.1", 80}, {"icmp", "10.0.0.1", 0},
		{"tcp", "2001:db8::1", 80}, {"tcp", "example.com:443", 80}, {"tcp", "example.1", 80},
		{"tcp", "single.", 80}, {"tcp", "http://example.com", 80},
		{"http", "ftp://example.com", 0}, {"http", "https://user:pass@example.com", 0},
		{"http", "https://example.com/#fragment", 0}, {"http", "http://169.254.169.254/", 0},
		{"http", "http://example.com:65536/", 0}, {"http", "", 0},
	} {
		t.Run(string(tc.method)+tc.address, func(t *testing.T) {
			task := model.LatencyProbeTask{Method: tc.method, Address: tc.address, Port: tc.port}
			if err := normalizeLatencyProbeTask(&task); err == nil {
				t.Fatalf("invalid target accepted: %#v", task)
			}
		})
	}
}

func TestNetworkProbeFieldsMigratePreviousTaskSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "previous-node", LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	task := model.LatencyProbeTask{Name: "地区任务", Province: "广东", Carrier: "中国电信", IntervalSeconds: 900, Enabled: true, ServerIDs: []int64{server.ID}}
	if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"method", "address", "port"} {
		if _, err := db.db.Exec("alter table latency_probe_tasks drop column " + column); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.GetLatencyProbeTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "tcp" || got.Port != 80 || got.Address != "" || got.Name != task.Name || got.Province != task.Province || got.Carrier != task.Carrier || got.IntervalSeconds != 900 || !got.Enabled || len(got.ServerIDs) != 1 || got.ServerIDs[0] != server.ID || !got.CreatedAt.Equal(task.CreatedAt) {
		t.Fatalf("migration lost task state: %#v", got)
	}
	got.Method = model.LatencyProbeModeHTTP
	got.Address = "https://example.com/health"
	if err := db.SaveLatencyProbeTask(ctx, got); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	again, err := db.GetLatencyProbeTask(ctx, task.ID)
	if err != nil || again.Method != "http" || again.Address != got.Address || again.Port != 0 {
		t.Fatalf("migration overwrote operator edits: %#v %v", again, err)
	}
}

func TestNetworkProbeCustomResultsAppearInHistory(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "history-node"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	report := model.LatencyProbeResultReport{ReportID: "http-result", ResourceVersion: "public", CheckedAt: now, Items: []model.LatencyProbeResult{{ProbeID: "t1-0", TaskID: 1, TaskName: "网站", Kind: "custom", Mode: "http", Host: "example.com", Port: 443, SampleCount: 1, SuccessCount: 1, Available: true, LatencyMS: 12}}}
	if err := db.SaveLatencyProbeResults(ctx, server.ID, report); err != nil {
		t.Fatal(err)
	}
	points, _, err := db.ListRegionalLatencyPoints(ctx, server.ID, now.Add(-time.Minute), now.Add(time.Minute), time.Minute)
	if err != nil || len(points) != 1 || points[0].TaskName != "网站" {
		t.Fatalf("custom history missing: %#v %v", points, err)
	}
}
