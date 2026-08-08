package controller

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) assignableNodeMetadataUpdate(w http.ResponseWriter, r *http.Request, rawType, rawID string) {
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	nodeType := model.AssignableNodeType(rawType)
	nodeID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || nodeID <= 0 {
		fail(w, errors.New("invalid node id"), 400)
		return
	}
	var req struct {
		DisplayNameOverride json.RawMessage `json:"display_name_override"`
		ExpectedLockVersion int64           `json:"expected_lock_version"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.DisplayNameOverride == nil {
		fail(w, errors.New("display_name_override is required"), 400)
		return
	}
	var override *string
	if string(req.DisplayNameOverride) != "null" {
		var value string
		if err := json.Unmarshal(req.DisplayNameOverride, &value); err != nil {
			fail(w, errors.New("display_name_override must be a string or null"), 400)
			return
		}
		value = strings.TrimSpace(value)
		if value != "" {
			if len([]rune(value)) > 100 {
				fail(w, errors.New("display_name_override is too long"), 400)
				return
			}
			override = &value
		}
	}
	data, err := s.loadPlanAssignmentData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	catalog, err := core.BuildAssignableNodeCatalog(core.AssignableNodeCatalogInput{
		Servers: data.config.Servers, Inbounds: data.config.Inbounds, ProxyPaths: data.config.ProxyPaths,
		ProxyPathSteps: data.config.ProxyPathSteps, EgressResults: data.config.ProxyPathEgressResults,
		ExternalOutbounds: data.config.ExternalOutbounds, ServerOnline: data.serverOnline, NodeMetadata: data.nodeMetadata,
	})
	if err != nil {
		fail(w, err, 500)
		return
	}
	key := core.NodeKeyOf(nodeType, nodeID)
	var oldOverride *string
	if current, ok := data.nodeMetadata[key]; ok && current.DisplayNameOverride != nil {
		value := *current.DisplayNameOverride
		oldOverride = &value
	}
	var sourceName string
	for _, node := range catalog {
		if node.Key == key {
			sourceName = node.SourceName
			break
		}
	}
	if sourceName == "" {
		fail(w, sql.ErrNoRows, 404)
		return
	}
	var actorID *int64
	if user, ok := r.Context().Value(userKey).(*model.User); ok && user != nil {
		actorID = &user.ID
	}
	metadata, err := s.store.UpsertNodeMetadata(r.Context(), nodeType, nodeID, override, req.ExpectedLockVersion, actorID)
	if err != nil {
		if errors.Is(err, store.ErrNodeMetadataConflict) {
			fail(w, err, http.StatusConflict)
			return
		}
		fail(w, err, 500)
		return
	}
	inherited, overridden, err := s.store.CountCurrentPlanNameStates(r.Context(), nodeType, nodeID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditDetail, _ := json.Marshal(map[string]any{
		"node_key":            key,
		"old_global_override": oldOverride,
		"new_global_override": override,
	})
	auditReq(s, r, "node_display_name.update", "assignable-node", string(auditDetail))
	write(w, 200, map[string]any{
		"node_type": nodeType, "node_id": nodeID, "source_name": sourceName,
		"global_name_override":          metadata.DisplayNameOverride,
		"effective_global_name":         core.ResolveEffectiveNodeName(sourceName, metadata.DisplayNameOverride, nil),
		"lock_version":                  metadata.LockVersion,
		"affected_current_plan_count":   inherited,
		"overridden_current_plan_count": overridden,
	})
}
