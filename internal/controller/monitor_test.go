package controller

import (
	"testing"
	"time"
)

func TestJitteredMonitorIntervalIsBounded(t *testing.T) {
	interval := 30 * time.Second
	for range 100 {
		delay := jitteredMonitorInterval(interval)
		if delay < interval || delay > interval+interval/10 {
			t.Fatalf("jittered delay = %s, want [%s, %s]", delay, interval, interval+interval/10)
		}
	}
}

func TestNotificationWakeCoalescesWhileMonitorIsActive(t *testing.T) {
	server := &Server{notificationWake: make(chan struct{}, 1)}
	server.monitorStarted.Store(true)
	server.wakeNotificationDelivery(t.Context())
	server.wakeNotificationDelivery(t.Context())
	if got := len(server.notificationWake); got != 1 {
		t.Fatalf("notification wake count = %d, want 1", got)
	}
}
