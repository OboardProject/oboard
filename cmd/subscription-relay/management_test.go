package main

import "testing"

func TestRelayUpdateOutputSuffixKeepsInstallerDetail(t *testing.T) {
	output := []byte("OBoard 订阅中继\n----------------\n[2/4] 从主控下载中继组件\n下载失败：中继组件\n无法从主控下载 oboard-subscription-relay-linux-amd64.tar.gz.\n\nOBoard 订阅中继操作未完成。\n请根据上方提示处理后重试。\n")
	got := relayUpdateOutputSuffix(output)
	want := ": 下载失败：中继组件 · 无法从主控下载 oboard-subscription-relay-linux-amd64.tar.gz."
	if got != want {
		t.Fatalf("suffix = %q, want %q", got, want)
	}
}

func TestRelayUpdateOutputSuffixEmpty(t *testing.T) {
	if got := relayUpdateOutputSuffix(nil); got != "" {
		t.Fatalf("empty output suffix = %q", got)
	}
}

func TestRelayUpdaterRejectsNonPositiveHeartbeatInterval(t *testing.T) {
	for _, interval := range []string{"0s", "-1s"} {
		if err := runUpdater([]string{"--interval", interval}); err == nil || err.Error() != "heartbeat interval must be positive" {
			t.Fatalf("interval %s: %v", interval, err)
		}
	}
}
