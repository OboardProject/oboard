package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func serverSupportsAuthorizationLease(server model.Server) bool {
	for _, capability := range server.KernelCapabilities {
		if capability == model.AgentCapabilityAuthorizationLease {
			return true
		}
	}
	return false
}

func serverSupportsAuthorizationControl(server model.Server) bool {
	for _, capability := range server.KernelCapabilities {
		if capability == model.AgentCapabilityAuthorizationControl {
			return true
		}
	}
	return false
}

// authorizationEnvelopeFor signs the lease for one server. Every transport
// (control message, HTTP pull, traffic response) carries the same envelope so
// the Agent verifies and applies through one path.
func (s *Server) authorizationEnvelopeFor(server model.Server, lease *model.AuthorizationLease, messageType string) (model.AuthorizationEnvelope, error) {
	leaseJSON, err := json.Marshal(lease)
	if err != nil {
		return model.AuthorizationEnvelope{}, err
	}
	messageID := newAuthorizationMessageID()
	envelope := model.AuthorizationEnvelope{Type: messageType, MessageID: messageID, ServerID: server.ID, LeaseJSON: string(leaseJSON)}
	envelope.Signature = security.SignAuthorizationEnvelope(server.AgentTokenHash, security.AuthorizationEnvelopeFields{
		ServerID: server.ID, MessageID: messageID, Revision: lease.Revision, Sequence: lease.Sequence, IssuedAt: lease.IssuedAt, ExpiresAt: lease.ExpiresAt, LeaseJSON: envelope.LeaseJSON,
	})
	return envelope, nil
}

// authorizationRedeliveryAfter is how long an unconfirmed delivery of the
// current revision suppresses a duplicate push. It is well under the recovery
// scan so a lost acknowledgement costs at most one scan period.
const authorizationRedeliveryAfter = 10 * time.Second

func newAuthorizationMessageID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "authz-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// wakeAuthorizationSync is a coalesced hint that some server's desired
// authorization may have changed. The worker keeps a database-backed recovery
// scan, so a lost hint only delays delivery.
func (s *Server) wakeAuthorizationSync() {
	if s.authorizationSyncWake == nil {
		return
	}
	select {
	case s.authorizationSyncWake <- struct{}{}:
	default:
	}
}

// StartAuthorizationSyncWorker delivers signed authorization snapshots to
// Agents outside the task queue. A wake evaluates only servers whose ledger is
// stale for the current routing revision or still unconfirmed; the periodic
// pass evaluates every enrolled server so time-driven boundaries (binding
// expiry, scheduled start) that change no database row are still delivered,
// including after a restart that missed them.
func (s *Server) StartAuthorizationSyncWorker(ctx context.Context) {
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
	s.reconcileAuthorizationSync(ctx, true)
	timer := time.NewTimer(next())
	defer timer.Stop()
	for {
		boundary := s.authorizationBoundaryTimer(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.authorizationSyncWake:
			timer.Stop()
			s.reconcileAuthorizationSync(ctx, false)
			timer.Reset(next())
		case <-timer.C:
			s.reconcileAuthorizationSync(ctx, true)
			timer.Reset(next())
		case <-boundary:
			timer.Stop()
			s.reconcileAuthorizationSync(ctx, true)
			timer.Reset(next())
		}
	}
}

// authorizationBoundaryTimer returns a channel that fires at the next
// time-driven grant transition, or nil when nothing is scheduled within the
// recovery window (the periodic pass covers later ones).
func (s *Server) authorizationBoundaryTimer(ctx context.Context) <-chan time.Time {
	projection, err := s.authorizationProjection(ctx)
	if err != nil {
		return nil
	}
	now := time.Now().UTC()
	next := projection.nextTransition(now)
	if next.IsZero() {
		return nil
	}
	wait := next.Sub(now) + 50*time.Millisecond
	if wait > time.Hour {
		return nil
	}
	return time.After(wait)
}

