package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// TestSubscriptionOneTimeTokenConcurrentConsume drives one one-time token from
// many clients at once. Exactly one caller may be authorized, and the winner
// must be the caller that actually burned the token. Every loser must fail
// closed rather than be served a second copy.
func TestSubscriptionOneTimeTokenConcurrentConsume(t *testing.T) {
	for _, auditEnabled := range []bool{true, false} {
		name := "audit_disabled"
		if auditEnabled {
			name = "audit_enabled"
		}
		t.Run(name, func(t *testing.T) {
			s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			user := createSubscriptionAuditUser(t, s, "one-time-race-"+name, "persistent-"+name, model.RoleViewer)
			const oneTimeToken = "race-one-time-token"
			if err := s.CreateOneTimeSubscriptionToken(ctx, user.ID, oneTimeToken); err != nil {
				t.Fatal(err)
			}

			const clients = 8
			var mu sync.Mutex
			allowed, burned := 0, 0
			var unexpected []error
			var start sync.WaitGroup
			var done sync.WaitGroup
			start.Add(1)
			for i := 0; i < clients; i++ {
				done.Add(1)
				go func() {
					defer done.Done()
					start.Wait()
					event := subscriptionAuditEvent(user.ID, "1.1.1.1", "广东", time.Now().UTC())
					decision, err := s.AuthorizeSubscriptionPull(ctx, user.ID, oneTimeToken, event, DefaultSubscriptionAuditPolicy(), SubscriptionAuditOptions{AuditEnabled: auditEnabled, Action: model.AuditActionRestrict})
					mu.Lock()
					defer mu.Unlock()
					switch {
					case err == nil:
						if decision.Allowed {
							allowed++
						}
						if decision.Burned {
							burned++
						}
						if decision.Allowed != decision.Burned {
							unexpected = append(unexpected, errors.New("one-time decision served without burning the token"))
						}
					case errors.Is(err, sql.ErrNoRows):
						// The token was already consumed by another client.
					default:
						unexpected = append(unexpected, err)
					}
				}()
			}
			start.Done()
			done.Wait()
			if len(unexpected) != 0 {
				t.Fatalf("unexpected errors: %v", unexpected)
			}
			if allowed != 1 || burned != 1 {
				t.Fatalf("allowed=%d burned=%d, expected exactly one of each", allowed, burned)
			}
			var remaining int
			if err := s.db.QueryRowContext(ctx, `select count(1) from subscription_one_time_tokens where user_id=?`, user.ID).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining != 0 {
				t.Fatalf("one-time token rows remaining = %d", remaining)
			}
		})
	}
}
