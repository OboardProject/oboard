package controller

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) selfUserDevices(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	parts := pathParts(r.URL.Path, "/api/v1/me/devices/")
	if r.URL.Path == "/api/v1/me/devices" {
		parts = nil
	}
	s.userDevices(w, r, user.ID, parts)
}

func (s *Server) userDevices(w http.ResponseWriter, r *http.Request, userID int64, parts []string) {
	if userID <= 0 {
		fail(w, errors.New("invalid user"), http.StatusBadRequest)
		return
	}
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			devices, err := s.store.ListUserDevices(r.Context(), userID)
			if err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			write(w, http.StatusOK, map[string]any{"devices": devices, "device_limit": user.DeviceLimit, "legacy_proxy_enabled": user.LegacyProxyEnabled})
		case http.MethodPost:
			fail(w, errors.New("device-specific subscriptions are no longer supported"), http.StatusGone)
		default:
			method(w)
		}
		return
	}
	deviceID := strings.TrimSpace(parts[0])
	if deviceID == "" || len(parts) > 2 {
		fail(w, errors.New("invalid device route"), http.StatusNotFound)
		return
	}
	if len(parts) == 2 {
		s.userDeviceAction(w, r, userID, deviceID, parts[1])
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var request struct {
			Name string `json:"name"`
		}
		if !decode(w, r, &request) {
			return
		}
		device, err := s.store.RenameUserDevice(r.Context(), userID, deviceID, request.Name)
		if err != nil {
			s.writeUserDeviceError(w, err)
			return
		}
		auditReq(s, r, "update", "user-device", fmt.Sprintf("%d:%s", userID, deviceID))
		write(w, http.StatusOK, map[string]any{"device": device})
	case http.MethodDelete:
		device, err := s.store.RevokeUserDevice(r.Context(), userID, deviceID)
		if err != nil {
			s.writeUserDeviceError(w, err)
			return
		}
		if err := s.queueUserDeviceCredentialDeployment(r.Context(), userID); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		auditReq(s, r, "revoke", "user-device", fmt.Sprintf("%d:%s", userID, deviceID))
		write(w, http.StatusOK, map[string]any{"device": device})
	default:
		method(w)
	}
}

func (s *Server) userDeviceAction(w http.ResponseWriter, r *http.Request, userID int64, deviceID, action string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	switch action {
	case "rotate", "suspend-subscription", "resume-subscription":
		fail(w, errors.New("device-specific subscriptions are no longer supported"), http.StatusGone)
	default:
		fail(w, errors.New("unsupported device action"), http.StatusNotFound)
	}
}

func (s *Server) queueUserDeviceCredentialDeployment(ctx context.Context, userID int64) error {
	s.applyChangePlan(ctx, userID, ClassifyCredentialRotation())
	return nil
}

func (s *Server) writeUserDeviceError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, sql.ErrNoRows):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrDeviceLimitReached):
		status = http.StatusConflict
	case strings.Contains(strings.ToLower(err.Error()), "device name"), strings.Contains(strings.ToLower(err.Error()), "invalid user device"):
		status = http.StatusBadRequest
	}
	fail(w, err, status)
}
