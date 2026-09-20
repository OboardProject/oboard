package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OboardProject/oboard/internal/auditrisk"
	"github.com/OboardProject/oboard/internal/store"
)

var auditRiskQueueConfig = coalescedQueueConfig{name: "evaluate audit risks user", workers: 1, size: 256, debounce: 2 * time.Second, minInterval: 15 * time.Second, maxRetry: 5 * time.Minute}

func newAuditRiskQueue(evaluate func(context.Context, int64) error) *coalescedQueue {
	return newCoalescedQueue(auditRiskQueueConfig, evaluate)
}

// Historical detail reports never supply business slices or trigger old scoring.
func (s *Server) evaluateConnectionAuditRisks(ctx context.Context, userID int64) error {
	if !s.auditSettingsState(ctx).Enabled {
		return nil
	}
	_, err := s.store.MarkAccountAuditDirty(ctx, userID)
	return err
}

func (s *Server) evaluateAccountAuditWork(ctx context.Context, now time.Time, after int64) (int64, error) {
	if !s.auditSettingsState(ctx).Enabled {
		return after, nil
	}
	_, sourcePolicy, err := s.accountAuditSourcePolicy(ctx)
	if err != nil {
		return after, err
	}
	work, err := s.store.ListAccountAuditWork(ctx, after, 64)
	if err != nil {
		return after, err
	}
	var failures error
	for _, item := range work {
		after = item.UserID
		if err := s.evaluateAccountAuditItem(ctx, now, item.UserID, item.Revision, sourcePolicy.ID()); err != nil {
			failures = errors.Join(failures, fmt.Errorf("account %d: %w", item.UserID, err))
		}
		if ctx.Err() != nil {
			return after, errors.Join(failures, ctx.Err())
		}
	}
	if len(work) < 64 {
		after = 0
	}
	return after, failures
}

func (s *Server) evaluateAccountAuditItem(ctx context.Context, now time.Time, userID, revision int64, sourceVersion string) error {
	if !s.auditSettingsState(ctx).Enabled {
		return nil
	}
	configured, err := s.store.GetAccountAuditPolicy(ctx)
	if err != nil {
		return err
	}
	policy := configured.Policy
	baseline, err := s.store.LoadAccountActivityBaseline(ctx, userID, now, policy.Version, sourceVersion)
	if err != nil {
		return err
	}
	policy, _, err = auditrisk.AdaptPolicy(policy, baseline)
	if err != nil {
		return err
	}
	features, quality, err := s.store.LoadAccountAuditFeatures(ctx, userID, now, policy, sourceVersion)
	if err != nil {
		return err
	}
	if err := s.attachAccountSubscriptionFeatures(ctx, &features, &quality, policy, sourceVersion, now); err != nil {
		return err
	}
	features.Resources, err = s.store.LoadAccountAuditResourceDimensions(ctx, userID, now, configured, quality)
	if err != nil {
		return err
	}
	snapshot, err := auditrisk.Evaluate(features, quality, policy, auditrisk.Versions{Model: "account-risk-v1", Baseline: fmt.Sprintf("histogram-v1:%d:%d", policy.ActivitySources.Start, policy.ActivitySources.Full), Source: sourceVersion}, now)
	if err != nil {
		return err
	}
	if !s.auditSettingsState(ctx).Enabled {
		return nil
	}
	if err = s.store.SaveAccountAuditSnapshot(ctx, snapshot, revision); err != nil {
		return err
	}
	s.publishRealtime("audit")
	events, err := s.store.ListAccountAuditEvents(ctx, store.AccountAuditQuery{UserID: userID, Limit: 100})
	if err != nil {
		return err
	}
	for _, event := range events.Items.([]store.AccountAuditEvent) {
		if event.Status != "recovered" {
			return nil
		}
	}
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if user.Status != "active" || user.SubscriptionSuspended {
		return nil
	}
	return s.store.RecordAccountActivityBaseline(ctx, snapshot, now)
}

func (s *Server) StartAuditRiskWorker(ctx context.Context) {
	// The durable queue is the sole scheduling source; diagnostics do not score.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var minute int64
	var after int64
	var expiryOffset int
	var subscriptionMinute int64
	var subscriptionEnabled bool
	var maintenanceMinute int64
	for {
		now := time.Now().UTC()
		state := s.auditSettingsState(ctx)
		enabled := state.Enabled && state.Subscription
		if current := now.Truncate(time.Minute).Unix(); subscriptionMinute != current || subscriptionEnabled != enabled {
			if err := s.store.SetAccountSubscriptionCoverage(ctx, state.Enabled && state.Subscription, now); err != nil {
				logConfigurationError("subscription activity coverage", err)
			} else {
				subscriptionMinute = current
				subscriptionEnabled = enabled
			}
		}
		if state.Enabled {
			current := now.Truncate(time.Minute).Unix()
			if minute != current {
				if err := s.recordAccountActivityExpectations(ctx, now); err != nil {
					logConfigurationError("account activity expected coverage", err)
				} else {
					minute = current
				}
			}
			if _, err := s.store.ApplyPendingAccountActivity(ctx, 64, now); err != nil {
				logConfigurationError("account activity apply", err)
			}
			if err := s.queueAccountActivityDirty(ctx); err != nil {
				logConfigurationError("account activity dirty", err)
			}
			var err error
			expiryOffset, err = s.queueAccountActivityExpiry(ctx, now, expiryOffset)
			if err != nil {
				logConfigurationError("account audit window advance", err)
			}
			after, err = s.evaluateAccountAuditWork(ctx, now, after)
			if err != nil {
				logConfigurationError("account audit evaluate", err)
			}
			if err := s.deliverAccountAuditNotifications(ctx, now); err != nil {
				logConfigurationError("account audit notifications", err)
			}
		} else {
			minute = 0
			after = 0
		}
		if err := s.store.CleanupAccountActivityPipeline(ctx, now); err != nil {
			logConfigurationError("account activity retention", err)
		}
		if current := now.Truncate(time.Minute).Unix(); maintenanceMinute != current {
			if _, err := s.store.CleanupAccountAuditNotifications(ctx, now.Add(-7*24*time.Hour), 100); err != nil {
				logConfigurationError("account audit outbox retention", err)
			}
			if err := s.store.CleanupAccountAuditHistory(ctx, now.Add(-30*24*time.Hour), 100); err != nil {
				logConfigurationError("account audit history retention", err)
			}
			maintenanceMinute = current
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
