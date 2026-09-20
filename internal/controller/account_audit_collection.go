package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func auditCollectionPrincipal(p application.Principal) error {
	if p.Role != model.RoleAdmin || !p.AllowsInt64("user_ids", -1) || !p.AllowsInt64("server_ids", -1) {
		return errors.New("unrestricted administrator required for global audit collection")
	}
	return nil
}
func (s *Server) queryAuditCollection(ctx context.Context, p application.Principal) (any, error) {
	if err := auditCollectionPrincipal(p); err != nil {
		return nil, err
	}
	return s.store.AuditCollection(ctx)
}
func (s *Server) registerAuditCollectionOperations() {
	candidate := func(ctx context.Context, p application.Principal, input json.RawMessage) (model.AuditCollectionConfig, error) {
		var c model.AuditCollectionConfig
		if err := auditCollectionPrincipal(p); err != nil {
			return c, err
		}
		if err := strictAutomationInput(input, &c); err != nil {
			return c, err
		}
		if err := store.ValidateAuditCollection(c, time.Now()); err != nil {
			return c, err
		}
		current, err := s.store.AuditCollection(ctx)
		if err != nil {
			return c, err
		}
		if current.Revision != c.Revision {
			return c, errors.New("collection revision conflict")
		}
		return c, nil
	}
	s.automation.RegisterValidator("audit.collection.update", func(ctx context.Context, p application.Principal, input json.RawMessage) (any, error) {
		return candidate(ctx, p, input)
	})
	s.automation.RegisterRevisionResolver("audit.collection.update", func(ctx context.Context, p application.Principal, input json.RawMessage) (map[string]string, error) {
		c, err := candidate(ctx, p, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"audit_collection": strconv.FormatInt(c.Revision, 10)}, nil
	})
	s.automation.Register("audit.collection.update", func(ctx context.Context, p application.Principal, input json.RawMessage) (any, error) {
		c, err := candidate(ctx, p, input)
		if err != nil {
			return nil, err
		}
		updated, err := s.store.SetAuditCollection(ctx, c, time.Now())
		if err == nil {
			s.publishRealtime("audit", "collection")
		}
		return updated, err
	})
}
func (s *Server) accountAuditCollectionUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "use a validated changeset", http.StatusMethodNotAllowed)
		return
	}
	c, err := s.store.AuditCollection(r.Context())
	if err != nil {
		http.Error(w, "collection unavailable", 500)
		return
	}
	writeJSON(w, 200, c)
}
func (s *Server) apiV1AuditCollection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		v2Error(w, r, 405, "method_not_allowed", "写入须通过 Changeset")
		return
	}
	p, _ := apiPrincipal(r)
	if _, ok := s.capabilities.Authorize(p, "audit.collection.get"); !ok {
		v2Error(w, r, 403, "capability_denied", "需要管理员权限")
		return
	}
	c, err := s.queryAuditCollection(r.Context(), p)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, 200, c, nil)
}
func (s *Server) effectiveAuditCollection(ctx context.Context, serverID int64) model.AuditCollectionEffective {
	c, err := s.store.AuditCollection(ctx)
	if err != nil {
		c = model.AuditCollectionConfig{Mode: "light"}
	}
	return c.Effective(serverID, time.Now())
}
