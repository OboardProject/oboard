package application

import (
	"encoding/json"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestAllowsInt64EmptyFilterRemainsUnrestricted(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`null`)} {
		principal := Principal{ResourceFilter: raw}
		if !principal.AllowsInt64("user_ids", 9) || !principal.AllowsInt64("server_ids", 3) {
			t.Fatalf("empty filter %s must stay unrestricted", raw)
		}
	}
}

func TestAllowsInt64PartialFilterDeniesUnmentionedTypes(t *testing.T) {
	principal := Principal{ResourceFilter: json.RawMessage(`{"server_ids":[1]}`)}
	if !principal.AllowsInt64("server_ids", 1) {
		t.Fatal("listed server must remain allowed")
	}
	if principal.AllowsInt64("server_ids", 2) {
		t.Fatal("other server must be denied")
	}
	if principal.AllowsInt64("user_ids", 1) {
		t.Fatal("unmentioned user_ids must be denied")
	}
	if principal.AllowsInt64("proxy_path_ids", 1) {
		t.Fatal("unmentioned proxy_path_ids must be denied")
	}
	if principal.AllowsInt64("subscription_plan_ids", 1) {
		t.Fatal("unmentioned subscription_plan_ids must be denied")
	}
	if principal.AllowsInt64("group_ids", 1) {
		t.Fatal("unmentioned group_ids must be denied")
	}
}

func TestScriptPrincipalEmptyFilterDeniesAllServers(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`null`)} {
		principal := Principal{Type: model.APIPrincipalScript, ResourceFilter: raw}
		if principal.AllowsInt64("server_ids", 1) || principal.AllowsCreate("server") || principal.AllowsGlobal() || principal.AllowsDestructiveOperations() {
			t.Fatalf("script principal with empty filter %s must deny by default", raw)
		}
	}
	human := Principal{ResourceFilter: nil}
	if !human.AllowsInt64("server_ids", 1) {
		t.Fatal("non-script empty filter must stay unrestricted")
	}
}

func TestAllowsInt64NestedBoundaryDeniesUnmentionedTypes(t *testing.T) {
	principal := Principal{ResourceFilter: json.RawMessage(`{"servers":{"mode":"selected","ids":[7]},"destructive_operations":false}`)}
	if !principal.AllowsInt64("server_ids", 7) {
		t.Fatal("selected server must be allowed")
	}
	if principal.AllowsInt64("user_ids", 7) {
		t.Fatal("nested servers-only filter must deny users")
	}
}
