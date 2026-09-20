package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/auditactivity"
	"github.com/OboardProject/oboard/internal/auditrisk"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestAccountActivityFakeAgentToSnapshot(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "activity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	enableTestAudit(t, db)
	if err = db.InitAccountActivityPipelineSchema(ctx); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "activity-node", AgentID: "activity-agent", AgentTokenHash: security.HashSecret("activity-token"), ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true, KernelCapabilities: []string{"account_activity_v1"}}
	if err = db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "activity-entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: "{}", Enabled: true}
	if err = db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "activity-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused-password"}
	if err = db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inbound.ID)
	sut := newTestServer(db, "activity-test-secret", "")
	start := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	send := func(report accountActivityWireReport, now time.Time, token string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(report)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/account-activity", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Agent-ID", server.AgentID)
		rr := httptest.NewRecorder()
		sut.agentAccountActivityAt(rr, req, now)
		return rr
	}
	var last accountActivityWireReport
	for minute := 0; minute < 32; minute++ {
		at := start.Add(time.Duration(minute) * time.Minute)
		if err = sut.recordAccountActivityExpectations(ctx, at); err != nil {
			t.Fatal(err)
		}
		report := accountActivityWireReport{CollectorStartedAt: start.Add(-time.Minute).UnixNano(), CollectorBootID: "0123456789abcdef0123456789abcdef", StreamType: "kernel", Sequence: int64(minute + 1), MinuteUnix: at.Unix(), ClockState: "aligned", Complete: true}
		for source := 0; source < 8; source++ {
			report.Items = append(report.Items, accountActivityWireItem{UserID: user.ID, InboundID: inbound.ID, SourcePrefix: fmt.Sprintf("8.8.%d.0/24", source), ActivityBits: 7, UploadBytes: 16384})
		}
		rr := send(report, at.Add(61*time.Second), "activity-token")
		if rr.Code != 200 {
			t.Fatalf("report %d: %d %s", minute, rr.Code, rr.Body.String())
		}
		if _, err = db.ApplyPendingAccountActivity(ctx, 64, at.Add(61*time.Second)); err != nil {
			t.Fatal(err)
		}
		last = report
		if minute >= 29 {
			if err := sut.queueAccountActivityDirty(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := sut.evaluateAccountAuditWork(ctx, at.Add(61*time.Second), 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := start.Add(32*time.Minute + time.Second)
	if rr := send(last, now, "activity-token"); rr.Code != 200 || !bytes.Contains(rr.Body.Bytes(), []byte(`"duplicate":true`)) {
		t.Fatalf("duplicate: %d %s", rr.Code, rr.Body.String())
	}
	if err = sut.queueAccountActivityDirty(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = sut.evaluateAccountAuditWork(ctx, now, 0); err != nil {
		t.Fatal(err)
	}
	page, err := db.ListAccountAuditSnapshots(ctx, store.AccountAuditQuery{UserID: user.ID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	rows := page.Items.([]store.AccountAuditRow)
	var snapshot auditrisk.Snapshot
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	if err = json.Unmarshal(rows[0].Snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Activity == nil || snapshot.Activity.Lower != 100 || snapshot.Activity.Upper != 100 {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	if snapshot.AutomaticActionEligible {
		t.Fatal("behavior must remain alert-only")
	}
	notifications, err := db.ListAccountAuditNotifications(ctx, now, 10)
	if err != nil || len(notifications) != 1 {
		t.Fatalf("durable event outbox: %+v %v", notifications, err)
	}
	var notificationSnapshot auditrisk.Snapshot
	if err := json.Unmarshal(notifications[0].Snapshot, &notificationSnapshot); err != nil {
		t.Fatal(err)
	}
	if notificationSnapshot.Activity.Lower != snapshot.Activity.Lower {
		t.Fatal("notification rescored evidence")
	}
	sut.monitorStarted.Store(true)
	channel := &model.NotificationChannel{OwnerUserID: user.ID, Name: "activity-alert", Type: "bark", Enabled: true, Events: notificationUserRisk, ConfigJSON: "{}"}
	badTemplates, _ := json.Marshal(map[string]model.NotificationTemplate{notificationUserRisk: {Title: "{{", Body: "risk"}})
	channel.TemplatesJSON = string(badTemplates)
	if err := db.CreateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	if err := sut.deliverAccountAuditNotifications(ctx, now); err == nil {
		t.Fatal("invalid template unexpectedly completed notification")
	}
	pending, err := db.ListAccountAuditNotifications(ctx, now, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("failed outbox delivery was not retryable: %+v %v", pending, err)
	}
	channel.TemplatesJSON = "{}"
	if err := db.UpdateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := sut.queueAccountAuditNotification(ctx, pending[0], now); err != nil {
			t.Fatal(err)
		}
	}
	if err := sut.deliverAccountAuditNotifications(ctx, now); err != nil {
		t.Fatal(err)
	}
	deliveries, err := db.ListPendingNotificationDeliveries(ctx, now, 10)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("channel delivery must be durable and idempotent: %+v %v", deliveries, err)
	}
	notifications, err = db.ListAccountAuditNotifications(ctx, now, 10)
	if err != nil || len(notifications) != 0 {
		t.Fatalf("outbox not completed: %+v %v", notifications, err)
	}

	configured, err := db.GetAccountAuditPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	configured.SourceGrouping.IPv4Bits = 16
	configured.SourceGrouping.Epoch++
	if _, err = db.SetAccountAuditPolicy(ctx, configured, now); err != nil {
		t.Fatal(err)
	}
	if rr := send(last, now, "activity-token"); rr.Code != 200 || !bytes.Contains(rr.Body.Bytes(), []byte(`"duplicate":true`)) {
		t.Fatalf("retry after source policy change: %d %s", rr.Code, rr.Body.String())
	}
	last.Items[0], last.Items[1] = last.Items[1], last.Items[0]
	if rr := send(last, now, "activity-token"); rr.Code != 200 || !bytes.Contains(rr.Body.Bytes(), []byte(`"duplicate":true`)) {
		t.Fatalf("reordered retry after source policy change: %d %s", rr.Code, rr.Body.String())
	}
	last.Items[0].UploadBytes++
	if rr := send(last, now, "activity-token"); rr.Code != 409 {
		t.Fatalf("conflict status %d", rr.Code)
	}
	if rr := send(last, now, "wrong-token"); rr.Code == 200 {
		t.Fatal("unauthenticated report accepted")
	}
	last.Sequence++
	last.Items[0].SourcePrefix = "8.8.8.1/24"
	if rr := send(last, now, "activity-token"); rr.Code != 400 {
		t.Fatalf("noncanonical source accepted: %d", rr.Code)
	}
	later := now.Add(35 * time.Minute)
	if _, err := sut.queueAccountActivityExpiry(ctx, later, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := sut.evaluateAccountAuditWork(ctx, later, 0); err != nil {
		t.Fatal(err)
	}
	page, err = db.ListAccountAuditSnapshots(ctx, store.AccountAuditQuery{UserID: user.ID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	var expired auditrisk.Snapshot
	if err := json.Unmarshal(page.Items.([]store.AccountAuditRow)[0].Snapshot, &expired); err != nil || !expired.AsOf.Equal(later) {
		t.Fatalf("snapshot did not advance without reports: %+v %v", expired, err)
	}
	events, err := db.ListAccountAuditEvents(ctx, store.AccountAuditQuery{UserID: user.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events.Items.([]store.AccountAuditEvent) {
		if event.Status == "recovered" {
			t.Fatal("missing reports falsely recovered an event")
		}
	}
	if err := db.SetSetting(ctx, settingAuditEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	if rr := send(last, now, "activity-token"); rr.Code != 200 || !bytes.Contains(rr.Body.Bytes(), []byte(`"terminal":true`)) || !bytes.Contains(rr.Body.Bytes(), []byte(`"accepted":false`)) {
		t.Fatalf("disabled audit: %d %s", rr.Code, rr.Body.String())
	}
}

func TestAccountActivityCanonicalSource(t *testing.T) {
	for _, raw := range []string{"8.8.8.1/24", "8.8.8.0/25", "::ffff:8.8.8.0/120", "10.0.0.0/24", "2001:4860:1::/64", "127.0.0.0/24"} {
		if _, err := accountActivitySourceGroup("secret", 1, raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	a, err := accountActivitySourceGroup("secret", 1, "8.8.8.0/24")
	if err != nil {
		t.Fatal(err)
	}
	key, policy := auditactivity.ControllerSourcePolicy("secret")
	subscriptionGroup, err := auditactivity.SourceGroup(key, 1, "8.8.8.42", true, policy)
	if err != nil || a != subscriptionGroup {
		t.Fatal("connection and subscription source identities differ")
	}
	b, err := accountActivitySourceGroup("secret", 2, "8.8.8.0/24")
	if err != nil || a == b {
		t.Fatal("account source scope")
	}
}
