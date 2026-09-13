package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// serverDeletionHistoryBatch bounds one purge transaction. Large enough that a
// year of samples drains in a few hundred statements, small enough that no
// single delete holds SQLite's writer while the panel is in use.
const serverDeletionHistoryBatch = 2000

// serverDeletionRetryPeriod is how often an unfinished external cleanup is
// retried. A DNS provider that is down must not keep the server undeletable,
// so the record is removed first and its records are released afterwards.
const serverDeletionRetryPeriod = time.Minute

// serverDeletionOwnedCommentPrefix is the marker OBoard writes into the
// provider comment of every record it manages. It is the second ownership
// signal, used for records this installation created before it started
// recording record metadata locally.
const serverDeletionOwnedCommentPrefix = "OBoard:"

// serverDeletionDNSRecord is one DNS record this server owns, captured before
// the server row is deleted. Ownership cannot be resolved afterwards:
// dns_record_metadata.server_id is `on delete set null`, so the moment the row
// goes away the provider record looks like it belongs to nobody.
type serverDeletionDNSRecord struct {
	CredentialID int64  `json:"credential_id"`
	ZoneID       int64  `json:"zone_id"`
	Domain       string `json:"domain"`
	RecordID     string `json:"record_id"`
}

type serverDeletionPayload struct {
	DNSRecords []serverDeletionDNSRecord `json:"dns_records"`
}

// captureServerDNSOwnership records which provider records belong to this
// server. It only reads local state, so a provider that is unreachable cannot
// block the delete from starting.
func (s *Server) captureServerDNSOwnership(ctx context.Context, serverID int64, inbounds []model.Inbound) serverDeletionPayload {
	payload := serverDeletionPayload{}
	seen := map[string]bool{}
	for _, inbound := range inbounds {
		if inbound.ServerID != serverID || !inbound.DNSSyncEnabled || inbound.DNSCredentialID == nil || !isDNSDomainName(inbound.DNSDomain) {
			continue
		}
		credential, err := s.store.GetDNSCredential(ctx, *inbound.DNSCredentialID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			log.Printf("server delete %d: read DNS credential %d: %v", serverID, *inbound.DNSCredentialID, err)
			continue
		}
		zone, err := selectDNSCredentialZone(*credential, inbound.DNSDomain, inbound.ServerID)
		if err != nil {
			log.Printf("server delete %d: resolve DNS zone for %s: %v", serverID, inbound.DNSDomain, err)
			continue
		}
		domain := normalizeDomainName(inbound.DNSDomain)
		metadata, err := s.store.ListDNSRecordMetadata(ctx, zone.ID)
		if err != nil {
			log.Printf("server delete %d: read DNS record metadata for zone %d: %v", serverID, zone.ID, err)
			continue
		}
		// The domain alone is recorded so a record this installation created
		// before it tracked metadata can still be matched by its own comment.
		key := fmt.Sprintf("%d/%d/%s/", credential.ID, zone.ID, domain)
		if !seen[key] {
			seen[key] = true
			payload.DNSRecords = append(payload.DNSRecords, serverDeletionDNSRecord{CredentialID: credential.ID, ZoneID: zone.ID, Domain: domain})
		}
		for recordID, record := range metadata {
			if record.ServerID == nil || *record.ServerID != serverID {
				continue
			}
			key := fmt.Sprintf("%d/%d/%s/%s", credential.ID, zone.ID, domain, recordID)
			if seen[key] {
				continue
			}
			seen[key] = true
			payload.DNSRecords = append(payload.DNSRecords, serverDeletionDNSRecord{CredentialID: credential.ID, ZoneID: zone.ID, Domain: domain, RecordID: recordID})
		}
	}
	return payload
}

