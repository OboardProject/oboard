package capability

import (
	"encoding/json"
	"strings"
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

func TestDefaultMCPCatalogSchemasAreClosedAndTyped(t *testing.T) {
	catalog := NewCatalog()
	principal := application.Principal{Scopes: []string{"*"}}
	items := catalog.ListMCP(principal)
	if len(items) == 0 {
		t.Fatal("default MCP catalog is empty")
	}
	for _, item := range items {
		if item.Version == "" || item.Documentation == "" {
			t.Errorf("%s is missing version or documentation metadata", item.Name)
		}
		for label, raw := range map[string]json.RawMessage{"input": item.InputSchema, "output": item.OutputSchema} {
			var schema any
			if len(raw) == 0 || json.Unmarshal(raw, &schema) != nil {
				t.Errorf("%s %s schema is not valid JSON: %s", item.Name, label, raw)
				continue
			}
			root, ok := schema.(map[string]any)
			if !ok || root["type"] == nil {
				t.Errorf("%s %s schema has no root type: %s", item.Name, label, raw)
			}
			if strings.TrimSpace(string(raw)) == "{}" || strings.Contains(string(raw), `"additionalProperties":true`) {
				t.Errorf("%s %s schema is open-ended: %s", item.Name, label, raw)
			}
		}
		if item.RiskClass >= 4 && item.ApprovalPolicy == "automatic" {
			t.Errorf("%s allows automatic risk 4 approval", item.Name)
		}
	}
}

func TestDefaultCatalogDoesNotExposeEscapeCapabilities(t *testing.T) {
	for _, forbidden := range []string{"raw_api", "raw_sql", "raw_agent_task", "shell.execute", "ssh.connect", "admins.delete", "secrets.export"} {
		if _, ok := NewCatalog().Get(forbidden); ok {
			t.Errorf("forbidden capability %q is present", forbidden)
		}
	}
}
