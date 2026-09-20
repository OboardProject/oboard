package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) agentAccountActivity(w http.ResponseWriter, r *http.Request) {
	s.agentAccountActivityAt(w, r, time.Now().UTC())
}

func (s *Server) agentAccountActivityAt(w http.ResponseWriter, r *http.Request, now time.Time) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !s.allowAgentRate(w, "agent-account-activity:"+server.AgentID, 120, time.Minute) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var report accountActivityWireReport
	if err := decoder.Decode(&report); err != nil {
		fail(w, errors.New("invalid account activity report"), 400)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		fail(w, errors.New("invalid account activity framing"), 400)
		return
	}
	terminal := func(reason string) {
		write(w, http.StatusOK, map[string]any{"accepted": false, "terminal": true, "reason": reason, "sequence": report.Sequence})
	}
	if !s.effectiveConnectionAuditEnabled(r.Context(), server) {
		terminal("disabled")
		return
	}
	if err := validateAccountActivityWire(report); err != nil {
		fail(w, err, 400)
		return
	}
	if report.CollectorStartedAt > now.Add(30*time.Second).UnixNano() {
		fail(w, errors.New("activity collector generation is in the future"), 400)
		return
	}
	if report.MinuteUnix >= now.Truncate(time.Minute).Unix() {
		fail(w, errors.New("activity minute has not ended"), 400)
		return
	}
	routing, err := s.routingSnapshot(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	allowed := routing.allowedAccessPairs()
	_, sourcePolicy, err := s.accountAuditSourcePolicy(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	batch := store.AccountActivityBatch{ReceiptDigest: accountActivityReceiptDigest(s.sessionSecret, report), ServerID: server.ID, CollectorBootID: report.CollectorBootID, CollectorStartedAt: report.CollectorStartedAt, StreamType: report.StreamType, Sequence: report.Sequence, MinuteUnix: report.MinuteUnix, SourceVersion: sourcePolicy.ID(), Complete: report.Complete, ClockState: report.ClockState, DroppedUpdates: report.DroppedUpdates, UnknownSourceUpdates: report.UnknownSourceUpdates}
	for _, item := range report.Items {
		inbound, exists := routing.inboundsByID[item.InboundID]
		if !exists {
			terminal("subject_removed")
			return
		}
		location := inbound.ServerID == server.ID
		if item.PathID > 0 {
			path, exists := routing.pathsByID[item.PathID]
			if !exists {
				terminal("subject_removed")
				return
			}
			if path.InboundID != inbound.ID {
				fail(w, errors.New("activity path ownership mismatch"), 403)
				return
			}
			location = core.IsProxyPathAccountingLocation(server.ID, inbound.ID, path.ID, routing.data.ProxyPaths, routing.data.ProxyPathSteps, routing.data.Inbounds)
			if !path.Enabled {
				terminal("subject_removed")
				return
			}
		} else if core.ProxyPathRequiresAccountingPathID(inbound.ID, routing.data.ProxyPaths, routing.data.ProxyPathSteps, routing.data.Inbounds) {
			fail(w, errors.New("activity accounting path required"), 403)
			return
		}
		if !location {
			fail(w, errors.New("activity accounting location mismatch"), 403)
			return
		}
		if (report.StreamType == "ssh") != (inbound.Protocol == "ssh") {
			fail(w, errors.New("activity stream mismatch"), 400)
			return
		}
		user, exists := routing.usersByID[item.UserID]
		if !exists || user.Status != "active" || !inbound.Enabled {
			terminal("subject_removed")
			return
		}
		if _, exists := allowed[accessPair{inboundID: inbound.ID, userID: item.UserID, pathID: item.PathID}]; !exists {
			terminal("subject_removed")
			return
		}
		group, err := accountActivitySourceGroup(s.sessionSecret, item.UserID, item.SourcePrefix, sourcePolicy)
		if err != nil {
			fail(w, err, 400)
			return
		}
		counter, err := accountActivitySourceGroup(s.sessionSecret, item.UserID, item.SourcePrefix)
		if err != nil {
			fail(w, err, 400)
			return
		}
		batch.Items = append(batch.Items, store.AccountActivityBatchItem{AccountID: item.UserID, InboundID: item.InboundID, PathID: item.PathID, SourceGroup: group, CounterSource: counter, ActivityBits: item.ActivityBits, UploadBytes: uint64(item.UploadBytes), DownloadBytes: uint64(item.DownloadBytes)})
	}
	receipt, err := s.store.ReceiveAccountActivityBatch(r.Context(), batch, now)
	switch {
	case errors.Is(err, store.ErrAccountActivityExpired):
		terminal("expired")
	case errors.Is(err, store.ErrAccountActivityConflict):
		fail(w, errors.New("activity identity conflict"), 409)
	case errors.Is(err, store.ErrAccountActivityCapacity):
		fail(w, errors.New("activity capacity reached"), 503)
	case errors.Is(err, store.ErrAccountActivityInvalid):
		fail(w, errors.New("invalid activity report"), 400)
	case err != nil:
		fail(w, errors.New("activity persistence unavailable"), 503)
	default:
		write(w, 200, receipt)
	}
}
