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

func (s *Server) reconcileRuntimeUsersSync(ctx context.Context, full bool) {
	var serverIDs []int64
	var err error
	if full {
		serverIDs, err = s.store.EnrolledServerIDs(ctx)
	} else {
		var revision uint64
		revision, err = s.store.RoutingCacheRevision(ctx)
		if err == nil {
			serverIDs, err = s.store.StaleRuntimeUserServerIDs(ctx, revision)
		}
	}
	if err != nil {
		log.Printf("runtime users sync: list servers: %v", err)
		return
	}
	for _, serverID := range serverIDs {
		if ctx.Err() != nil {
			return
		}
		s.syncServerRuntimeUsers(ctx, serverID)
	}
}

func (s *Server) currentRuntimeUserPackage(ctx context.Context, server model.Server, revision int64) (core.RuntimeUserPackage, error) {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return core.RuntimeUserPackage{}, err
	}
	data, err = s.loadProxyCredentialData(ctx, data)
	if err != nil {
		return core.RuntimeUserPackage{}, err
	}
	ledger := core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)
	generated, err := s.generateServerCoreConfigWithLedger(ctx, server, data, ledger)
	if err != nil {
		return core.RuntimeUserPackage{}, err
	}
	if generated.RuntimeUsers != nil {
		pkg := *generated.RuntimeUsers
		pkg.UsersRevision = revision
		digest, err := core.UsersDigest(revision, pkg.Scope, pkg.Entries)
		if err != nil {
			return core.RuntimeUserPackage{}, err
		}
		pkg.UsersDigest = digest
		pkg.Mode = "full"
		return pkg, nil
	}
	var config core.SingBoxConfig
	if err := json.Unmarshal([]byte(generated.Config), &config); err != nil {
		return core.RuntimeUserPackage{}, err
	}
	return core.RuntimeUserPackageFromConfig(config, revision)
}

func (s *Server) syncServerRuntimeUsers(ctx context.Context, serverID int64) {
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
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return
	}
	if server.AgentID == "" {
		_ = s.store.MarkRuntimeUsersPending(ctx, serverID, store.RuntimeUsersPendingUnenrolled, "", false)
		return
	}
	state, err := s.store.RuntimeUserState(ctx, serverID)
	if err != nil {
		return
	}
	probeRevision := state.DesiredRevision
	if probeRevision <= 0 {
		probeRevision = 1
	}
	pkg, err := s.currentRuntimeUserPackage(ctx, *server, probeRevision)
	if err != nil {
		log.Printf("runtime users sync server=%d: build package: %v", serverID, err)
		_ = s.store.MarkRuntimeUsersPending(ctx, serverID, store.RuntimeUsersPendingDeliveryFailed, err.Error(), true)
		return
	}
	contentDigest, err := core.UsersDigest(0, pkg.Scope, pkg.Entries)
	if err != nil {
		_ = s.store.MarkRuntimeUsersPending(ctx, serverID, store.RuntimeUsersPendingDeliveryFailed, err.Error(), true)
		return
	}
	routingRevision, err := s.store.RoutingCacheRevision(ctx)
	if err != nil {
		return
	}
	evaluation, err := s.store.EvaluateRuntimeUsersDesired(ctx, serverID, routingRevision, contentDigest, time.Now().UTC())
	if err != nil {
		return
	}
	state = evaluation.State
	pkg.UsersRevision = state.DesiredRevision
	pkg.UsersDigest, err = core.UsersDigest(pkg.UsersRevision, pkg.Scope, pkg.Entries)
	if err != nil {
		return
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
	if len(pkg.Entries) <= runtimeUsersChunkEntryLimit {
		if raw, err := json.Marshal(pkg.Request()); err == nil && len(raw) <= runtimeUsersChunkBytesLimit {
			return []core.RuntimeUserPackage{pkg}
		}
	}
	if len(pkg.Entries) == 0 {
		return []core.RuntimeUserPackage{pkg}
	}
	size := runtimeUsersChunkEntryLimit
	if size < 1 {
		size = 1
	}
	for size > 1 {
		if raw, err := json.Marshal(pkg.Request()); err == nil && len(raw)/((len(pkg.Entries)+size-1)/size) <= runtimeUsersChunkBytesLimit {
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
	pkg, err := s.currentRuntimeUserPackage(r.Context(), *server, revision)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	contentDigest, err := core.UsersDigest(0, pkg.Scope, pkg.Entries)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	routingRevision, err := s.store.RoutingCacheRevision(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	evaluation, err := s.store.EvaluateRuntimeUsersDesired(r.Context(), server.ID, routingRevision, contentDigest, time.Now().UTC())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	pkg.UsersRevision = evaluation.State.DesiredRevision
	pkg.UsersDigest, err = core.UsersDigest(pkg.UsersRevision, pkg.Scope, pkg.Entries)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	pkg.Mode = "full"
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
