package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Authorization pending reasons are the operator-facing explanation of why a
// server's confirmed authorization still trails the desired one.
const (
	AuthorizationPendingAgentOffline       = "agent_offline"
	AuthorizationPendingDelivering         = "delivering"
	AuthorizationPendingAgentUpgrade       = "agent_upgrade_required"
	AuthorizationPendingDeliveryFailed     = "delivery_failed"
	AuthorizationPendingUnenrolled         = "unenrolled"
	AuthorizationPendingAwaitingConfirm    = "awaiting_confirmation"
	AuthorizationPendingSuperseded         = "superseded"
	AuthorizationPendingRuntimeUnavailable = "runtime_unavailable"
)

// AuthorizationState is the durable per-server authorization ledger. The
// desired revision is semantic: it advances only when the effective grant set
// (credential keys plus their business boundaries) of that server changes, not
// on every renewal and not on unrelated routing writes. The confirmed columns
// come from the Agent's acknowledgement and are the only evidence that a
// revoke has taken effect on the data plane.
type AuthorizationState struct {
	ServerID                 int64
	DesiredRevision          int64
	DesiredDigest            string
	DesiredKeys              []string
	EvaluatedRoutingRevision uint64
	IssuedSequence           int64
	LastIssuedExpiresAt      *time.Time
	DeliveredRevision        int64
	DeliveredSequence        int64
	DeliveredMessageID       string
	DeliveredAt              *time.Time
	ConfirmedRevision        int64
	ConfirmedSequence        int64
	ConfirmedDigest          string
	ConfirmedBootID          string
	ConfirmedAt              *time.Time
	PendingReason            string
	LastError                string
	Retryable                bool
	UpdatedAt                time.Time
}

// Confirmed reports whether the data plane has acknowledged the desired revision.
func (s AuthorizationState) Confirmed() bool {
	return s.DesiredRevision > 0 && s.ConfirmedRevision >= s.DesiredRevision
}

// AuthorizationDenial is an emergency deny watermark for one credential on one
// server. It is written when a credential leaves the desired grant set and is
// kept until the Agent has confirmed a revision at or above deny_revision and
// every lease issued before the change has expired (lease_bound_until), which
// is the last instant an already-issued grant could still admit the credential.
type AuthorizationDenial struct {
	ServerID        int64
	CredentialID    string
	DenyRevision    int64
	LeaseBoundUntil time.Time
	ConfirmedAt     *time.Time
	CreatedAt       time.Time
}

const authorizationStateSelectSQL = `select server_id,desired_revision,desired_digest,desired_keys_json,evaluated_routing_revision,issued_sequence,last_issued_expires_at,delivered_revision,delivered_sequence,delivered_message_id,delivered_at,confirmed_revision,confirmed_sequence,confirmed_digest,confirmed_boot_id,confirmed_at,pending_reason,last_error,retryable,updated_at from authorization_states`

// AuthorizationDesiredEvaluation is the outcome of comparing a freshly computed
// grant projection with the stored desired state.
type AuthorizationDesiredEvaluation struct {
	State      AuthorizationState
	Changed    bool
	DeniedKeys []string
}

