package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

const runtimeUsersRedeliveryAfter = 10 * time.Second

func serverSupportsRuntimeUsersLane(server model.Server) bool {
	hasAgent := false
	hasKernel := false
	for _, capability := range server.KernelCapabilities {
		if capability == model.AgentCapabilityRuntimeUsers {
			hasAgent = true
		}
		if capability == model.KernelCapabilityRuntimeUsers {
			hasKernel = true
		}
	}
	return hasAgent && hasKernel
}

func (s *Server) usersEnvelopeFor(server model.Server, pkg core.RuntimeUserPackage, messageType string) (model.UsersEnvelope, error) {
	usersJSON, err := json.Marshal(pkg.Request())
	if err != nil {
		return model.UsersEnvelope{}, err
	}
	messageID := newUsersMessageID()
	envelope := model.UsersEnvelope{Type: messageType, MessageID: messageID, ServerID: server.ID, UsersJSON: string(usersJSON)}
	envelope.Signature = security.SignUsersEnvelope(server.AgentTokenHash, security.UsersEnvelopeFields{
		ServerID: server.ID, MessageID: messageID, Revision: pkg.UsersRevision, Digest: pkg.UsersDigest, UsersJSON: envelope.UsersJSON,
	})
	return envelope, nil
}

func newUsersMessageID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "users-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func (s *Server) wakeRuntimeUsersSync() {
	if s.runtimeUsersSyncWake == nil {
		return
	}
	select {
	case s.runtimeUsersSyncWake <- struct{}{}:
	default:
	}
}

// wakeRuntimeUsersSyncFor is the directed form of the wake above: it names the
// servers that may owe a user set and why. See wakeAuthorizationSyncFor.
func (s *Server) wakeRuntimeUsersSyncFor(reason string, serverIDs ...int64) {
	s.runtimeUsersHints.note(reason, serverIDs)
	s.wakeRuntimeUsersSync()
}

func (s *Server) StartRuntimeUsersSyncWorker(ctx context.Context) {
	recoveryMin, recoveryMax := s.taskRecoveryScanMin, s.taskRecoveryScanMax
	if recoveryMin <= 0 {
		recoveryMin = defaultTaskRecoveryScanMin
	}
	if recoveryMax <= recoveryMin {
		recoveryMax = recoveryMin + defaultTaskRecoveryScanMin
	}
	next := func() time.Duration {
		span := int64(recoveryMax - recoveryMin)
		if span <= 0 {
			return recoveryMin
		}
		n, err := rand.Int(rand.Reader, big.NewInt(span))
		if err != nil {
			return recoveryMin
		}
		return recoveryMin + time.Duration(n.Int64())
	}
	s.reconcileRuntimeUsersSync(ctx, true)
	timer := time.NewTimer(next())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.runtimeUsersSyncWake:
			timer.Stop()
			s.reconcileRuntimeUsersSync(ctx, false)
			timer.Reset(next())
		case <-timer.C:
			s.reconcileRuntimeUsersSync(ctx, true)
			timer.Reset(next())
		}
	}
}

// reconcileRuntimeUsersSync mirrors reconcileAuthorizationSync: one
// implementation shared by the directed wake, the undirected wake and the
// periodic recovery scan, with a settled check that runs before any package is
// built.
func (s *Server) reconcileRuntimeUsersSync(ctx context.Context, full bool) {
	hints := s.runtimeUsersHints.drain()
	routingRevision, err := s.store.RoutingCacheRevision(ctx)
	if err != nil {
		log.Printf("runtime users sync: routing revision: %v", err)
		return
	}
	var candidates []store.AccessSyncCandidate
	if full || len(hints) > 0 {
		candidates, err = s.store.ListRuntimeUsersSyncCandidates(ctx)
	} else {
		var stale []int64
		stale, err = s.store.StaleRuntimeUserServerIDs(ctx, routingRevision)
		for _, serverID := range stale {
			candidates = append(candidates, store.AccessSyncCandidate{ServerID: serverID})
		}
	}
	if err != nil {
		log.Printf("runtime users sync: list servers: %v", err)
		return
	}
	generation := s.runtimeUserPackageGeneration()
	plan := planAccessSyncRound(candidates, routingRevision, hints, func(serverID int64) bool {
		return s.runtimeUsersTimeBoundarySettled(serverID, generation)
	})
	s.hotPath.runtimeUsersSyncSkipped.Add(int64(plan.skipped))
	s.hotPath.runtimeUsersSyncEvaluated.Add(int64(len(plan.work)))
	runAccessSyncPlan(ctx, plan, s.syncServerRuntimeUsers)
}

