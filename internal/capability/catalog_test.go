package capability

import (
	"encoding/json"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
)

func TestListMCPFiltersCapabilitiesNotExposedToMCP(t *testing.T) {
	catalog := &Catalog{items: map[string]Descriptor{
		"rest.only":  {Name: "rest.only", Description: "REST only", InputSchema: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{}`), RequiredScopes: []string{"rest:write"}, ReadOnly: false, MCPEnabled: false},
		"shared.get": {Name: "shared.get", Description: "shared read", InputSchema: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{}`), RequiredScopes: []string{"shared:read"}, ReadOnly: true, MCPEnabled: true},
		"no.scope":   {Name: "no.scope", Description: "not authorized", InputSchema: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{}`), RequiredScopes: []string{"missing:read"}, ReadOnly: true, MCPEnabled: true},
	}}
	principal := application.Principal{ID: "prn_test", Scopes: []string{"rest:write", "shared:read"}}

	all := catalog.List(principal)
	if len(all) != 2 {
		t.Fatalf("List returned %d capabilities, want 2: %#v", len(all), all)
	}
	mcp := catalog.ListMCP(principal)
	if len(mcp) != 1 || mcp[0].Name != "shared.get" {
		t.Fatalf("ListMCP returned %#v, want only shared.get", mcp)
	}
}
