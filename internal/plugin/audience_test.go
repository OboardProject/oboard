package plugin

import (
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

const audienceManifest = `{
  "id": "acme.audience",
  "name": "Audience",
  "version": "1.0.0",
  "description": "",
  "runtime": "oboard-js",
  "entry": "main.js",
  "capabilities": ["users.read", "users.notify", "plans.read", "plans.notify"],
  "triggers": {"schedule": false},
  "limits": {}
}`

func TestPluginCanReadUsersAndNotifyPlans(t *testing.T) {
	h := newHarness(t)
	candidate := h.candidate(audienceManifest, model.PluginPublisherLocal)
	installed, err := h.service.InstallPackage(h.ctx, adminPrincipal(), candidate, candidate.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	grant := Grant{Capabilities: map[string]CapabilityGrant{
		CapUsersRead: {Users: []int64{8}}, CapUsersNotify: {Users: []int64{8}},
		CapPlansRead: {Plans: []int64{4}}, CapPlansNotify: {Plans: []int64{4}},
	}}
	if _, err := h.service.SetGrant(h.ctx, adminPrincipal(), installed.InstanceID, 0, grant); err != nil {
		t.Fatal(err)
	}
	if err := h.service.SetInstallationEnabled(h.ctx, adminPrincipal(), installed.InstallationID, true); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := h.service.UpdateInstance(h.ctx, adminPrincipal(), installed.InstanceID, InstanceUpdate{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	lease := h.startRun(installed.InstanceID)
	listed := h.call(lease, "users.list", map[string]any{})
	if !listed.OK || strings.Contains(string(listed.Result), "bob") || !strings.Contains(string(listed.Result), "alice") {
		t.Fatalf("user list must stay inside the grant: %s", listed.Result)
	}
	expectCode(t, h.call(lease, "users.notify", map[string]any{"user_ids": []string{"9"}, "title": "hi"}), CodeResourceDenied)
	notified := h.call(lease, "users.notify", map[string]any{"user_ids": []string{"8"}, "title": "你好", "body": "到期提醒"})
	if !notified.OK || !strings.Contains(string(notified.Result), `"queued":1`) {
		t.Fatalf("user notify failed: %+v", notified)
	}
	members := h.call(lease, "plans.users", map[string]any{"plan_id": "4"})
	if !members.OK || !strings.Contains(string(members.Result), "alice") {
		t.Fatalf("plan users failed: %+v", members)
	}
	planNote := h.call(lease, "plans.notify", map[string]any{"plan_id": "4", "title": "套餐通知"})
	if !planNote.OK {
		t.Fatalf("plan notify failed: %+v", planNote)
	}
	h.host.mu.Lock()
	defer h.host.mu.Unlock()
	if len(h.host.notifications) != 2 || h.host.notifications[0] != "你好|到期提醒" {
		t.Fatalf("notifications were not queued: %+v", h.host.notifications)
	}
}