func (s *Server) buildRuntimeUserPackage(ctx context.Context, server model.Server, revision int64) (core.RuntimeUserPackage, error) {
	span := startHotPath("runtime_user_package_build")
	var err error
	defer func() { span.end(err) }()

	// Prefer the shared routing snapshot so concurrent package builds across
	// servers reuse one consistent FullRoutingConfig + access view instead of
	// each paying FullRoutingConfigData + credential decrypt again.
	snap, err := s.routingSnapshot(ctx)
	if err != nil {
		return core.RuntimeUserPackage{}, err
	}
	pkg, err := s.projectRuntimeUserPackage(ctx, server, snap, revision)
	if err != nil {
		return core.RuntimeUserPackage{}, err
	}
	return pkg, nil
}

// projectRuntimeUserPackage builds the installable user package without the
// deploy-side side effects of a full core-config generation: no certificate
// materialization, no DNS policy hard-fail, no port allocation persistence,
// and no authorization-lease attachment.
//
// Everything server-independent comes from the shared routing snapshot: its
// users already carry decrypted proxy credentials, and its projection inputs
// are derived once per revision. This path must not decrypt credentials or
// rebuild the access snapshot again, because it runs once per server on every
// fleet poll cycle.
func (s *Server) projectRuntimeUserPackage(ctx context.Context, server model.Server, snap *routingSnapshot, revision int64) (core.RuntimeUserPackage, error) {
	data := snap.data
	shared := snap.runtimeProjectionInputs()
	data.ProxyPaths = shared.proxyPaths
	bindings, pathBindings, userPolicies := shared.inboundBindings, shared.pathBindings, shared.userPolicies
	accountingUsers := core.TrafficAccountingUsersForServer(server.ID, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, bindings, pathBindings)
	trafficPolicies, err := s.trafficRuntimePolicies(ctx, server.ID, data.Users, accountingUsers, userPolicies)
	if err != nil {
		return core.RuntimeUserPackage{}, err
	}
	// Read-only ledger: generation may consult existing allocations but this
	// path must never persist new ports.
	ledger := core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)
	// DNS is optional for user projection. BuildDNSConfig already substitutes a
	// default state when nil, so a missing per-server policy must not block
	// delivering users for an otherwise valid topology.
	var dnsState *core.DNSConfigState
	if state, dnsErr := core.DNSConfigStateForServer(server.ID, data.DNSLists, data.ServerDNSPolicies); dnsErr == nil {
		dnsState = state
	}
	pkg, err := core.ProjectServerRuntimeUsers(server, data.Inbounds, data.Outbounds, dnsState, data.Users, core.ConfigOptions{
		RoutingRules: data.RoutingRules, RoutingRuleSets: data.RoutingRuleSets, ExternalOutbounds: data.ExternalOutbounds,
		ProxyPaths: data.ProxyPaths, ProxyPathSteps: data.ProxyPathSteps, Servers: data.Servers, Inbounds: data.Inbounds,
		WARPProfiles: data.WARPProfiles, InboundUsers: bindings, ProxyPathUsers: pathBindings,
		UserPolicies: userPolicies, TrafficPolicies: trafficPolicies, UserDevices: data.UserDevices,
		PortLedger: ledger,
	})
	if err != nil {
		return core.RuntimeUserPackage{}, err
	}
	pkg.UsersRevision = revision
	digest, err := core.UsersDigest(revision, pkg.Scope, pkg.Entries)
	if err != nil {
		return core.RuntimeUserPackage{}, err
	}
	pkg.UsersDigest = digest
	// Both delivery paths gate on the content digest; computing it here keeps a
	// cached package from re-hashing every entry on each Agent pull.
	if pkg.ContentDigest, err = core.UsersContentDigest(pkg.Scope, pkg.Entries); err != nil {
		return core.RuntimeUserPackage{}, err
	}
	pkg.Mode = "full"
	return pkg, nil
}

// runtimeUserContentDigest returns the package's cached users-lane gate,
// computing it only for a package that did not come from the build path.
func runtimeUserContentDigest(pkg core.RuntimeUserPackage) (string, error) {
	if pkg.ContentDigest != "" {
		return pkg.ContentDigest, nil
	}
	return core.UsersContentDigest(pkg.Scope, pkg.Entries)
}

