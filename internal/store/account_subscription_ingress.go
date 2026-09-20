package store

import "time"

// ChargeAuthenticatedSubscriptionRequest runs after token ownership resolution
// and before conversion. Invalid representations still consume account budget.
func (s *Store) ChargeAuthenticatedSubscriptionRequest(userID int64, limit int, at time.Time) (bool, time.Duration) {
	if userID <= 0 {
		return false, time.Minute
	}
	limited, retry := s.consumeAccountSubscriptionLimit(userID, limit, at)
	return !limited, retry
}
