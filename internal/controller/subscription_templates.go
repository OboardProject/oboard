package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) subscriptionTemplates(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/subscription-templates")
	path = strings.Trim(path, "/")
	if path == "" {
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		items, err := s.store.ListSubscriptionClientTemplates(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"subscription_templates": items})
		return
	}
	parts := strings.Split(path, "/")
	format := core.NormalizeSubscriptionFormatForAPI(model.SubscriptionFormat(parts[0]))
	if !core.IsConcreteSubscriptionFormat(format) {
		fail(w, fmtSubscriptionTemplateFormatError(parts[0]), 400)
		return
	}
	if len(parts) == 1 {
		s.subscriptionTemplateItem(w, r, format)
		return
	}
	switch parts[1] {
	case "validate":
		s.subscriptionTemplateValidate(w, r, format)
	case "preview":
		s.subscriptionTemplatePreview(w, r, format)
	case "reset":
		s.subscriptionTemplateReset(w, r, format)
	default:
		fail(w, errors.New("not found"), 404)
	}
}

func (s *Server) subscriptionTemplateItem(w http.ResponseWriter, r *http.Request, format model.SubscriptionFormat) {
	switch r.Method {
	case http.MethodGet:
		item, err := s.store.GetSubscriptionClientTemplate(r.Context(), format)
		if err != nil {
			fail(w, err, 404)
			return
		}
		write(w, 200, map[string]any{"subscription_template": item})
	case http.MethodPut:
		var request struct {
			Content           string `json:"content"`
			ExpectedRevision  int64  `json:"expected_revision"`
		}
		if !decode(w, r, &request) {
			return
		}
		actorID := int64(0)
		if actor := currentUser(r); actor != nil {
			actorID = actor.ID
		}
		item, err := s.store.PutSubscriptionClientTemplate(r.Context(), format, request.Content, request.ExpectedRevision, actorID)
		if err != nil {
			if errors.Is(err, store.ErrSubscriptionTemplateConflict) {
				fail(w, err, http.StatusConflict)
				return
			}
			fail(w, err, 400)
			return
		}
		auditReq(s, r, "update", "subscription_template", string(format))
		write(w, 200, map[string]any{"subscription_template": item})
	default:
		method(w)
	}
}

func (s *Server) subscriptionTemplateValidate(w http.ResponseWriter, r *http.Request, format model.SubscriptionFormat) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	content, ok := decodeSubscriptionTemplateContent(w, r)
	if !ok {
		return
	}
	if err := core.ValidateSubscriptionTemplateWithPreview(format, content); err != nil {
		fail(w, err, 400)
		return
	}
	write(w, 200, map[string]any{"valid": true})
}

func (s *Server) subscriptionTemplatePreview(w http.ResponseWriter, r *http.Request, format model.SubscriptionFormat) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	content, ok := decodeSubscriptionTemplateContent(w, r)
	if !ok {
		return
	}
	rendered, err := core.RenderSubscriptionTemplatePreview(format, content)
	if err != nil {
		fail(w, err, 400)
		return
	}
	write(w, 200, map[string]any{"content": rendered, "format": format})
}

func (s *Server) subscriptionTemplateReset(w http.ResponseWriter, r *http.Request, format model.SubscriptionFormat) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	item, err := s.store.ResetSubscriptionClientTemplate(r.Context(), format)
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "reset", "subscription_template", string(format))
	write(w, 200, map[string]any{"subscription_template": item})
}

func decodeSubscriptionTemplateContent(w http.ResponseWriter, r *http.Request) (string, bool) {
	var request struct {
		Content string `json:"content"`
	}
	if !decode(w, r, &request) {
		return "", false
	}
	return request.Content, true
}

func fmtSubscriptionTemplateFormatError(raw string) error {
	return errors.New("unsupported subscription format " + jsonQuote(raw))
}

func jsonQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