func (s *Server) syncServerRuntimeUsers(ctx context.Context, serverID int64, forceRebuild bool) {
	s.runtimeUsersSyncMu.Lock()
	if s.runtimeUsersSyncInFlight[serverID] {
		s.runtimeUsersSyncMu.Unlock()
		return
	}
	s.runtimeUsersSyncInFlight[serverID] = true
	s.runtimeUsersSyncMu.Unlock()
	defer func() {
		s.runtimeUsersSyncMu.Lock()
		delete(s.runtimeUsersSyncInFlight, serverID)
		s.runtimeUsersSyncMu.Unlock()
	}()
	// The ledger decides first: a stable node must not pay for a server load or
	// a package build to conclude it is already confirmed.
	state, err := s.store.RuntimeUserState(ctx, serverID)
	if err != nil {
		return
	}
	routingRevision, err := s.store.RoutingCacheRevision(ctx)
	if err != nil {
		return
	}
	// Wake path revision gate: confirmed servers already evaluated for the
	// current routing revision skip full package generation. A forced pass -
	// a reconnect, a revoke, or a business boundary the ledger cannot express -
	// still rebuilds, so a missed deadline cannot stick.
	if !forceRebuild && state.EvaluatedRoutingRevision == routingRevision && state.Confirmed() {
		return
	}
	// An offline Agent already recorded at this revision gains nothing from
	// another package build and another signed envelope.
	if state.PendingReason == store.RuntimeUsersPendingAgentOffline && state.EvaluatedRoutingRevision == routingRevision && !s.agentControlOnline(serverID) {
		s.hotPath.runtimeUsersSyncOfflineSkipped.Add(1)
		return
	}
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return
	}
	if server.AgentID == "" {
		_ = s.store.MarkRuntimeUsersPending(ctx, serverID, store.RuntimeUsersPendingUnenrolled, "", false)
		return
	}
	probeRevision := state.DesiredRevision
	if probeRevision <= 0 {
		probeRevision = 1
	}
	pkg, builtRevision, err := s.currentRuntimeUserPackage(ctx, *server, probeRevision)
	if err != nil {
		log.Printf("runtime users sync server=%d: build package: %v", serverID, err)
		_ = s.store.MarkRuntimeUsersPending(ctx, serverID, store.RuntimeUsersPendingDeliveryFailed, err.Error(), true)
		return
	}
	routingRevision = builtRevision
	contentDigest, err := runtimeUserContentDigest(pkg)
	if err != nil {
		_ = s.store.MarkRuntimeUsersPending(ctx, serverID, store.RuntimeUsersPendingDeliveryFailed, err.Error(), true)
		return
	}
	evaluation, err := s.store.EvaluateRuntimeUsersDesired(ctx, serverID, routingRevision, contentDigest, time.Now().UTC())
	if err != nil {
		return
	}
	// Record which package generation this evaluation belongs to. Any business
	// deadline firing bumps that generation, so the next recovery scan cannot
	// mistake "the routing revision did not change" for "no time boundary
	// passed".
	s.markRuntimeUsersEvaluated(serverID, s.runtimeUserPackageGeneration())
	state = evaluation.State
	if pkg.UsersRevision != state.DesiredRevision {
		pkg.UsersRevision = state.DesiredRevision
		pkg.UsersDigest, err = core.UsersDigest(pkg.UsersRevision, pkg.Scope, pkg.Entries)
		if err != nil {
			return
		}
	}
	pkg.Mode = "full"
	if !s.runtimeUsersLaneEnabled(ctx, *server) {
		reason := store.RuntimeUsersPendingAgentUpgrade
		if serverSupportsRuntimeUsersLane(*server) {
			reason = store.RuntimeUsersPendingCoreConfigFallback
		}
		if state.PendingReason == reason && state.DeliveredRevision >= pkg.UsersRevision {
			return
		}
		_ = s.store.MarkRuntimeUsersPending(ctx, serverID, reason, "", true)
		return
	}
	if len(pkg.Scope) == 0 {
		_, _ = s.store.RecordRuntimeUsersConfirmation(ctx, serverID, pkg.UsersRevision, pkg.UsersDigest, "")
		return
	}
	if state.Confirmed() {
		return
	}
	if state.DeliveredRevision == pkg.UsersRevision && state.DeliveredAt != nil && time.Since(*state.DeliveredAt) < runtimeUsersRedeliveryAfter && state.PendingReason != store.RuntimeUsersPendingAgentUpgrade {
		return
	}
	if !s.agentControlOnline(serverID) {
		_ = s.store.MarkRuntimeUsersPending(ctx, serverID, store.RuntimeUsersPendingAgentOffline, "", true)
		return
	}
	parts := chunkRuntimeUserPackages(pkg)
	var lastMessageID string
	for _, part := range parts {
		envelope, err := s.usersEnvelopeFor(*server, part, model.AgentControlUsersUpdate)
		if err != nil {
			_ = s.store.MarkRuntimeUsersPending(ctx, serverID, store.RuntimeUsersPendingDeliveryFailed, err.Error(), true)
			return
		}
		if !s.sendAgentControl(serverID, envelope) {
			_ = s.store.MarkRuntimeUsersPending(ctx, serverID, store.RuntimeUsersPendingAgentOffline, "", true)
			return
		}
		lastMessageID = envelope.MessageID
	}
	if err := s.store.RecordRuntimeUsersDelivery(ctx, serverID, pkg.UsersRevision, lastMessageID); err != nil {
		log.Printf("runtime users sync server=%d: record delivery: %v", serverID, err)
	}
	log.Printf("runtime users delivered server=%d(%s) revision=%d entries=%d chunks=%d message=%s", serverID, safeLogField(server.Name), pkg.UsersRevision, len(pkg.Entries), len(parts), lastMessageID)
}