// reconcileAuthorizationSync evaluates and delivers authorization for servers.
func (s *Server) reconcileAuthorizationSync(ctx context.Context, full bool) {
	var serverIDs []int64
	var err error
	if full {
		serverIDs, err = s.store.EnrolledServerIDs(ctx)
	} else {
		var revision uint64
		revision, err = s.store.RoutingCacheRevision(ctx)
		if err == nil {
			serverIDs, err = s.store.StaleAuthorizationServerIDs(ctx, revision)
		}
	}
	if err != nil {
		log.Printf("authorization sync: list servers: %v", err)
		return
	}
	for _, serverID := range serverIDs {
		if ctx.Err() != nil {
			return
		}
		s.syncServerAuthorization(ctx, serverID, full)
	}
	if _, err := s.store.PruneAuthorizationDenials(ctx, time.Now().UTC()); err != nil {
		log.Printf("authorization sync: prune denials: %v", err)
	}
}

// syncServerAuthorization computes the current lease for one server, records
// the ledger, and pushes the signed envelope when the desired revision is not
// yet confirmed. It never blocks on the Agent task slot.
func (s *Server) syncServerAuthorization(ctx context.Context, serverID int64, forceRebuild bool) {
	s.authorizationSyncMu.Lock()
	if s.authorizationSyncInFlight[serverID] {
		s.authorizationSyncMu.Unlock()
		return
	}
	s.authorizationSyncInFlight[serverID] = true
	s.authorizationSyncMu.Unlock()
	defer func() {
		s.authorizationSyncMu.Lock()
		delete(s.authorizationSyncInFlight, serverID)
		s.authorizationSyncMu.Unlock()
	}()
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return
	}
	if server.AgentID == "" {
		_ = s.store.MarkAuthorizationPending(ctx, serverID, store.AuthorizationPendingUnenrolled, "", false)
		return
	}
	state, err := s.store.AuthorizationState(ctx, serverID)
	if err != nil {
		return
	}
	routingRevision, err := s.store.RoutingCacheRevision(ctx)
	if err != nil {
		return
	}
	if !forceRebuild && state.EvaluatedRoutingRevision == routingRevision && state.Confirmed() {
		return
	}
	lease, err := s.currentAuthorizationLease(ctx, serverID)
	if err != nil {
		log.Printf("authorization sync server=%d: issue lease: %v", serverID, err)
		_ = s.store.MarkAuthorizationPending(ctx, serverID, store.AuthorizationPendingDeliveryFailed, err.Error(), true)
		return
	}
	state, err = s.store.AuthorizationState(ctx, serverID)
	if err != nil {
		return
	}
	if state.Confirmed() {
		return
	}
	if state.DeliveredRevision == lease.Revision && state.DeliveredAt != nil && time.Since(*state.DeliveredAt) < authorizationRedeliveryAfter && state.PendingReason != store.AuthorizationPendingAgentUpgrade {
		// The current revision is already in flight on some transport; give the
		// Agent time to acknowledge before re-sending the same snapshot.
		return
	}
	if !s.authorizationFastLaneEnabled(ctx, *server) {
		// Older Agents, or a server whose fast lane is turned off, learn
		// about revokes through the task queue and traffic responses.
		reason := store.AuthorizationPendingAgentUpgrade
		if serverSupportsAuthorizationControl(*server) {
			reason = store.AuthorizationPendingCompatibilityTask
		}
		if state.DeliveredRevision >= lease.Revision && state.PendingReason == reason {
			return
		}
		if serverSupportsAuthorizationLease(*server) && server.Status == model.ServerOnline {
			if err := s.queueAuthorizationRefreshTask(ctx, *server, lease, "authorization_revision_"+strconv.FormatInt(lease.Revision, 10)); err != nil {
				log.Printf("authorization sync server=%d: queue compatibility task: %v", serverID, err)
			} else {
				_ = s.store.RecordAuthorizationDelivery(ctx, serverID, lease.Revision, lease.Sequence, "task")
			}
		}
		_ = s.store.MarkAuthorizationPending(ctx, serverID, reason, "", true)
		return
	}
	if !s.agentControlOnline(serverID) {
		_ = s.store.MarkAuthorizationPending(ctx, serverID, store.AuthorizationPendingAgentOffline, "", true)
		return
	}
	envelope, err := s.authorizationEnvelopeFor(*server, lease, model.AgentControlAuthorizationUpdate)
	if err != nil {
		_ = s.store.MarkAuthorizationPending(ctx, serverID, store.AuthorizationPendingDeliveryFailed, err.Error(), true)
		return
	}
	if !s.sendAgentControl(serverID, envelope) {
		_ = s.store.MarkAuthorizationPending(ctx, serverID, store.AuthorizationPendingAgentOffline, "", true)
		return
	}
	if err := s.store.RecordAuthorizationDelivery(ctx, serverID, lease.Revision, lease.Sequence, envelope.MessageID); err != nil {
		log.Printf("authorization sync server=%d: record delivery: %v", serverID, err)
	}
	log.Printf("authorization delivered server=%d(%s) revision=%d sequence=%d grants=%d denied=%d message=%s", serverID, safeLogField(server.Name), lease.Revision, lease.Sequence, len(lease.Grants), len(lease.Denied), envelope.MessageID)
}

