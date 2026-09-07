package controller

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/OboardProject/oboard/internal/security"
)

func (s *Server) rotateUserProxyCredentials(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if id <= 0 {
		fail(w, errors.New("missing id"), http.StatusBadRequest)
		return
	}
	current, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	proxyUUID, err := security.RandomUUID()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	proxyPassword, err := security.RandomToken(18)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if err := s.store.RotateUserProxyIdentity(r.Context(), id, proxyUUID, proxyPassword); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	updated, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	s.syncUserChange(r.Context(), *current, *updated)
	auditReq(s, r, "rotate", "user-credentials", fmt.Sprint(id))
	write(w, http.StatusOK, map[string]any{"rotated": true, "user_id": id})
}