const (
	runtimeUsersChunkEntryLimit = 128
	runtimeUsersChunkBytesLimit = 256 << 10
)

func chunkRuntimeUserPackages(pkg core.RuntimeUserPackage) []core.RuntimeUserPackage {
	// The encoded size does not depend on the chunk size, so it is measured once
	// and reused. Re-encoding the whole package inside the halving loop below
	// produced the same bytes up to seven times per delivery.
	encodedSize := -1
	if raw, err := json.Marshal(pkg.Request()); err == nil {
		encodedSize = len(raw)
	}
	if len(pkg.Entries) <= runtimeUsersChunkEntryLimit && encodedSize >= 0 && encodedSize <= runtimeUsersChunkBytesLimit {
		return []core.RuntimeUserPackage{pkg}
	}
	if len(pkg.Entries) == 0 {
		return []core.RuntimeUserPackage{pkg}
	}
	size := runtimeUsersChunkEntryLimit
	if size < 1 {
		size = 1
	}
	for size > 1 {
		if encodedSize < 0 || encodedSize/((len(pkg.Entries)+size-1)/size) <= runtimeUsersChunkBytesLimit {
			break
		}
		size /= 2
	}
	total := (len(pkg.Entries) + size - 1) / size
	if total <= 1 {
		return []core.RuntimeUserPackage{pkg}
	}
	out := make([]core.RuntimeUserPackage, 0, total)
	for index := 0; index < total; index++ {
		start := index * size
		end := start + size
		if end > len(pkg.Entries) {
			end = len(pkg.Entries)
		}
		part := pkg
		part.Entries = append([]model.UsersInstallEntry(nil), pkg.Entries[start:end]...)
		payload, _ := json.Marshal(part.Entries)
		sum := sha256SumHex(payload)
		part.Chunk = &model.UsersInstallChunk{Index: index, Total: total, SHA256: sum}
		out = append(out, part)
	}
	return out
}

func sha256SumHex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (s *Server) handleUsersAck(ctx context.Context, server *model.Server, raw map[string]json.RawMessage) {
	var ack model.UsersAck
	if payload, ok := raw["users_ack"]; ok {
		if err := json.Unmarshal(payload, &ack); err != nil {
			return
		}
	} else {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return
		}
		if err := json.Unmarshal(encoded, &ack); err != nil {
			return
		}
	}
	s.recordUsersAck(ctx, server, ack)
}

