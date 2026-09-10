package store

import (
	"context"
)

// AccessSyncCandidate is the minimum state a reconciliation pass needs to
// decide whether one server requires work at all.
//
// A recovery scan must not assemble a full server object, or read the whole
// delivery ledger row per server, just to conclude that the server is already
// confirmed. These columns answer that question for the whole fleet in one
// query; only the servers that survive the check pay for a full load, a package
// build, or a lease evaluation.
type AccessSyncCandidate struct {
	ServerID                 int64
	DesiredRevision          int64
	ConfirmedRevision        int64
	EvaluatedRoutingRevision uint64
	PendingReason            string
	Retryable                bool
}

// Confirmed reports whether the Agent has acknowledged the desired revision.
// It matches AuthorizationState.Confirmed and RuntimeUserState.Confirmed: a
// server with no ledger row yet is never confirmed.
func (c AccessSyncCandidate) Confirmed() bool {
	return c.DesiredRevision > 0 && c.ConfirmedRevision >= c.DesiredRevision
}

// Settled reports whether the ledger alone says this server needs nothing for
// the given routing revision. It is only half the decision: a caller must still
// establish that no business time boundary passed since the last evaluation,
// because a boundary changes what is owed without changing any row.
func (c AccessSyncCandidate) Settled(routingRevision uint64) bool {
	return c.Confirmed() && c.EvaluatedRoutingRevision == routingRevision
}

func (s *Store) listAccessSyncCandidates(ctx context.Context, query string) ([]AccessSyncCandidate, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccessSyncCandidate{}
	for rows.Next() {
		var item AccessSyncCandidate
		var retryable int
		if err := rows.Scan(&item.ServerID, &item.DesiredRevision, &item.ConfirmedRevision, &item.EvaluatedRoutingRevision, &item.PendingReason, &retryable); err != nil {
			return nil, err
		}
		item.Retryable = retryable != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListAuthorizationSyncCandidates returns the authorization ledger summary of
// every enrolled server in one query.
func (s *Store) ListAuthorizationSyncCandidates(ctx context.Context) ([]AccessSyncCandidate, error) {
	return s.listAccessSyncCandidates(ctx, `select srv.id,coalesce(st.desired_revision,0),coalesce(st.confirmed_revision,0),coalesce(st.evaluated_routing_revision,0),coalesce(st.pending_reason,''),coalesce(st.retryable,1) from servers srv left join authorization_states st on st.server_id=srv.id where coalesce(srv.agent_id,'')<>'' order by srv.id`)
}

// ListRuntimeUsersSyncCandidates returns the runtime-user ledger summary of
// every enrolled server in one query.
func (s *Store) ListRuntimeUsersSyncCandidates(ctx context.Context) ([]AccessSyncCandidate, error) {
	return s.listAccessSyncCandidates(ctx, `select srv.id,coalesce(st.desired_revision,0),coalesce(st.confirmed_revision,0),coalesce(st.evaluated_routing_revision,0),coalesce(st.pending_reason,''),coalesce(st.retryable,1) from servers srv left join runtime_user_states st on st.server_id=srv.id where coalesce(srv.agent_id,'')<>'' order by srv.id`)
}
