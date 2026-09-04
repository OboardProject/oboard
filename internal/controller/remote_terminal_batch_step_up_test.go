package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

// One step-up authentication scoped to an explicit server set must open a terminal on every
// server it lists, exactly once each, and must never reach a server outside the set.
func TestServerSetStepUpOpensOneTerminalPerListedServer(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "terminal-batch.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	defer app.Close()
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()

	token, _, _ := realtimeLogin(t, httpServer.URL)
	ctx := context.Background()

	nodes := make([]*model.Server, 0, 3)
	for index, name := range []string{"batch-a", "batch-b", "batch-c"} {
		node := &model.Server{
			Name: name, AgentID: "agent-" + name, AgentTokenHash: security.HashSecret("agent-token"),
			ListenIP: "0.0.0.0", PortRangeStart: 10000 + index*100, PortRangeEnd: 10099 + index*100,
			Status: model.ServerOnline,
		}
		if err := db.CreateServer(ctx, node); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertServerRemoteAccessStatus(ctx, node.ID, model.RemoteAccessReport{
			Capabilities: []string{model.RemoteAccessCapabilityTerminal},
			LocalMode:    model.RemoteAccessModeStandard,
		}); err != nil {
			t.Fatal(err)
		}
		live := make(chan any, 4)
		app.registerAgentLive(node.ID, live)
		defer app.unregisterAgentLive(node.ID, live)
		nodes = append(nodes, node)
	}

	post := func(path string, body any) (*http.Response, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		request, _ := http.NewRequest(http.MethodPost, httpServer.URL+path, bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var decoded map[string]any
		_ = json.NewDecoder(response.Body).Decode(&decoded)
		return response, decoded
	}

	// Password confirmation stays enabled: this is the path a batch run has to satisfy.
	setSelected := itoa(nodes[0].ID) + "," + itoa(nodes[1].ID)
	beginRes, begin := post("/api/v1/ui/auth/step-up/begin", map[string]any{
		"purpose":  model.StepUpPurposeRemoteTerminal,
		"resource": map[string]any{"type": "server_set", "id": setSelected},
	})
	if beginRes.StatusCode != http.StatusOK {
		t.Fatalf("step-up begin status = %d body=%#v", beginRes.StatusCode, begin)
	}
	challengeID, _ := begin["challenge_id"].(string)
	if challengeID == "" {
		t.Fatalf("step-up begin = %#v", begin)
	}
	finishRes, finish := post("/api/v1/ui/auth/step-up/password", map[string]any{
		"challenge_id": challengeID, "password": "very-secure-password",
	})
	if finishRes.StatusCode != http.StatusOK {
		t.Fatalf("step-up password status = %d body=%#v", finishRes.StatusCode, finish)
	}
	stepUpToken, _ := finish["step_up_token"].(string)
	if stepUpToken == "" {
		t.Fatalf("step-up password = %#v", finish)
	}

	createTerminal := func(serverID int64) (*http.Response, map[string]any) {
		return post("/api/v1/ui/servers/"+itoa(serverID)+"/terminal/sessions", map[string]any{
			"step_up_token": stepUpToken, "cols": 80, "rows": 24,
		})
	}

	for _, node := range nodes[:2] {
		response, body := createTerminal(node.ID)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("server %d create status = %d body=%#v", node.ID, response.StatusCode, body)
		}
		if sessionID, _ := body["session_id"].(string); sessionID == "" {
			t.Fatalf("server %d create = %#v", node.ID, body)
		}
	}

	// Same token, same server: the per-server slot is single use.
	replayRes, replay := createTerminal(nodes[0].ID)
	if replayRes.StatusCode != http.StatusForbidden {
		t.Fatalf("replay status = %d body=%#v", replayRes.StatusCode, replay)
	}

	// Same token, a server outside the authorized set.
	outsideRes, outside := createTerminal(nodes[2].ID)
	if outsideRes.StatusCode != http.StatusForbidden {
		t.Fatalf("outside-set status = %d body=%#v", outsideRes.StatusCode, outside)
	}
}

func TestStepUpServerSetCanonicalizationAndBounds(t *testing.T) {
	canonical, err := canonicalStepUpServerSet(" 7, 3 ,7,11 ")
	if err != nil {
		t.Fatal(err)
	}
	if canonical != "3,7,11" {
		t.Fatalf("canonical = %q", canonical)
	}
	if _, err := canonicalStepUpServerSet(""); err == nil {
		t.Fatal("empty server_set must be rejected")
	}
	if _, err := canonicalStepUpServerSet("0"); err == nil {
		t.Fatal("non-positive server id must be rejected")
	}
	if _, err := canonicalStepUpServerSet("a,1"); err == nil {
		t.Fatal("non-numeric server id must be rejected")
	}
	oversized := ""
	for index := 1; index <= stepUpServerSetMax+1; index++ {
		if index > 1 {
			oversized += ","
		}
		oversized += itoa(int64(index))
	}
	if _, err := canonicalStepUpServerSet(oversized); err == nil {
		t.Fatalf("server_set beyond %d must be rejected", stepUpServerSetMax)
	}

	ids, err := parseStepUpServerSet("3,7,11")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != 3 || ids[2] != 11 {
		t.Fatalf("parsed = %#v", ids)
	}
	if _, err := parseStepUpServerSet("3,3"); err == nil {
		t.Fatal("duplicate server id must be rejected")
	}
}