// EvaluateAuthorizationDesired records the projection computed at
// routingRevision for a server. When the semantic digest differs from the stored
// desired digest the desired revision advances by one, keys that disappeared
// become pending denials bound to the latest issued lease expiry, and the
// caller must deliver the new revision. Equal digests only move the evaluated
// routing watermark. The whole comparison runs in one transaction so a crash
// can never lose a revoke that the business write already committed.
func (s *Store) EvaluateAuthorizationDesired(ctx context.Context, serverID int64, routingRevision uint64, digest string, keys []string, now time.Time) (AuthorizationDesiredEvaluation, error) {
	if serverID <= 0 {
		return AuthorizationDesiredEvaluation{}, fmt.Errorf("server id must be positive")
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	keysJSON, err := json.Marshal(sorted)
	if err != nil {
		return AuthorizationDesiredEvaluation{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AuthorizationDesiredEvaluation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	ts := now.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `insert or ignore into authorization_states(server_id,updated_at) values(?,?)`, serverID, ts); err != nil {
		return AuthorizationDesiredEvaluation{}, err
	}
	current, err := scanAuthorizationState(tx.QueryRowContext(ctx, authorizationStateSelectSQL+` where server_id=?`, serverID))
	if err != nil {
		return AuthorizationDesiredEvaluation{}, err
	}
	out := AuthorizationDesiredEvaluation{State: current}
	if current.DesiredDigest == digest && current.DesiredRevision > 0 {
		if _, err := tx.ExecContext(ctx, `update authorization_states set evaluated_routing_revision=?,updated_at=? where server_id=?`, routingRevision, ts, serverID); err != nil {
			return AuthorizationDesiredEvaluation{}, err
		}
		if err := tx.Commit(); err != nil {
			return AuthorizationDesiredEvaluation{}, err
		}
		out.State.EvaluatedRoutingRevision = routingRevision
		return out, nil
	}
	nextRevision := current.DesiredRevision + 1
	previous := make(map[string]struct{}, len(current.DesiredKeys))
	for _, key := range current.DesiredKeys {
		previous[key] = struct{}{}
	}
	for _, key := range sorted {
		delete(previous, key)
	}
	// A denial is bounded by the latest lease this server could still hold.
	// Without any issued lease no grant can be live, so the bound is now.
	bound := now.UTC()
	if current.LastIssuedExpiresAt != nil && current.LastIssuedExpiresAt.After(bound) {
		bound = current.LastIssuedExpiresAt.UTC()
	}
	denied := make([]string, 0, len(previous))
	for key := range previous {
		denied = append(denied, key)
	}
	sort.Strings(denied)
	for _, key := range denied {
		if _, err := tx.ExecContext(ctx, `insert into authorization_denials(server_id,credential_id,deny_revision,lease_bound_until,confirmed_at,created_at) values(?,?,?,?,null,?) on conflict(server_id,credential_id) do update set deny_revision=excluded.deny_revision,lease_bound_until=case when excluded.lease_bound_until>authorization_denials.lease_bound_until then excluded.lease_bound_until else authorization_denials.lease_bound_until end,confirmed_at=null`, serverID, key, nextRevision, bound.Format(time.RFC3339Nano), ts); err != nil {
			return AuthorizationDesiredEvaluation{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `update authorization_states set desired_revision=?,desired_digest=?,desired_keys_json=?,evaluated_routing_revision=?,pending_reason=case when pending_reason='' then ? else pending_reason end,updated_at=? where server_id=?`, nextRevision, digest, string(keysJSON), routingRevision, AuthorizationPendingDelivering, ts, serverID); err != nil {
		return AuthorizationDesiredEvaluation{}, err
	}
	if err := tx.Commit(); err != nil {
		return AuthorizationDesiredEvaluation{}, err
	}
	out.Changed = true
	out.DeniedKeys = denied
	out.State.DesiredRevision = nextRevision
	out.State.DesiredDigest = digest
	out.State.DesiredKeys = sorted
	out.State.EvaluatedRoutingRevision = routingRevision
	if out.State.PendingReason == "" {
		out.State.PendingReason = AuthorizationPendingDelivering
	}
	return out, nil
}

// IssueAuthorizationSequence allocates the next renewal sequence for a lease
// of the given revision and records the expiry bound of that issuance. The
// sequence orders renewals within one revision on the Agent and kernel.
func (s *Store) IssueAuthorizationSequence(ctx context.Context, serverID, revision int64, expiresAt time.Time) (int64, error) {
	if serverID <= 0 || revision <= 0 {
		return 0, fmt.Errorf("server id and authorization revision must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `insert or ignore into authorization_states(server_id,updated_at) values(?,?)`, serverID, ts); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update authorization_states set issued_sequence=issued_sequence+1,last_issued_expires_at=case when last_issued_expires_at is null or last_issued_expires_at<? then ? else last_issued_expires_at end,updated_at=? where server_id=?`, expiresAt.UTC().Format(time.RFC3339Nano), expiresAt.UTC().Format(time.RFC3339Nano), ts, serverID); err != nil {
		return 0, err
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `select issued_sequence from authorization_states where server_id=?`, serverID).Scan(&sequence); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return sequence, nil
}

// RecordAuthorizationDelivery notes that a signed message for revision/sequence
// left the Controller. It never touches the confirmed columns.
func (s *Store) RecordAuthorizationDelivery(ctx context.Context, serverID, revision, sequence int64, messageID string) error {
	if serverID <= 0 {
		return fmt.Errorf("server id must be positive")
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `update authorization_states set delivered_revision=?,delivered_sequence=?,delivered_message_id=?,delivered_at=?,pending_reason=case when confirmed_revision>=desired_revision then '' else ? end,last_error='',retryable=1,updated_at=? where server_id=?`, revision, sequence, strings.TrimSpace(messageID), ts, AuthorizationPendingAwaitingConfirm, ts, serverID)
	return err
}

// RecordAuthorizationConfirmation applies an Agent acknowledgement. Older or
// equal (revision, sequence) pairs are ignored so a late acknowledgement of a
// superseded message cannot move the confirmed watermark backwards. It also
// marks every denial at or below the confirmed revision as confirmed and
// reports whether the confirmation advanced the ledger.
func (s *Store) RecordAuthorizationConfirmation(ctx context.Context, serverID, revision, sequence int64, digest, bootID string) (bool, error) {
	if serverID <= 0 || revision <= 0 {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `insert or ignore into authorization_states(server_id,updated_at) values(?,?)`, serverID, ts); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `update authorization_states set confirmed_revision=?,confirmed_sequence=?,confirmed_digest=?,confirmed_boot_id=?,confirmed_at=?,pending_reason=case when desired_revision<=? then '' else pending_reason end,last_error=case when desired_revision<=? then '' else last_error end,updated_at=? where server_id=? and (confirmed_revision<? or (confirmed_revision=? and confirmed_sequence<?))`, revision, sequence, strings.TrimSpace(digest), strings.TrimSpace(bootID), ts, revision, revision, ts, serverID, revision, revision, sequence)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `update authorization_denials set confirmed_at=? where server_id=? and confirmed_at is null and deny_revision<=?`, ts, serverID, revision); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// MarkAuthorizationPending records why a desired revision is not confirmed yet.
func (s *Store) MarkAuthorizationPending(ctx context.Context, serverID int64, reason, lastError string, retryable bool) error {
	if serverID <= 0 {
		return fmt.Errorf("server id must be positive")
	}
	if len(lastError) > 2000 {
		lastError = lastError[:2000]
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `insert or ignore into authorization_states(server_id,updated_at) values(?,?)`, serverID, ts); err != nil {
		return err
	}
	retryFlag := 0
	if retryable {
		retryFlag = 1
	}
	_, err := s.db.ExecContext(ctx, `update authorization_states set pending_reason=?,last_error=?,retryable=?,updated_at=? where server_id=?`, strings.TrimSpace(reason), strings.TrimSpace(lastError), retryFlag, ts, serverID)
	return err
}

// AuthorizationState returns the ledger row for one server. A server without a
// row yields a zero state and no error.
func (s *Store) AuthorizationState(ctx context.Context, serverID int64) (AuthorizationState, error) {
	state, err := scanAuthorizationState(s.db.QueryRowContext(ctx, authorizationStateSelectSQL+` where server_id=?`, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizationState{ServerID: serverID, Retryable: true}, nil
	}
	return state, err
}

// ListAuthorizationStates returns every ledger row ordered by server.
func (s *Store) ListAuthorizationStates(ctx context.Context) ([]AuthorizationState, error) {
	rows, err := s.db.QueryContext(ctx, authorizationStateSelectSQL+` order by server_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuthorizationState{}
	for rows.Next() {
		state, err := scanAuthorizationState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, state)
	}
	return out, rows.Err()
}

// StaleAuthorizationServerIDs returns enrolled servers whose ledger has not
// been evaluated at routingRevision or whose desired revision is unconfirmed.
// It is the worker's work list; it is deliberately a lightweight query rather
// than a full server listing.
func (s *Store) StaleAuthorizationServerIDs(ctx context.Context, routingRevision uint64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `select srv.id from servers srv left join authorization_states st on st.server_id=srv.id where coalesce(srv.agent_id,'')<>'' and (st.server_id is null or st.evaluated_routing_revision<? or st.confirmed_revision<st.desired_revision) order by srv.id`, routingRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// EnrolledServerIDs lists servers that have an Agent identity, in ID order.
func (s *Store) EnrolledServerIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `select id from servers where coalesce(agent_id,'')<>'' order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PendingAuthorizationDenials lists credential keys the server must deny even
// before a full snapshot lands: denials not yet confirmed at their revision.
func (s *Store) PendingAuthorizationDenials(ctx context.Context, serverID int64) ([]AuthorizationDenial, error) {
	rows, err := s.db.QueryContext(ctx, `select server_id,credential_id,deny_revision,lease_bound_until,confirmed_at,created_at from authorization_denials where server_id=? and confirmed_at is null order by credential_id`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuthorizationDenial{}
	for rows.Next() {
		var item AuthorizationDenial
		var bound, confirmed, created sql.NullString
		if err := rows.Scan(&item.ServerID, &item.CredentialID, &item.DenyRevision, &bound, &confirmed, &created); err != nil {
			return nil, err
		}
		item.LeaseBoundUntil = parseTime(bound.String)
		item.ConfirmedAt = parseNullTimePtr(confirmed)
		item.CreatedAt = parseTime(created.String)
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListAuthorizationDenials returns every denial for a server, confirmed or not.
func (s *Store) ListAuthorizationDenials(ctx context.Context, serverID int64) ([]AuthorizationDenial, error) {
	rows, err := s.db.QueryContext(ctx, `select server_id,credential_id,deny_revision,lease_bound_until,confirmed_at,created_at from authorization_denials where server_id=? order by credential_id`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuthorizationDenial{}
	for rows.Next() {
		var item AuthorizationDenial
		var bound, confirmed, created sql.NullString
		if err := rows.Scan(&item.ServerID, &item.CredentialID, &item.DenyRevision, &bound, &confirmed, &created); err != nil {
			return nil, err
		}
		item.LeaseBoundUntil = parseTime(bound.String)
		item.ConfirmedAt = parseNullTimePtr(confirmed)
		item.CreatedAt = parseTime(created.String)
		out = append(out, item)
	}
	return out, rows.Err()
}

// PruneAuthorizationDenials removes denials that are confirmed and whose lease
// bound has passed: no issued lease can admit the credential any more, and the
// kernel has acknowledged a revision that no longer grants it.
func (s *Store) PruneAuthorizationDenials(ctx context.Context, now time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `delete from authorization_denials where confirmed_at is not null and lease_bound_until<?`, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type authorizationStateScanner interface {
	Scan(dest ...any) error
}

func scanAuthorizationState(scanner authorizationStateScanner) (AuthorizationState, error) {
	var item AuthorizationState
	var keysJSON string
	var lastIssued, deliveredAt, confirmedAt sql.NullString
	var retryable int
	var updatedAt string
	if err := scanner.Scan(&item.ServerID, &item.DesiredRevision, &item.DesiredDigest, &keysJSON, &item.EvaluatedRoutingRevision, &item.IssuedSequence, &lastIssued, &item.DeliveredRevision, &item.DeliveredSequence, &item.DeliveredMessageID, &deliveredAt, &item.ConfirmedRevision, &item.ConfirmedSequence, &item.ConfirmedDigest, &item.ConfirmedBootID, &confirmedAt, &item.PendingReason, &item.LastError, &retryable, &updatedAt); err != nil {
		return AuthorizationState{}, err
	}
	if strings.TrimSpace(keysJSON) != "" {
		if err := json.Unmarshal([]byte(keysJSON), &item.DesiredKeys); err != nil {
			return AuthorizationState{}, fmt.Errorf("decode desired authorization keys: %w", err)
		}
	}
	item.LastIssuedExpiresAt = parseNullTimePtr(lastIssued)
	item.DeliveredAt = parseNullTimePtr(deliveredAt)
	item.ConfirmedAt = parseNullTimePtr(confirmedAt)
	item.Retryable = retryable != 0
	item.UpdatedAt = parseTime(updatedAt)
	return item, nil
}
