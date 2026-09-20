package automation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/model"
)

func TestPluginChangesetApprovalRechecksOriginIdentity(t *testing.T) {
	for _, mode := range []string{"allowed", "revoked", "missing_resolver", "interactive_resolver", "denied_policy"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			db := openAutomationTestStore(t)
			admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "plugin-approval-test", ProxyPassword: "unused"}
			if err := db.CreateUser(ctx, admin); err != nil {
				t.Fatal(err)
			}
			operator := application.HumanPrincipal(*admin, model.RoleAdmin, application.Principal{}.SourceIP)
			principal := application.PluginPrincipal("test-run", "plugin", &admin.ID, []string{"servers:onboard"}, json.RawMessage(`{"servers":{"mode":"selected","ids":[7]}}`))
			service := NewService(db, capability.NewCatalog())
			registerAutomationTestCapability(service)
			called := false
			service.Register("servers.onboard", func(_ context.Context, effective application.Principal, _ json.RawMessage) (any, error) {
				called = true
				if effective.ID != principal.ID || effective.Interactive || string(effective.ResourceFilter) != string(principal.ResourceFilter) {
					t.Fatalf("approver replaced plugin boundary: %#v", effective)
				}
				return map[string]bool{"applied": true}, nil
			})
			service.SetPluginPrincipalResolver(func(context.Context, *model.AutomationChangeset) (application.Principal, error) {
				return principal, nil
			})
			item := createAutomationTestChangeset(t, service, principal, "plugin-action", json.RawMessage(`{}`))
			validated, err := service.Validate(ctx, principal, item.ID)
			if err != nil || validated.Status != model.ChangesetAwaitingApproval {
				t.Fatalf("plugin without stored APIPrincipal must await approval: %#v %v", validated, err)
			}
			switch mode {
			case "revoked":
				service.SetPluginPrincipalResolver(func(context.Context, *model.AutomationChangeset) (application.Principal, error) {
					return application.Principal{}, errors.New("revoked")
				})
			case "missing_resolver":
				service.SetPluginPrincipalResolver(nil)
			case "interactive_resolver":
				service.SetPluginPrincipalResolver(func(context.Context, *model.AutomationChangeset) (application.Principal, error) {
					p := principal
					p.Interactive = true
					return p, nil
				})
			case "denied_policy":
				if err := db.CreateAPIPrincipal(ctx, &model.APIPrincipal{ID: principal.ID, Name: "plugin policy", Type: model.APIPrincipalPlugin, Enabled: true}); err != nil {
					t.Fatal(err)
				}
				if err := db.UpsertApprovalPolicy(ctx, &model.ApprovalPolicy{ID: "deny-plugin", PrincipalID: principal.ID, Capability: "servers.onboard", Mode: model.ApprovalDenied, ResourceFilter: json.RawMessage(`{}`)}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.Approve(ctx, operator, item.ID, "reviewed")
			if mode == "allowed" {
				if err != nil || result.Status != model.ChangesetSucceeded || !called {
					t.Fatalf("approval failed: %#v %v called=%v", result, err, called)
				}
			} else if err == nil || called {
				t.Fatalf("invalid origin was applied: %#v %v called=%v", result, err, called)
			}
		})
	}
}
