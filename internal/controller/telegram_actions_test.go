package controller

import (
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestTelegramNotificationActionSpecs(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		in      telegramActionInput
		actions []string
	}{
		{name: "offline view", in: telegramActionInput{Event: notificationServerOffline, Context: notificationContext{ServerID: 4}}, actions: []string{"view_server"}},
		{name: "deployment retry", in: telegramActionInput{Event: notificationTaskFailed, Context: notificationContext{ServerID: 4, TaskID: 9, TaskType: model.AgentTaskTypeApplyDeployment}}, actions: []string{"view_server", "retry_delivery"}},
		{name: "agent update stays read only", in: telegramActionInput{Event: notificationTaskFailed, Context: notificationContext{ServerID: 4, TaskID: 9, TaskType: model.AgentTaskTypeUpdateAgent}}, actions: []string{"view_server"}},
		{name: "certificate reissue", in: telegramActionInput{Event: notificationCertificateFailed, Context: notificationContext{CertificateID: 3}, CertificateIssueAllowed: true}, actions: []string{"issue_certificate"}},
		{name: "certificate still valid", in: telegramActionInput{Event: notificationCertificateExpiry, Context: notificationContext{CertificateID: 3}}, actions: nil},
		{name: "dns", in: telegramActionInput{Event: notificationDNSSyncFailed, Context: notificationContext{ServerID: 4, InboundID: 8}}, actions: []string{"view_server", "sync_dns"}},
		{name: "clock", in: telegramActionInput{Event: notificationServerClockSkew, Context: notificationContext{ServerID: 4}}, actions: []string{"view_server", "recheck_time"}},
		{name: "backup", in: telegramActionInput{Event: notificationBackupFailed}, actions: []string{"create_backup"}},
		{name: "expiry", in: telegramActionInput{Event: notificationServerExpiry, Context: notificationContext{ServerID: 4}}, actions: []string{"view_server", "extend_expiry", "extend_expiry", "extend_expiry", "extend_expiry"}},
		{name: "audit refresh", in: telegramActionInput{Event: notificationUserRisk, Context: notificationContext{UserID: 2}}, actions: []string{"refresh_audit"}},
		{name: "announcement", in: telegramActionInput{Event: notificationAdminAnnouncement}, actions: nil},
		{name: "controller update", in: telegramActionInput{Event: notificationUpdateFailed}, actions: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := telegramNotificationActionSpecs(tc.in)
			if len(got) != len(tc.actions) {
				t.Fatalf("actions = %#v, want %v", labels(got), tc.actions)
			}
			for i, action := range tc.actions {
				if got[i].Action != action {
					t.Fatalf("action %d = %s, want %s", i, got[i].Action, action)
				}
			}
		})
	}
	issued := model.Certificate{ID: 1, Status: "issued", ChallengeType: "dns-01", NotAfter: telegramTimePtr(now.Add(30 * 24 * time.Hour))}
	if certificateTelegramReissueAllowed(issued, now) {
		t.Fatal("issued certificate with 30 days left should not offer reissue")
	}
	soon := issued
	soon.NotAfter = telegramTimePtr(now.Add(3 * 24 * time.Hour))
	if !certificateTelegramReissueAllowed(soon, now) {
		t.Fatal("issued certificate inside 7 days should offer reissue")
	}
	imported := soon
	imported.ChallengeType = "imported"
	if certificateTelegramReissueAllowed(imported, now) {
		t.Fatal("imported certificate should not offer reissue")
	}
	days := []int{}
	for _, spec := range telegramNotificationActionSpecs(telegramActionInput{Event: notificationServerExpiry, Context: notificationContext{ServerID: 1}}) {
		if spec.Action == "extend_expiry" {
			days = append(days, spec.Payload.Days)
		}
	}
	if len(days) != 4 || days[0] != 7 || days[1] != 30 || days[2] != 90 || days[3] != 365 {
		t.Fatalf("extend days = %v", days)
	}
}

func TestTelegramIncidentActionSpecs(t *testing.T) {
	active := telegramIncidentActionSpecs(telegramIncidentButtonInput{ID: 5, Version: 2, ServerID: 9, Status: string(model.NodeIncidentActive), Published: []int64{3, 4}})
	if len(active) != 4 || active[0].Action != "view_server" || active[1].Payload.RecoveryPolicy != "manual" || active[2].Payload.RecoveryPolicy != "auto" || active[3].Action != "incident_remove" {
		t.Fatalf("active specs = %#v", active)
	}
	restored := telegramIncidentActionSpecs(telegramIncidentButtonInput{ID: 5, Version: 3, ServerID: 9, Status: string(model.NodeIncidentResolved), Isolations: []struct {
		ID   int64
		Name string
	}{{ID: 8, Name: "入口"}}})
	if len(restored) != 2 || restored[1].Action != "incident_restore" || restored[1].Payload.IsolationID != 8 {
		t.Fatalf("resolved specs = %#v", restored)
	}
}

func labels(specs []telegramActionSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.Action)
	}
	return out
}

func telegramTimePtr(value time.Time) *time.Time { return &value }
