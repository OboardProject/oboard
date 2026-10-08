package capability

import (
	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"testing"
)

func TestRuntimeSecurityCatalogAuthorization(t *testing.T) {
	catalog := NewCatalog()
	reader := application.Principal{Role: model.RoleViewer, Scopes: []string{"servers:read"}}
	for _, action := range []string{"read", "update", "check"} {
		name := "servers.runtime_security." + action
		descriptor, ok := catalog.Get(name)
		if !ok || !descriptor.MCPEnabled || descriptor.ResourceEvaluator != "server_ids" {
			t.Fatal(name, descriptor)
		}
		_, allowed := catalog.Authorize(reader, name)
		if allowed != (action == "read") {
			t.Fatal("viewer authorization", name, allowed)
		}
		if action != "read" && (!descriptor.Executable || descriptor.ReadOnly || descriptor.ApprovalPolicy != "required") {
			t.Fatal("write bypasses changesets", name)
		}
	}
}
