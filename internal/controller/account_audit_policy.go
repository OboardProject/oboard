package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) queryAccountAuditPolicy(ctx context.Context, p application.Principal) (any, error) {
	if err := auditCollectionPrincipal(p); err != nil {
		return nil, err
	}
	c, err := s.store.GetAccountAuditPolicy(ctx)
	if err != nil {
		return nil, err
	}
	_, source := configuredAccountSourcePolicy(s.sessionSecret, c.SourceGrouping)
	return map[string]any{"revision": c.Revision, "policy": c.Policy, "resources": c.Resources, "source_grouping": c.SourceGrouping, "source": map[string]any{"version": source.Version, "epoch": c.SourceGrouping.Epoch, "ipv4_prefix_bits": source.IPv4Bits, "ipv6_prefix_bits": source.IPv6Bits, "rotation_supported": true, "rotation_unknown_days": 7}, "resource_measurement": "coverage_required"}, nil
}
func (s *Server) registerAccountAuditPolicyOperations() {
	candidate := func(ctx context.Context, p application.Principal, input json.RawMessage) (store.AccountAuditPolicy, error) {
		var c store.AccountAuditPolicy
		if err := auditCollectionPrincipal(p); err != nil {
			return c, err
		}
		if err := strictAutomationInput(input, &c); err != nil {
			return c, err
		}
		if err := store.ValidateAccountAuditPolicy(c); err != nil {
			return c, err
		}
		current, err := s.store.GetAccountAuditPolicy(ctx)
		if err != nil {
			return c, err
		}
		if (c.SourceGrouping.IPv4Bits != current.SourceGrouping.IPv4Bits || c.SourceGrouping.IPv6Bits != current.SourceGrouping.IPv6Bits) && c.SourceGrouping.Epoch <= current.SourceGrouping.Epoch {
			return c, errors.New("changing source prefix lengths requires a new epoch")
		}
		if c.SourceGrouping.Epoch < current.SourceGrouping.Epoch {
			return c, errors.New("source grouping epoch cannot decrease")
		}
		if current.Revision != c.Revision {
			return c, errors.New("account policy revision conflict")
		}
		return c, nil
	}
	s.automation.RegisterValidator("audit.policy.update", func(ctx context.Context, p application.Principal, input json.RawMessage) (any, error) {
		return candidate(ctx, p, input)
	})
	s.automation.RegisterRevisionResolver("audit.policy.update", func(ctx context.Context, p application.Principal, input json.RawMessage) (map[string]string, error) {
		c, err := candidate(ctx, p, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"account_audit_policy": strconv.FormatInt(c.Revision, 10)}, nil
	})
	s.automation.Register("audit.policy.update", func(ctx context.Context, p application.Principal, input json.RawMessage) (any, error) {
		c, err := candidate(ctx, p, input)
		if err != nil {
			return nil, err
		}
		updated, err := s.store.SetAccountAuditPolicy(ctx, c, time.Now())
		if err == nil {
			s.publishRealtime("audit", "policy")
		}
		return updated, err
	})
}
func (s *Server) accountAuditPolicyUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "use a validated changeset", http.StatusMethodNotAllowed)
		return
	}
	p := application.Principal{Role: "admin"}
	c, err := s.queryAccountAuditPolicy(r.Context(), p)
	if err != nil {
		http.Error(w, "collection unavailable", 500)
		return
	}
	writeJSON(w, 200, c)
}
func (s *Server) apiV1AccountAuditPolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		v2Error(w, r, 405, "method_not_allowed", "写入须通过 Changeset")
		return
	}
	p, _ := apiPrincipal(r)
	if _, ok := s.capabilities.Authorize(p, "audit.policy.get"); !ok {
		v2Error(w, r, 403, "capability_denied", "需要管理员权限")
		return
	}
	c, err := s.queryAccountAuditPolicy(r.Context(), p)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, 200, c, nil)
}