// queueAuthorizationRefreshTask is the compatibility path for Agents without
// authorization_control_v1: the lease travels inside an apply_traffic_policy
// task and therefore waits for the single task slot.
func (s *Server) queueAuthorizationRefreshTask(ctx context.Context, server model.Server, lease *model.AuthorizationLease, reason string) error {
	pending, err := s.store.ListPendingTasksByServerAndType(ctx, server.ID, model.AgentTaskTypeApplyTrafficPolicy)
	if err != nil {
		return err
	}
	for _, task := range pending {
		var previous model.ApplyTrafficPolicyTaskPayload
		if json.Unmarshal([]byte(task.PayloadJSON), &previous) == nil && previous.Authorization != nil && previous.PolicyRevision == 0 && len(previous.Policies) == 0 {
			if previous.Authorization.Revision >= lease.Revision {
				return nil
			}
			if err := s.store.SupersedePendingTask(ctx, task.ID, "授权状态已由新任务取代"); err != nil {
				return err
			}
		}
	}
	payload := model.ApplyTrafficPolicyTaskPayload{Authorization: lease, Reason: reason, Policies: map[string]model.TrafficRuntimePolicy{}}
	_, err = s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeApplyTrafficPolicy, payload, lease.Revision)
	return err
}

// queueAuthorizationRefresh is retained for call sites that used to queue an
// apply_traffic_policy task after a credential change. It now only wakes the
// authorization worker, which decides the transport per server.
func (s *Server) queueAuthorizationRefresh(ctx context.Context, serverIDs []int64, reason string) error {
	_ = serverIDs
	_ = reason
	s.wakeAuthorizationSync()
	return nil
}

