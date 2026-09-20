package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestConfigurationSyncPreparationDiagnostics(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{fmt.Errorf("token=secret SQL SELECT password=hunter2: %w", os.ErrPermission), "filesystem_permission"},
		{fmt.Errorf("token=secret SQL SELECT password=hunter2: %w", context.DeadlineExceeded), "deadline_exceeded"},
		{errors.New("token=secret SQL SELECT password=hunter2 cause A"), "unknown"},
		{errors.New("token=secret SQL SELECT password=hunter2 cause B"), "unknown"},
	} {
		diagnostic := configurationPrepareDiagnostic(tc.err)
		if !strings.Contains(diagnostic, "reason_code "+tc.reason+" ") {
			t.Fatal(diagnostic)
		}
		for _, forbidden := range []string{"token", "secret", "SQL", "SELECT", "password", "hunter2", "cause A", "cause B"} {
			if strings.Contains(diagnostic, forbidden) {
				t.Fatalf("diagnostic leaks %q", forbidden)
			}
		}
		if seen[diagnostic] || diagnostic != configurationPrepareDiagnostic(tc.err) {
			t.Fatal("unstable or indistinguishable diagnostic")
		}
		seen[diagnostic] = true
	}
}

func TestConfigurationSyncProblemClassificationAndMCP(t *testing.T) {
	for _, message := range []string{"secret SQL token", "completely different"} {
		p := configurationPrepareProblem(42, fmt.Errorf("%w: %s", core.ErrInvalidDesiredState, message))
		if p.Code != "invalid_desired_state" || p.RetryPolicy != "after_change" || p.Resources[0].ID != "42" || strings.Contains(p.Message, message) {
			t.Fatalf("problem: %#v", p)
		}
	}
	if p := configurationPrepareProblem(42, errors.New("token=secret")); strings.Contains(p.Message, "secret") {
		t.Fatal(p)
	}
	if p := configurationPrepareProblem(42, fmt.Errorf("wrapped: %w", missingDNSCredentialError{})); p.Code != missingDNSCredentialCode {
		t.Fatal(p)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "sync-problems", Status: model.ServerOnline, PortRangeStart: 10000, PortRangeEnd: 20000}
	if err = db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkConfigurationSyncPending(ctx, 10, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ClaimConfigurationSync(ctx, server.ID, 10); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	srv.recordConfigurationPrepareError(ctx, store.ConfigurationSyncState{ServerID: server.ID, WantedRevision: 10}, fmt.Errorf("%w: token=secret SQL SELECT password=hunter2", core.ErrInvalidDesiredState))
	if !strings.Contains(logs.String(), fmt.Sprintf("server %d revision 10", server.ID)) || !strings.Contains(logs.String(), "reason_code invalid_desired_state") || strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "SELECT") || strings.Contains(logs.String(), "hunter2") {
		t.Fatalf("unexpected diagnostic: %s", logs.String())
	}
	input := json.RawMessage(fmt.Sprintf(`{"server_id":%d}`, server.ID))
	principal := application.Principal{Scopes: []string{"tasks:read"}}
	output, err := srv.queryMCPCapabilityFallback(ctx, principal, "configuration_sync.get", input)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(output)
	if err != nil || !strings.Contains(string(data), `"code":"invalid_desired_state"`) || strings.Contains(string(data), "secret") {
		t.Fatalf("output %s %v", data, err)
	}
	descriptor, ok := srv.capabilities.Get("configuration_sync.get")
	if !ok || !strings.Contains(string(descriptor.OutputSchema), `"problems"`) {
		t.Fatal("missing MCP schema")
	}
	if err := db.MarkConfigurationSyncQueued(ctx, server.ID, 10, 99, 0, "test"); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, 99, false, "token=secret SQL SELECT password=hunter2"); err != nil {
		t.Fatal(err)
	}
	output, err = srv.queryMCPCapabilityFallback(ctx, principal, "configuration_sync.get", input)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(output)
	if err != nil || strings.Contains(string(data), "hunter2") || strings.Contains(string(data), "SELECT") || strings.Contains(string(data), "secret") {
		t.Fatalf("legacy task failure leaked through MCP: %s %v", data, err)
	}
	dns, ok := srv.capabilities.Get("servers.dns_test")
	if !ok || !strings.Contains(string(dns.OutputSchema), `"operation"`) || !strings.Contains(string(dns.OutputSchema), `"run"`) {
		t.Fatal("DNS operation output missing from schema")
	}
	grant := mcpauth.GrantPolicy{GrantID: "sync-reader", AccessLevel: mcpauth.AccessRead, ResourceBoundary: mcpauth.ResourceBoundary{Version: mcpauth.ResourceBoundaryVersion, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: "selected", IDs: []string{"999999"}}}}}
	principal.ResourceFilter = application.ResourceFilterFromBoundary(grant.ResourceBoundary)
	if _, err = srv.queryMCPCapabilityFallback(ctx, principal, "configuration_sync.get", input); err == nil {
		t.Fatal("out-of-scope query accepted")
	}
}