func (s *Server) recordUsersAck(ctx context.Context, server *model.Server, ack model.UsersAck) {
	if ack.Revision <= 0 {
		return
	}
	if !ack.Confirmed {
		if ack.Runtimes["kernel"] == "incomplete" || strings.Contains(ack.Error, "incomplete") {
			return
		}
		reason := store.RuntimeUsersPendingRuntimeUnavailable
		if strings.Contains(ack.Error, "does not advertise") || strings.Contains(ack.Error, "does not support") {
			reason = store.RuntimeUsersPendingCoreConfigFallback
		}
		if strings.TrimSpace(ack.Error) == "" {
			ack.Error = "agent did not confirm users"
		}
		_ = s.store.MarkRuntimeUsersPending(ctx, server.ID, reason, ack.Error, true)
		if reason == store.RuntimeUsersPendingCoreConfigFallback {
			_ = s.queueCoreConfigRefreshForServers(ctx, []int64{server.ID}, "runtime_users_fallback")
		}
		log.Printf("runtime users not confirmed server=%d(%s) revision=%d error=%s", server.ID, safeLogField(server.Name), ack.Revision, safeLogField(ack.Error))
		return
	}
	advanced, err := s.store.RecordRuntimeUsersConfirmation(ctx, server.ID, ack.Revision, ack.Digest, ack.BootID)
	if err != nil {
		log.Printf("record runtime users confirmation server=%d: %v", server.ID, err)
		return
	}
	state, err := s.store.RuntimeUserState(ctx, server.ID)
	if err != nil {
		return
	}
	if !state.Confirmed() {
		s.wakeRuntimeUsersSync()
	}
	if advanced {
		s.publishRealtime("authorization")
	}
}

func (s *Server) recordUsersApplied(ctx context.Context, server *model.Server, applied *model.UsersAppliedSnapshot) {
	if applied == nil || applied.Revision <= 0 {
		return
	}
	s.recordUsersAck(ctx, server, model.UsersAck{Revision: applied.Revision, Digest: applied.Digest, BootID: applied.BootID, Confirmed: true})
}

func (s *Server) agentUsersSnapshot(w http.ResponseWriter, r *http.Request) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowAgentRate(w, "agent-users:"+server.AgentID, 30, time.Minute) {
		return
	}
	if applied := parseAppliedUsersHeaders(r); applied != nil {
		s.recordUsersApplied(r.Context(), server, applied)
	}
	state, err := s.store.RuntimeUserState(r.Context(), server.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	revision := state.DesiredRevision
	if revision <= 0 {
		revision = 1
	}
	pkg, routingRevision, err := s.currentRuntimeUserPackage(r.Context(), *server, revision)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	contentDigest, err := runtimeUserContentDigest(pkg)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	evaluation, err := s.store.EvaluateRuntimeUsersDesired(r.Context(), server.ID, routingRevision, contentDigest, time.Now().UTC())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if pkg.UsersRevision != evaluation.State.DesiredRevision {
		pkg.UsersRevision = evaluation.State.DesiredRevision
		pkg.UsersDigest, err = core.UsersDigest(pkg.UsersRevision, pkg.Scope, pkg.Entries)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
	}
	pkg.Mode = "full"
	// An empty scope means this server has no runtime-managed inbound, and the
	// kernel rejects an install without one. Answer with an empty envelope the
	// Agent skips instead of signing a package it can only fail to apply.
	if len(pkg.Scope) == 0 {
		_, _ = s.store.RecordRuntimeUsersConfirmation(r.Context(), server.ID, pkg.UsersRevision, pkg.UsersDigest, "")
		w.Header().Set("Cache-Control", "no-store")
		write(w, http.StatusOK, model.UsersEnvelope{ServerID: server.ID})
		return
	}
	envelope, err := s.usersEnvelopeFor(*server, pkg, "")
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if err := s.store.RecordRuntimeUsersDelivery(r.Context(), server.ID, pkg.UsersRevision, envelope.MessageID); err != nil {
		log.Printf("runtime users pull server=%d: record delivery: %v", server.ID, err)
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, envelope)
}

const (
	headerUsersAppliedRevision = "X-Oboard-Users-Revision"
	headerUsersAppliedDigest   = "X-Oboard-Users-Digest"
	headerUsersBootID          = "X-Oboard-Users-Boot-Id"
)

// serverDeliveryLaneStates is one request-scoped load of authorization, runtime
// user, and delivery-flag ledgers. page-data used to list those tables twice:
// once to annotate servers and again to decorate configuration_sync.
type serverDeliveryLaneStates struct {
	auth  map[int64]store.AuthorizationState
	users map[int64]store.RuntimeUserState
	flags map[int64]store.ServerDeliveryFlags
	ok    bool
}