// handleAuthorizationAck records an Agent acknowledgement received on the
// control channel. An acknowledgement that names a revision below the desired
// one leaves the ledger unconfirmed and re-wakes the worker so the current
// snapshot is re-sent.
func (s *Server) handleAuthorizationAck(ctx context.Context, server *model.Server, raw map[string]json.RawMessage) {
	var ack model.AuthorizationAck
	if payload, ok := raw["authorization_ack"]; ok {
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
	s.recordAuthorizationAck(ctx, server, ack)
}

func (s *Server) recordAuthorizationAck(ctx context.Context, server *model.Server, ack model.AuthorizationAck) {
	if ack.Revision <= 0 {
		return
	}
	if !ack.Confirmed {
		reason := store.AuthorizationPendingRuntimeUnavailable
		if strings.TrimSpace(ack.Error) == "" {
			ack.Error = "agent did not confirm authorization"
		}
		_ = s.store.MarkAuthorizationPending(ctx, server.ID, reason, ack.Error, true)
		log.Printf("authorization not confirmed server=%d(%s) revision=%d sequence=%d error=%s", server.ID, safeLogField(server.Name), ack.Revision, ack.Sequence, safeLogField(ack.Error))
		return
	}
	advanced, err := s.store.RecordAuthorizationConfirmation(ctx, server.ID, ack.Revision, ack.Sequence, ack.Digest, ack.BootID)
	if err != nil {
		log.Printf("record authorization confirmation server=%d: %v", server.ID, err)
		return
	}
	state, err := s.store.AuthorizationState(ctx, server.ID)
	if err != nil {
		return
	}
	if !state.Confirmed() {
		s.wakeAuthorizationSync()
	}
	if advanced {
		s.publishRealtime("authorization")
	}
}

// recordAuthorizationTaskResult confirms the revision carried by a
// compatibility apply_traffic_policy task when the Agent reports success.
func (s *Server) recordAuthorizationTaskResult(ctx context.Context, server *model.Server, task model.AgentTask, status string) {
	var payload model.ApplyTrafficPolicyTaskPayload
	if json.Unmarshal([]byte(task.PayloadJSON), &payload) != nil || payload.Authorization == nil {
		return
	}
	if status != "succeeded" {
		_ = s.store.MarkAuthorizationPending(ctx, server.ID, store.AuthorizationPendingDeliveryFailed, "apply_traffic_policy "+status, true)
		s.wakeAuthorizationSync()
		return
	}
	s.recordAuthorizationAck(ctx, server, model.AuthorizationAck{Revision: payload.Authorization.Revision, Sequence: payload.Authorization.Sequence, Digest: payload.Authorization.Digest, Confirmed: true})
}

// recordAuthorizationApplied consumes the opaque confirmation metadata an Agent
// reports in health reports and pull requests.
func (s *Server) recordAuthorizationApplied(ctx context.Context, server *model.Server, applied *model.AuthorizationAppliedSnapshot) {
	if applied == nil || applied.Revision <= 0 {
		return
	}
	s.recordAuthorizationAck(ctx, server, model.AuthorizationAck{Revision: applied.Revision, Sequence: applied.Sequence, Digest: applied.Digest, BootID: applied.BootID, Confirmed: true})
}

// agentAuthorization serves GET /api/v1/agent/authorization: the same signed
// envelope the control channel pushes, for Agents that poll on start, after a
// reconnect, or when they detect a revision gap. The request headers carry the
// Agent's currently applied identity so the pull doubles as a confirmation.
func (s *Server) agentAuthorization(w http.ResponseWriter, r *http.Request) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowAgentRate(w, "agent-authorization:"+server.AgentID, 30, time.Minute) {
		return
	}
	if applied := parseAppliedAuthorizationHeaders(r); applied != nil {
		s.recordAuthorizationApplied(r.Context(), server, applied)
	}
	lease, err := s.currentAuthorizationLease(r.Context(), server.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	envelope, err := s.authorizationEnvelopeFor(*server, lease, "")
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if err := s.store.RecordAuthorizationDelivery(r.Context(), server.ID, lease.Revision, lease.Sequence, envelope.MessageID); err != nil {
		log.Printf("authorization pull server=%d: record delivery: %v", server.ID, err)
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, envelope)
}

const (
	headerAuthorizationAppliedRevision = "X-Oboard-Authorization-Revision"
	headerAuthorizationAppliedSequence = "X-Oboard-Authorization-Sequence"
	headerAuthorizationAppliedDigest   = "X-Oboard-Authorization-Digest"
	headerAuthorizationBootID          = "X-Oboard-Authorization-Boot-Id"
)

func parseAppliedAuthorizationHeaders(r *http.Request) *model.AuthorizationAppliedSnapshot {
	revision, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get(headerAuthorizationAppliedRevision)), 10, 64)
	if err != nil || revision <= 0 {
		return nil
	}
	sequence, _ := strconv.ParseInt(strings.TrimSpace(r.Header.Get(headerAuthorizationAppliedSequence)), 10, 64)
	digest := strings.TrimSpace(r.Header.Get(headerAuthorizationAppliedDigest))
	bootID := strings.TrimSpace(r.Header.Get(headerAuthorizationBootID))
	if len(digest) > 128 || len(bootID) > 128 {
		return nil
	}
	return &model.AuthorizationAppliedSnapshot{Revision: revision, Sequence: sequence, Digest: digest, BootID: bootID}
}