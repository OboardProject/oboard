package controller

import (
	"context"
	"fmt"

	"github.com/OboardProject/oboard/internal/auditactivity"
	"github.com/OboardProject/oboard/internal/store"
)

type accountAuditSourceSnapshot struct {
	revision int64
	key      []byte
	policy   auditactivity.SourcePolicy
}

func configuredAccountSourcePolicy(secret string, config store.AccountAuditSourceConfig) ([]byte, auditactivity.SourcePolicy) {
	key, policy := auditactivity.ControllerSourcePolicy(secret)
	policy.Version = fmt.Sprintf("prefix-v2:%d:%d:%d", config.IPv4Bits, config.IPv6Bits, config.Epoch)
	policy.IPv4Bits, policy.IPv6Bits = config.IPv4Bits, config.IPv6Bits
	return key, policy
}

func (s *Server) accountAuditSourcePolicy(ctx context.Context) ([]byte, auditactivity.SourcePolicy, error) {
	revision := s.store.SettingsRevision()
	if cached := s.accountSourceCache.Load(); cached != nil && cached.revision == revision {
		return cached.key, cached.policy, nil
	}
	config, err := s.store.GetAccountAuditPolicy(ctx)
	if err != nil {
		return nil, auditactivity.SourcePolicy{}, err
	}
	key, policy := configuredAccountSourcePolicy(s.sessionSecret, config.SourceGrouping)
	s.accountSourceCache.Store(&accountAuditSourceSnapshot{revision: revision, key: key, policy: policy})
	return key, policy, nil
}