func (s *Server) loadServerDeliveryLaneStates(ctx context.Context) serverDeliveryLaneStates {
	auths, err := s.store.ListAuthorizationStates(ctx)
	if err != nil {
		return serverDeliveryLaneStates{}
	}
	users, err := s.store.ListRuntimeUserStates(ctx)
	if err != nil {
		return serverDeliveryLaneStates{}
	}
	flags, err := s.store.ListServerDeliveryFlags(ctx)
	if err != nil {
		flags = map[int64]store.ServerDeliveryFlags{}
	}
	authByID := make(map[int64]store.AuthorizationState, len(auths))
	for _, auth := range auths {
		authByID[auth.ServerID] = auth
	}
	usersByID := make(map[int64]store.RuntimeUserState, len(users))
	for _, state := range users {
		usersByID[state.ServerID] = state
	}
	return serverDeliveryLaneStates{auth: authByID, users: usersByID, flags: flags, ok: true}
}

func (s *Server) annotateServerDeliveryStatus(ctx context.Context, items []model.Server) {
	s.annotateServerDeliveryStatusFromLaneStates(ctx, items, s.loadServerDeliveryLaneStates(ctx))
}

func (s *Server) annotateServerDeliveryStatusFromLaneStates(ctx context.Context, items []model.Server, states serverDeliveryLaneStates) {
	if len(items) == 0 {
		return
	}
	if !states.ok {
		for i := range items {
			s.annotateOneServerDeliveryStatus(ctx, &items[i])
		}
		return
	}
	for i := range items {
		auth := states.auth[items[i].ID]
		if auth.ServerID == 0 {
			auth = store.AuthorizationState{ServerID: items[i].ID, Retryable: true}
		}
		userState := states.users[items[i].ID]
		if userState.ServerID == 0 {
			userState = store.RuntimeUserState{ServerID: items[i].ID, Retryable: true}
		}
		deliveryFlags, ok := states.flags[items[i].ID]
		if !ok {
			deliveryFlags = store.ServerDeliveryFlags{ServerID: items[i].ID, AuthorizationFastLane: true, RuntimeUsersEnabled: true}
		}
		applyServerDeliveryAnnotation(&items[i], auth, userState, deliveryFlags)
	}
	hasSSH := false
	for _, item := range items {
		hasSSH = hasSSH || states.flags[item.ID].HasSSHInbounds
	}
	if hasSSH {
		s.annotateSSHUserDeliveryStatuses(ctx, items)
	}
}

func (s *Server) annotateOneServerDeliveryStatus(ctx context.Context, server *model.Server) {
	if server == nil {
		return
	}
	auth, err := s.store.AuthorizationState(ctx, server.ID)
	if err != nil {
		auth = store.AuthorizationState{ServerID: server.ID, Retryable: true}
	}
	users, err := s.store.RuntimeUserState(ctx, server.ID)
	if err != nil {
		users = store.RuntimeUserState{ServerID: server.ID, Retryable: true}
	}
	flags, err := s.store.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		flags = store.ServerDeliveryFlags{ServerID: server.ID, AuthorizationFastLane: true, RuntimeUsersEnabled: true}
	}
	applyServerDeliveryAnnotation(server, auth, users, flags)
	if flags.HasSSHInbounds {
		s.annotateSSHUserDelivery(ctx, server)
	}
}

func applyServerDeliveryAnnotation(server *model.Server, auth store.AuthorizationState, users store.RuntimeUserState, flags store.ServerDeliveryFlags) {
	if server == nil {
		return
	}
	server.AuthorizationRevision = auth.DesiredRevision
	server.AuthorizationConfirmed = auth.Confirmed()
	server.AuthorizationPendingReason = auth.PendingReason
	server.UsersRevision = users.DesiredRevision
	server.UsersConfirmed = users.Confirmed()
	server.UsersPendingReason = users.PendingReason
	if users.PendingReason == store.RuntimeUsersPendingAgentUpgrade || users.PendingReason == store.RuntimeUsersPendingCoreConfigFallback {
		server.UsersFallback = "apply_core_config"
	} else {
		server.UsersFallback = ""
	}
	applyDeliveryFlagsToServer(server, flags)
}

