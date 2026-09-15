package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestRefreshRuntimeRequiresConfirmAndRebuildsEnrolledServers(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	enrolled := &model.Server{
		Name: "edge-online", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("token-edge"),
		ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline,
	}
	if err := db.CreateServer(ctx, enrolled); err != nil {
		t.Fatal(err)
	}
	unenrolled := &model.Server{
		Name: "edge-new", ListenIP: "0.0.0.0", PortRangeStart: 20001, PortRangeEnd: 30000, Status: model.ServerOffline,
	}
	if err := db.CreateServer(ctx, unenrolled); err != nil {
		t.Fatal(err)
	}

	request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{
		"username": "viewer", "password": "long-user-password", "role": "viewer", "status": "active",
	}, http.StatusCreated)
	viewerToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-user-password"}, http.StatusOK)["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/ui/deployments/refresh-runtime", viewerToken, map[string]any{"confirm": true}, http.StatusForbidden)
	request(t, h, http.MethodPost, "/api/v1/ui/deployments/refresh-runtime", adminToken, map[string]any{}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/ui/deployments/refresh-runtime", adminToken, map[string]any{"confirm": false}, http.StatusBadRequest)

	beforeRevision, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result := request(t, h, http.MethodPost, "/api/v1/ui/deployments/refresh-runtime", adminToken, map[string]any{"confirm": true}, http.StatusAccepted)
	if result["queued_tasks"].(float64) != 2 || result["queued_servers"].(float64) != 1 || result["failed_immediate"].(float64) != 1 {
		t.Fatalf("unexpected refresh summary: %#v", result)
	}
	if result["delivery_retried"].(float64) != 1 || result["skipped_unenrolled"].(float64) != 1 {
		t.Fatalf("unexpected delivery summary: %#v", result)
	}
	afterRevision, err := db.ConfigurationRevision(ctx)
	if err != nil || afterRevision != beforeRevision {
		t.Fatalf("runtime refresh changed configuration revision: before=%d after=%d err=%v", beforeRevision, afterRevision, err)
	}

	tasks, err := db.ListTasksByServer(ctx, enrolled.ID, 10)
	if err != nil || len(tasks) == 0 {
		t.Fatalf("enrolled tasks: %#v err=%v", tasks, err)
	}
	var deployment *model.AgentTask
	for index := range tasks {
		if tasks[index].Type == model.AgentTaskTypeApplyDeployment {
			deployment = &tasks[index]
			break
		}
	}
	if deployment == nil {
		t.Fatalf("enrolled server received no deployment task: %#v", tasks)
	}
	var payload model.DeploymentTaskPayload
	if err := json.Unmarshal([]byte(deployment.PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.ForceRefresh || !payload.ConfigChanged || payload.TriggerReason != "runtime_refresh" {
		t.Fatalf("enrolled refresh payload: task=%#v payload=%#v", deployment, payload)
	}
	if payload.Version <= 0 || payload.Version != deployment.ConfigVersion {
		t.Fatalf("refresh did not allocate a config version: task=%#v payload=%#v", deployment, payload)
	}

	authState, err := db.AuthorizationState(ctx, enrolled.ID)
	if err != nil || authState.PendingReason != store.AuthorizationPendingDelivering {
		t.Fatalf("enrolled authorization was not marked pending: %#v err=%v", authState, err)
	}
	usersState, err := db.RuntimeUserState(ctx, enrolled.ID)
	if err != nil || usersState.PendingReason != store.RuntimeUsersPendingDelivering {
		t.Fatalf("enrolled runtime users were not marked pending: %#v err=%v", usersState, err)
	}
}

// The refresh exists for a node whose reported state is not trusted, so every
// version-gated lane has to leave it under a version that node cannot already
// hold. Redelivering the current version would be answered as "unchanged" by a
// healthy gate and rebuild nothing.
func TestRefreshRuntimeReissuesEveryDeliveryLaneVersion(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	server := &model.Server{
		Name: "edge-online", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("token-edge"),
		ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline,
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	// Seed one confirmed state per lane, all describing content that does not
	// change across the refresh.
	const authDigest, usersDigest, probeDigest = "auth-digest", "users-digest", "probe-digest"
	authKeys := []string{"credential-1"}
	if _, err := db.EvaluateAuthorizationDesired(ctx, server.ID, 1, authDigest, authKeys, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordAuthorizationConfirmation(ctx, server.ID, 1, 1, authDigest, "boot-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordRuntimeUserDesired(ctx, server.ID, usersDigest, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordRuntimeUserConfirmation(ctx, server.ID, 1, usersDigest, "boot-1"); err != nil {
		t.Fatal(err)
	}
	probeVersion, err := db.LatencyProbePlanVersion(ctx, server.ID, probeDigest, 0)
	if err != nil || probeVersion <= 0 {
		t.Fatalf("probe plan version: %d err=%v", probeVersion, err)
	}
	beforeTrafficRevision, err := db.TrafficPolicyRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	result := request(t, h, http.MethodPost, "/api/v1/ui/deployments/refresh-runtime", adminToken, map[string]any{"confirm": true}, http.StatusAccepted)
	if result["reissued_servers"].(float64) != 1 {
		t.Fatalf("unexpected reissue summary: %#v", result)
	}
	if int64(result["traffic_policy_revision"].(float64)) <= int64(beforeTrafficRevision) {
		t.Fatalf("refresh did not advance the traffic policy revision: before=%d result=%#v", beforeTrafficRevision, result)
	}

	// Same content, evaluated again: each lane must answer with a new version.
	authEval, err := db.EvaluateAuthorizationDesired(ctx, server.ID, 1, authDigest, authKeys, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !authEval.Changed || authEval.State.DesiredRevision <= 1 {
		t.Fatalf("authorization revision was not reissued: %#v", authEval)
	}
	usersEval, err := db.RecordRuntimeUserDesired(ctx, server.ID, usersDigest, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !usersEval.Changed || usersEval.State.DesiredRevision <= 1 {
		t.Fatalf("runtime users revision was not reissued: %#v", usersEval)
	}
	nextProbeVersion, err := db.LatencyProbePlanVersion(ctx, server.ID, probeDigest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if nextProbeVersion <= probeVersion {
		t.Fatalf("probe plan version was not reissued: before=%d after=%d", probeVersion, nextProbeVersion)
	}
}

// reissueServerDelivery is the part that makes the refresh more than a
// redelivery: with no content change at all, every lane must still come back
// under a version the node cannot already be holding.
func TestReissueServerDeliveryAdvancesEveryLaneWithoutAContentChange(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	s := newTestServer(db, "test-secret", "")

	server := &model.Server{
		Name: "edge-online", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("token-edge"),
		ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline,
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	const authDigest, usersDigest, probeDigest = "auth-digest", "users-digest", "probe-digest"
	authKeys := []string{"credential-1"}
	if _, err := db.EvaluateAuthorizationDesired(ctx, server.ID, 1, authDigest, authKeys, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordAuthorizationConfirmation(ctx, server.ID, 1, 1, authDigest, "boot-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordRuntimeUserDesired(ctx, server.ID, usersDigest, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordRuntimeUserConfirmation(ctx, server.ID, 1, usersDigest, "boot-1"); err != nil {
		t.Fatal(err)
	}
	probeVersion, err := db.LatencyProbePlanVersion(ctx, server.ID, probeDigest, 0)
	if err != nil || probeVersion <= 0 {
		t.Fatalf("probe plan version: %d err=%v", probeVersion, err)
	}

	if err := s.reissueServerDelivery(ctx, server.ID); err != nil {
		t.Fatal(err)
	}

	authEval, err := db.EvaluateAuthorizationDesired(ctx, server.ID, 1, authDigest, authKeys, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !authEval.Changed || authEval.State.DesiredRevision != 2 {
		t.Fatalf("authorization revision was not reissued for identical grants: %#v", authEval)
	}
	if len(authEval.DeniedKeys) != 0 {
		t.Fatalf("reissuing the same grants must deny nothing: %#v", authEval.DeniedKeys)
	}
	usersEval, err := db.RecordRuntimeUserDesired(ctx, server.ID, usersDigest, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !usersEval.Changed || usersEval.State.DesiredRevision != 2 {
		t.Fatalf("runtime users revision was not reissued for identical content: %#v", usersEval)
	}
	nextProbeVersion, err := db.LatencyProbePlanVersion(ctx, server.ID, probeDigest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if nextProbeVersion <= probeVersion {
		t.Fatalf("probe plan version was not reissued for an identical plan: before=%d after=%d", probeVersion, nextProbeVersion)
	}
}
