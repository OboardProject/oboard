package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	choices := []string{}
	for _, spec := range telegramNotificationActionSpecs(telegramActionInput{Event: notificationServerExpiry, Context: notificationContext{ServerID: 1}}) {
		if spec.Action == "extend_expiry" {
			choices = append(choices, fmt.Sprintf("%dd%dm", spec.Payload.Days, spec.Payload.Months))
		}
	}
	if strings.Join(choices, ",") != "7d0m,0d1m,0d3m,0d12m" {
		t.Fatalf("extend choices = %v", choices)
	}
}

func TestTelegramIncidentActionSpecs(t *testing.T) {
	active := telegramIncidentActionSpecs(telegramIncidentButtonInput{ID: 5, Version: 2, ServerID: 9, Status: string(model.NodeIncidentActive), Published: []int64{3, 4}})
	if len(active) != 6 || active[0].Action != "view_server" || active[1].Payload.RecoveryPolicy != "auto" || active[2].Payload.DurationMinutes != 60 || active[5].Action != "incident_remove" {
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

var telegramShanghai = time.FixedZone("Asia/Shanghai", 8*3600)

func TestTelegramExtendExpiryChangeset(t *testing.T) {
	for _, tc := range []struct {
		name       string
		days       int
		months     int
		expiry     time.Time
		want       string
		restricted bool
	}{
		{name: "7 days", days: 7, expiry: time.Date(2026, 10, 14, 0, 0, 0, 0, telegramShanghai), want: "2026-10-21"},
		{name: "1 month same date", months: 1, expiry: time.Date(2026, 10, 14, 0, 0, 0, 0, telegramShanghai), want: "2026-11-14"},
		{name: "1 month clamps to month end", months: 1, expiry: time.Date(2027, 1, 31, 0, 0, 0, 0, telegramShanghai), want: "2027-02-28"},
		{name: "3 months", months: 3, expiry: time.Date(2026, 10, 14, 0, 0, 0, 0, telegramShanghai), want: "2027-01-14"},
		{name: "1 year", months: 12, expiry: time.Date(2028, 2, 29, 0, 0, 0, 0, telegramShanghai), want: "2029-02-28"},
		{name: "outside server scope", months: 1, expiry: time.Date(2026, 10, 14, 0, 0, 0, 0, telegramShanghai), want: "2026-10-14", restricted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := openControllerAutomationTestStore(t)
			srv := newTestServer(db, "test-secret", "")
			admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111112", ProxyPassword: "unused"}
			if err := db.CreateUser(ctx, admin); err != nil {
				t.Fatal(err)
			}
			principal := userAutomationPrincipal(t, db, admin.ID)
			if tc.restricted {
				principal.ResourceFilter = json.RawMessage(`{"servers":{"mode":"selected","ids":[]}}`)
			}
			expiry := tc.expiry
			server := &model.Server{Name: "telegram-renewal", Status: model.ServerOffline, ExpiresAt: &expiry}
			if err := db.CreateServer(ctx, server); err != nil {
				t.Fatal(err)
			}
			before, err := db.GetServer(ctx, server.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, execErr := srv.executeTelegramAction(ctx, *admin, model.RoleAdmin, principal, telegramActionPayload{Action: "extend_expiry", ServerID: server.ID, Days: tc.days, Months: tc.months}, "renewal-confirmation")
			if (execErr != nil) != tc.restricted {
				t.Fatalf("execute renewal: %v", execErr)
			}
			stored, err := db.GetServer(ctx, server.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.ExpiresAt == nil || stored.ExpiresAt.In(telegramShanghai).Format("2006-01-02") != tc.want {
				t.Fatalf("expiry = %v, want %s", stored.ExpiresAt, tc.want)
			}
			tasks, err := db.ListTasksByServer(ctx, server.ID, 10)
			if err != nil || len(tasks) != 0 {
				t.Fatalf("renewal queued Agent tasks: %v, error: %v", tasks, err)
			}
			if tc.restricted {
				return
			}
			changeset, err := db.FindAutomationChangesetByIdempotency(ctx, principal.ID, "telegram-action:renewal-confirmation")
			if err != nil {
				t.Fatal(err)
			}
			if changeset.Status != model.ChangesetSucceeded {
				t.Fatalf("changeset status = %s", changeset.Status)
			}
			var revisions map[string]string
			if err := json.Unmarshal(changeset.BaseRevisions, &revisions); err != nil {
				t.Fatal(err)
			}
			if revisions[fmt.Sprintf("server:%d", server.ID)] != before.UpdatedAt.UTC().Format(time.RFC3339Nano) {
				t.Fatalf("changeset did not retain the server revision before renewal: %v", revisions)
			}
		})
	}
}