// Subscription reads must not expose credentials saved after the last evaluated install.
func (s *Server) annotateSnellSubscriptionDelivery(ctx context.Context, servers []model.Server, inbounds []model.Inbound) {
	shared := make(map[int64]bool)
	for _, inbound := range inbounds {
		if core.SnellSharedPort(inbound) {
			shared[inbound.ServerID] = true
		}
	}
	if len(shared) == 0 {
		return
	}
	states := s.loadServerDeliveryLaneStates(ctx)
	revision, err := s.store.RoutingCacheRevision(ctx)
	for i := range servers {
		server := &servers[i]
		if !shared[server.ID] {
			continue
		}
		server.UsersConfirmed, server.AuthorizationConfirmed = false, false
		if err != nil || !states.ok {
			continue
		}
		users, auth := states.users[server.ID], states.auth[server.ID]
		server.UsersConfirmed = users.Confirmed() && users.EvaluatedRoutingRevision == revision && users.DesiredDigest != "" && users.DesiredDigest == users.ConfirmedDigest
		server.AuthorizationConfirmed = auth.Confirmed() && auth.EvaluatedRoutingRevision == revision && auth.DesiredDigest != "" && auth.DesiredDigest == auth.ConfirmedDigest
	}
}

func parseAppliedUsersHeaders(r *http.Request) *model.UsersAppliedSnapshot {
	revision, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get(headerUsersAppliedRevision)), 10, 64)
	if err != nil || revision <= 0 {
		return nil
	}
	digest := strings.TrimSpace(r.Header.Get(headerUsersAppliedDigest))
	bootID := strings.TrimSpace(r.Header.Get(headerUsersBootID))
	if len(digest) > 128 || len(bootID) > 128 {
		return nil
	}
	return &model.UsersAppliedSnapshot{Revision: revision, Digest: digest, BootID: bootID}
}

func (s *Server) filterSubscriptionNodesByDelivery(ctx context.Context, user model.User, data store.FullRoutingConfig, snapshot *core.EffectiveAccessSnapshot, effectiveNodes map[string]bool) map[string]bool {
	if len(effectiveNodes) == 0 || snapshot == nil {
		return effectiveNodes
	}
	grants := snapshot.UserNodes[user.ID]
	var bindingUpdated time.Time
	for _, binding := range data.PlanBindings {
		if binding.UserID != user.ID {
			continue
		}
		if binding.UpdatedAt.After(bindingUpdated) {
			bindingUpdated = binding.UpdatedAt
		}
		if binding.CreatedAt.After(bindingUpdated) {
			bindingUpdated = binding.CreatedAt
		}
	}
	out := make(map[string]bool, len(effectiveNodes))
	for key := range effectiveNodes {
		grant := grants[key]
		serverID, ok := subscriptionNodeServerID(grant, data)
		if !ok {
			out[key] = true
			continue
		}
		auth, _ := s.store.AuthorizationState(ctx, serverID)
		users, _ := s.store.RuntimeUserState(ctx, serverID)
		grantTime := bindingUpdated
		if grant.Exception != nil {
			grantTime = grant.Exception.CreatedAt
		}
		if subscriptionDeliveryAllowsNode(auth, users, grantTime) {
			out[key] = true
		}
	}
	return out
}

func subscriptionNodeServerID(grant core.EffectiveNodeGrant, data store.FullRoutingConfig) (int64, bool) {
	switch grant.NodeType {
	case model.AssignableNodeInbound:
		for _, inbound := range data.Inbounds {
			if inbound.ID == grant.NodeID {
				return inbound.ServerID, true
			}
		}
	case model.AssignableNodeProxyPath:
		for _, path := range data.ProxyPaths {
			if path.ID != grant.NodeID {
				continue
			}
			for _, inbound := range data.Inbounds {
				if inbound.ID == path.InboundID {
					return inbound.ServerID, true
				}
			}
		}
	}
	return 0, false
}

func subscriptionDeliveryAllowsNode(auth store.AuthorizationState, users store.RuntimeUserState, grantTime time.Time) bool {
	authPending := auth.DesiredRevision > 0 && !auth.Confirmed()
	usersPending := users.DesiredRevision > 0 && !users.Confirmed() &&
		users.PendingReason != store.RuntimeUsersPendingAgentUpgrade &&
		users.PendingReason != store.RuntimeUsersPendingCoreConfigFallback
	if !authPending && !usersPending {
		return true
	}
	var confirmedAt *time.Time
	if auth.ConfirmedAt != nil {
		confirmedAt = auth.ConfirmedAt
	}
	if users.ConfirmedAt != nil && (confirmedAt == nil || users.ConfirmedAt.After(*confirmedAt)) {
		confirmedAt = users.ConfirmedAt
	}
	if confirmedAt == nil {
		return false
	}
	return !grantTime.After(*confirmedAt)
}
