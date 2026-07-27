package controller

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

const googleEABHMACKeyPurpose = "google-eab-hmac-key"

type googleEABCredentialRequest struct {
	KeyID   string `json:"key_id"`
	HMACKey string `json:"hmac_key"`
	Remark  string `json:"remark"`
}

func (s *Server) googleEABCredentials(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/google-eab-credentials"), "/")
	id := int64(0)
	if rest != "" {
		if strings.Contains(rest, "/") {
			fail(w, errors.New("unknown Google EAB route"), http.StatusNotFound)
			return
		}
		parsed, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || parsed <= 0 {
			fail(w, errors.New("invalid Google EAB id"), http.StatusBadRequest)
			return
		}
		id = parsed
	}

	switch r.Method {
	case http.MethodGet:
		if id > 0 {
			credential, err := s.store.GetGoogleEABCredential(r.Context(), id)
			if err != nil {
				fail(w, errors.New("Google EAB 不存在"), http.StatusNotFound)
				return
			}
			write(w, http.StatusOK, map[string]any{"google_eab_credential": credential})
			return
		}
		credentials, err := s.store.ListGoogleEABCredentials(r.Context())
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		write(w, http.StatusOK, map[string]any{"google_eab_credentials": credentials})
	case http.MethodPost:
		if id > 0 {
			method(w)
			return
		}
		var req googleEABCredentialRequest
		if !decode(w, r, &req) {
			return
		}
		req.KeyID = strings.TrimSpace(req.KeyID)
		req.HMACKey = strings.TrimSpace(req.HMACKey)
		req.Remark = strings.TrimSpace(req.Remark)
		if err := validateCertificateEABValue("Key ID", req.KeyID, 512); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
		if err := validateCertificateEABValue("HMAC Key", req.HMACKey, 2048); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
		if len(req.Remark) > 120 {
			fail(w, errors.New("备注不能超过 120 个字符"), http.StatusBadRequest)
			return
		}
		encrypted, err := security.EncryptSecret(s.sessionSecret, googleEABHMACKeyPurpose, req.HMACKey)
		if err != nil {
			fail(w, fmt.Errorf("保存 Google EAB: %w", err), http.StatusInternalServerError)
			return
		}
		credential := model.GoogleEABCredential{KeyID: req.KeyID, Remark: req.Remark, HMACKeyEncrypted: encrypted}
		if err := s.store.CreateGoogleEABCredential(r.Context(), &credential); err != nil {
			if errors.Is(err, store.ErrGoogleEABCredentialExists) {
				fail(w, err, http.StatusConflict)
			} else {
				fail(w, err, http.StatusInternalServerError)
			}
			return
		}
		auditReq(s, r, "create", "google_eab_credential", strconv.FormatInt(credential.ID, 10))
		write(w, http.StatusCreated, map[string]any{"google_eab_credential": credential})
	case http.MethodDelete:
		if id == 0 {
			fail(w, errors.New("missing Google EAB id"), http.StatusBadRequest)
			return
		}
		if err := s.store.DeleteGoogleEABCredential(r.Context(), id); err != nil {
			switch {
			case errors.Is(err, store.ErrGoogleEABCredentialInUse):
				fail(w, errors.New("此 EAB 正在被证书使用，请先为相关证书更换 EAB"), http.StatusConflict)
			case errors.Is(err, sql.ErrNoRows):
				fail(w, errors.New("Google EAB 不存在"), http.StatusNotFound)
			default:
				fail(w, err, http.StatusInternalServerError)
			}
			return
		}
		auditReq(s, r, "delete", "google_eab_credential", strconv.FormatInt(id, 10))
		write(w, http.StatusOK, map[string]any{"deleted": true})
	default:
		method(w)
	}
}