// purgeServerHistory drains the unbounded per-server history in bounded
// transactions so the final row delete stays small.
func (s *Server) purgeServerHistory(ctx context.Context, serverID int64) error {
	for {
		removed, err := s.store.PurgeServerHistoryBatch(ctx, serverID, serverDeletionHistoryBatch)
		if err != nil {
			return err
		}
		if removed == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

// releaseServerDeletionExternals deletes the DNS records the server owned. It
// runs after the server row is gone, so a failure is retried rather than
// turning into a failed delete.
func (s *Server) releaseServerDeletionExternals(ctx context.Context, deletion store.ServerDeletion) error {
	var payload serverDeletionPayload
	if strings.TrimSpace(deletion.Payload) != "" {
		if err := json.Unmarshal([]byte(deletion.Payload), &payload); err != nil {
			// A payload that cannot be read will never become readable; the
			// deletion is finished rather than retried forever.
			log.Printf("server delete %d: unreadable cleanup payload, nothing released: %v", deletion.ServerID, err)
			return nil
		}
	}
	type zoneWork struct {
		credentialID int64
		zoneID       int64
		domains      map[string]bool
		recordIDs    map[string]bool
	}
	zones := map[int64]*zoneWork{}
	for _, record := range payload.DNSRecords {
		work := zones[record.ZoneID]
		if work == nil {
			work = &zoneWork{credentialID: record.CredentialID, zoneID: record.ZoneID, domains: map[string]bool{}, recordIDs: map[string]bool{}}
			zones[record.ZoneID] = work
		}
		if record.Domain != "" {
			work.domains[record.Domain] = true
		}
		if record.RecordID != "" {
			work.recordIDs[record.RecordID] = true
		}
	}
	var failures []string
	for _, work := range zones {
		if err := s.releaseServerDeletionZone(ctx, work.credentialID, work.zoneID, work.domains, work.recordIDs); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (s *Server) releaseServerDeletionZone(ctx context.Context, credentialID, zoneID int64, domains, recordIDs map[string]bool) error {
	credential, err := s.store.GetDNSCredential(ctx, credentialID)
	if errors.Is(err, sql.ErrNoRows) {
		// The credential is gone, so this installation can no longer reach the
		// provider at all. There is nothing left to retry.
		return nil
	}
	if err != nil {
		return err
	}
	var zone *model.DNSCredentialZone
	for i := range credential.Zones {
		if credential.Zones[i].ID == zoneID {
			zone = &credential.Zones[i]
		}
	}
	if zone == nil {
		return nil
	}
	client, err := s.dnsProviderClient(credentialForDNSZone(*credential, *zone))
	if err != nil {
		return err
	}
	records, err := client.ListRecords(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		switch strings.ToUpper(record.Type) {
		case "A", "AAAA", "CNAME":
		default:
			continue
		}
		owned := recordIDs[record.ID]
		if !owned && domains[normalizeDomainName(record.Name)] && strings.HasPrefix(strings.TrimSpace(record.Comment), serverDeletionOwnedCommentPrefix) {
			// Created by this installation before record metadata existed.
			// A record OBoard never wrote keeps its own comment and is left
			// alone, even when it carries the same name.
			owned = true
		}
		if !owned {
			continue
		}
		if err := client.DeleteRecord(ctx, record.ID); err != nil {
			return err
		}
		if err := s.store.DeleteDNSRecordMetadata(ctx, zoneID, record.ID); err != nil {
			return err
		}
	}
	return nil
}

// errServerExternalCleanupPending marks a failure that happened after the
// server record was already removed. The delete itself succeeded; only owned
// external state is still to be released, and the worker keeps retrying it.
var errServerExternalCleanupPending = errors.New("服务器已删除，外部资源清理将自动重试")

// finishServerDeletion drives one deletion from wherever it currently is to
// completion. It is safe to call repeatedly and from the resume worker.
func (s *Server) finishServerDeletion(ctx context.Context, deletion store.ServerDeletion) error {
	if deletion.Stage == store.ServerDeletionPurging {
		if err := s.purgeServerHistory(ctx, deletion.ServerID); err != nil {
			return err
		}
		if err := s.store.DeleteServer(ctx, deletion.ServerID); err != nil {
			return err
		}
		s.forgetServerRuntimeState(deletion.ServerID)
		if err := s.store.SetServerDeletionStage(ctx, deletion.ServerID, store.ServerDeletionExternal); err != nil {
			return err
		}
		deletion.Stage = store.ServerDeletionExternal
	}
	if err := s.releaseServerDeletionExternals(ctx, deletion); err != nil {
		if recordErr := s.store.RecordServerDeletionFailure(ctx, deletion.ServerID, err.Error()); recordErr != nil {
			log.Printf("server delete %d: record cleanup failure: %v", deletion.ServerID, recordErr)
		}
		return fmt.Errorf("%w: %v", errServerExternalCleanupPending, err)
	}
	return s.store.CompleteServerDeletion(ctx, deletion.ServerID)
}

// forgetServerRuntimeState drops every per-server cache entry and lock object
// that the deleted server owned.
func (s *Server) forgetServerRuntimeState(serverID int64) {
	s.forgetLatencyProbePlan(serverID)
	s.forgetRemoteAccessStatus(serverID)
	s.forgetPresenceAuditState(serverID)
	s.forgetAuthorizationLease(serverID)
	s.forgetRuntimeUsersSettled(serverID)
	s.store.ForgetMetricSampleAdmission(serverID)
}

// StartServerDeletionWorker resumes deletions that a restart interrupted and
// retries external cleanup that failed. Without it, a Controller killed between
// the row delete and the provider call would leave DNS records pointing at a
// server nobody can see.
func (s *Server) StartServerDeletionWorker(ctx context.Context) {
	ticker := time.NewTicker(serverDeletionRetryPeriod)
	defer ticker.Stop()
	s.runServerDeletions(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runServerDeletions(ctx)
		}
	}
}

func (s *Server) runServerDeletions(ctx context.Context) {
	deletions, err := s.store.ListServerDeletions(ctx)
	if err != nil {
		log.Printf("server delete: list unfinished deletions: %v", err)
		return
	}
	for _, deletion := range deletions {
		if ctx.Err() != nil {
			return
		}
		if err := s.finishServerDeletion(ctx, deletion); err != nil {
			log.Printf("server delete %d (%s): %v", deletion.ServerID, deletion.Name, err)
		}
	}
}
