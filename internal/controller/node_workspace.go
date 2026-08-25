package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

const (
	nodeSourceTimeout       = 15 * time.Second
	nodeSourceMaxBody       = 1 << 20
	nodeSourceRefreshPeriod = 6 * time.Hour
)

type nodeLibraryView struct {
	ID       string                            `json:"id"`
	GroupID  int64                             `json:"group_id"`
	Name     string                            `json:"name"`
	Protocol model.PrivateSubscriptionProtocol `json:"protocol"`
	Source   string                            `json:"source"`
	Copyable bool                              `json:"copyable"`
	Editable bool                              `json:"editable"`
}

type privateImportNodeSummary struct {
	Name        string                            `json:"name"`
	Protocol    model.PrivateSubscriptionProtocol `json:"protocol"`
	Fingerprint string                            `json:"fingerprint"`
}

type privateImportSummary struct {
	NodeCount int                        `json:"node_count"`
	Nodes     []privateImportNodeSummary `json:"nodes"`
	Issues    []core.PrivateImportIssue  `json:"issues"`
}

type subscriptionPreviewNodeSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Group    string `json:"group"`
	Protocol string `json:"protocol"`
}

func subscriptionPreviewView(preview core.SubscriptionPreview, includeContent bool, filterStats ...core.SubscriptionFilterStats) map[string]any {
	nodes := make([]subscriptionPreviewNodeSummary, 0, len(preview.Nodes))
	for _, node := range preview.Nodes {
		nodes = append(nodes, subscriptionPreviewNodeSummary{ID: node.Key, Name: node.Name, Group: node.Group, Protocol: strings.ToLower(stringFromNodeType(node.Raw["type"]))})
	}
	view := map[string]any{"nodes": nodes, "filtered_count": preview.FilteredCount, "invalid_reasons": preview.InvalidReasons}
	if len(filterStats) > 0 && len(filterStats[0].Rules) > 0 {
		view["filter_stats"] = filterStats[0].Rules
		view["filter_dropped"] = filterStats[0].TotalDropped
	}
	if includeContent {
		view["content"] = preview.Content
	}
	return view
}

func summarizePrivateImport(result *core.PrivateImportResult) privateImportSummary {
	summary := privateImportSummary{Nodes: []privateImportNodeSummary{}, Issues: []core.PrivateImportIssue{}}
	if result == nil {
		return summary
	}
	summary.NodeCount = len(result.Nodes)
	summary.Issues = result.Issues
	for _, node := range result.Nodes {
		summary.Nodes = append(summary.Nodes, privateImportNodeSummary{Name: node.Name, Protocol: node.Protocol, Fingerprint: node.Fingerprint})
	}
	return summary
}

func (s *Server) nodeWorkspaceSubject(r *http.Request) (*model.User, error) {
	actor := currentUser(r)
	if actor == nil || actor.Status != "active" {
		return nil, errors.New("invalid session")
	}
	targetID := actor.ID
	if raw := strings.TrimSpace(r.URL.Query().Get("user_id")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			return nil, errors.New("invalid user_id")
		}
		if parsed != actor.ID && !roleAllows(currentRole(r), model.RoleAdmin) {
			return nil, errors.New("forbidden")
		}
		targetID = parsed
	}
	target, err := s.store.GetUser(r.Context(), targetID)
	if err != nil {
		return nil, err
	}
	if target.Status != "active" {
		return nil, errors.New("target user is not active")
	}
	return target, nil
}

func (s *Server) nodeWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	user, err := s.nodeWorkspaceSubject(r)
	if err != nil {
		nodeWorkspaceFail(w, err)
		return
	}
	_, groups, err := s.workspaceSubscriptionNodes(r.Context(), *user, nil)
	if err != nil {
		fail(w, err, 500)
		return
	}
	sources, err := s.store.ListNodeSources(r.Context(), user.ID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	for i := range sources {
		sources[i].URLDisplay = s.nodeSourceDisplay(sources[i])
		sources[i].URLEncrypted = ""
		sources[i].URLFingerprint = ""
		sources[i].ETag = ""
		sources[i].LastModified = ""
	}
	outputs, err := s.store.ListSubscriptionOutputs(r.Context(), user.ID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, http.StatusOK, map[string]any{"subject": map[string]any{"id": user.ID, "username": user.Username, "nickname": user.Nickname}, "node_groups": groups, "node_sources": sources, "subscription_outputs": outputs})
}

func (s *Server) nodeGroups(w http.ResponseWriter, r *http.Request) {
	user, err := s.nodeWorkspaceSubject(r)
	if err != nil {
		nodeWorkspaceFail(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		_, items, listErr := s.workspaceSubscriptionNodes(r.Context(), *user, nil)
		if listErr != nil {
			fail(w, listErr, 500)
			return
		}
		write(w, 200, map[string]any{"node_groups": items})
	case http.MethodPost:
		var request struct {
			Name    string              `json:"name"`
			Kind    model.NodeGroupKind `json:"kind"`
			URL     string              `json:"url"`
			Content string              `json:"content"`
		}
		if !decode(w, r, &request) {
			return
		}
		group := &model.NodeGroup{UserID: user.ID, Name: request.Name, Kind: request.Kind}
		if err := s.store.CreateNodeGroup(r.Context(), group); err != nil {
			fail(w, err, 400)
			return
		}
		response := map[string]any{"node_group": group}
		if request.Kind == model.NodeGroupRemote {
			source, sourceErr := s.createNodeSource(r.Context(), user.ID, group.ID, request.URL)
			if sourceErr != nil {
				_ = s.store.DeleteNodeGroup(r.Context(), user.ID, group.ID)
				fail(w, sourceErr, 400)
				return
			}
			refresh, refreshErr := s.refreshNodeSource(r.Context(), *source)
			if refreshErr != nil {
				response["refresh_error"] = refreshErr.Error()
			}
			response["node_source"] = sanitizeNodeSource(*source, s.nodeSourceDisplay(*source))
			response["import"] = summarizePrivateImport(refresh)
		} else if strings.TrimSpace(request.Content) != "" {
			result, importErr := s.importManualNodes(r.Context(), user.ID, group.ID, request.Content)
			if importErr != nil {
				_ = s.store.DeleteNodeGroup(r.Context(), user.ID, group.ID)
				fail(w, importErr, 400)
				return
			}
			response["import"] = summarizePrivateImport(result)
		}
		s.publishRealtime("nodes", "subscriptions")
		write(w, http.StatusCreated, response)
	default:
		method(w)
	}
}

func (s *Server) nodeGroup(w http.ResponseWriter, r *http.Request) {
	user, err := s.nodeWorkspaceSubject(r)
	if err != nil {
		nodeWorkspaceFail(w, err)
		return
	}
	parts := pathParts(r.URL.Path, "/api/v1/node-groups/")
	id, err := nodeWorkspaceID(parts)
	if err != nil {
		fail(w, err, 400)
		return
	}
	if len(parts) == 2 && parts[1] == "import" && r.Method == http.MethodPost {
		var request struct {
			Content string `json:"content"`
		}
		if !decode(w, r, &request) {
			return
		}
		result, importErr := s.importManualNodes(r.Context(), user.ID, id, request.Content)
		if importErr != nil {
			fail(w, importErr, 400)
			return
		}
		s.publishRealtime("nodes", "subscriptions")
		write(w, 200, map[string]any{"import": summarizePrivateImport(result)})
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var request struct {
			Name    string `json:"name"`
			URL     string `json:"url"`
			Content string `json:"content"`
		}
		if !decode(w, r, &request) {
			return
		}
		response, updateErr := s.updateNodeGroup(r.Context(), user.ID, id, request.Name, request.URL, request.Content)
		if updateErr != nil {
			nodeWorkspaceFail(w, updateErr)
			return
		}
		write(w, 200, response)
	case http.MethodDelete:
		if err := s.store.DeleteNodeGroup(r.Context(), user.ID, id); err != nil {
			fail(w, err, 400)
			return
		}
		s.publishRealtime("nodes", "subscriptions")
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) nodeSource(w http.ResponseWriter, r *http.Request) {
	user, err := s.nodeWorkspaceSubject(r)
	if err != nil {
		nodeWorkspaceFail(w, err)
		return
	}
	parts := pathParts(r.URL.Path, "/api/v1/node-sources/")
	id, err := nodeWorkspaceID(parts)
	if err != nil || len(parts) != 2 || parts[1] != "refresh" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	source, err := s.store.GetNodeSource(r.Context(), user.ID, id)
	if err != nil {
		nodeWorkspaceFail(w, err)
		return
	}
	result, err := s.refreshNodeSource(r.Context(), *source)
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	s.publishRealtime("nodes", "subscriptions")
	write(w, 200, map[string]any{"import": summarizePrivateImport(result)})
}

func (s *Server) nodeImportPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if _, err := s.nodeWorkspaceSubject(r); err != nil {
		nodeWorkspaceFail(w, err)
		return
	}
	var request struct {
		Content string `json:"content"`
		URL     string `json:"url"`
	}
	if !decode(w, r, &request) {
		return
	}
	var result *core.PrivateImportResult
	var err error
	if strings.TrimSpace(request.URL) != "" {
		result, err = s.previewRemoteNodeSource(r.Context(), request.URL)
	} else {
		parsed, parseErr := core.ParsePrivateSubscription(request.Content)
		result, err = &parsed, parseErr
	}
	if err != nil {
		fail(w, err, 400)
		return
	}
	write(w, 200, map[string]any{"nodes": privateImportPreviewNodes(result.Nodes), "issues": result.Issues})
}

func (s *Server) nodeLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	user, err := s.nodeWorkspaceSubject(r)
	if err != nil {
		nodeWorkspaceFail(w, err)
		return
	}
	nodes, groups, err := s.workspaceAllNodes(r.Context(), *user)
	if err != nil {
		fail(w, err, 500)
		return
	}
	views := make([]nodeLibraryView, 0, len(nodes))
	for _, node := range nodes {
		protocol := model.PrivateSubscriptionProtocol(strings.ToLower(fmt.Sprint(node.Raw["type"])))
		groupID := groupIDForNode(node, groups)
		_, shareErr := core.CanonicalShareURIForNode(node)
		views = append(views, nodeLibraryView{ID: node.Key, GroupID: groupID, Name: node.Name, Protocol: protocol, Source: nodeSourceKind(node), Copyable: shareErr == nil, Editable: nodeLibraryEditable(node, groupID, groups)})
	}
	write(w, 200, map[string]any{"nodes": views, "node_groups": groups})
}

func (s *Server) nodeLibraryItem(w http.ResponseWriter, r *http.Request) {
	user, err := s.nodeWorkspaceSubject(r)
	if err != nil {
		nodeWorkspaceFail(w, err)
		return
	}
	parts := pathParts(r.URL.Path, "/api/v1/node-library/")
	if len(parts) == 1 && r.Method == http.MethodPatch {
		var request struct {
			Name    string `json:"name"`
			Content string `json:"content"`
			Enabled *bool  `json:"enabled"`
		}
		if !decode(w, r, &request) {
			return
		}
		updated, updateErr := s.updateImportedNode(r.Context(), user.ID, parts[0], request.Name, request.Content, request.Enabled)
		if updateErr != nil {
			nodeWorkspaceFail(w, updateErr)
			return
		}
		s.publishRealtime("nodes", "subscriptions")
		write(w, 200, map[string]any{"node": updated})
		return
	}
	if r.Method != http.MethodPost || !((len(parts) == 1 && parts[0] == "share") || (len(parts) == 2 && parts[1] == "share")) {
		http.NotFound(w, r)
		return
	}
	var request struct {
		NodeID   string `json:"node_id"`
		DeviceID string `json:"device_id"`
	}
	if !decode(w, r, &request) {
		return
	}
	subscriptionUser := *user
	if request.DeviceID != "" {
		device, deviceErr := s.store.GetUserDevice(r.Context(), user.ID, request.DeviceID)
		if deviceErr != nil || device.Status != "active" {
			nodeWorkspaceFail(w, sql.ErrNoRows)
			return
		}
		subscriptionUser = core.UserForDevice(*user, *device)
	}
	nodes, _, err := s.workspaceAllNodes(r.Context(), subscriptionUser)
	if err != nil {
		fail(w, err, 500)
		return
	}
	for _, node := range nodes {
		if node.Key != request.NodeID {
			continue
		}
		uri, shareErr := core.CanonicalShareURIForNode(node)
		if shareErr != nil {
			fail(w, fmt.Errorf("该节点无法无损转换为分享链接: %w", shareErr), 422)
			return
		}
		write(w, 200, map[string]any{"url": uri})
		return
	}
	nodeWorkspaceFail(w, sql.ErrNoRows)
}

func (s *Server) subscriptionOutputs(w http.ResponseWriter, r *http.Request) {
	user, err := s.nodeWorkspaceSubject(r)
	if err != nil {
		nodeWorkspaceFail(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, listErr := s.store.ListSubscriptionOutputs(r.Context(), user.ID)
		if listErr != nil {
			fail(w, listErr, 500)
			return
		}
		write(w, 200, map[string]any{"subscription_outputs": items})
	case http.MethodPost:
		var request struct {
			Name     string                            `json:"name"`
			GroupIDs []int64                           `json:"group_ids"`
			Filters  *[]model.SubscriptionOutputFilter `json:"filters"`
		}
		if !decode(w, r, &request) {
			return
		}
		filters, filterErr := s.normalizeSubscriptionOutputFilterRequest(r.Context(), user.ID, request.Filters)
		if filterErr != nil {
			nodeWorkspaceFail(w, filterErr)
			return
		}
		item := &model.SubscriptionOutput{UserID: user.ID, Name: request.Name, GroupIDs: request.GroupIDs, Filters: filters, Enabled: true}
		if err := s.store.SaveSubscriptionOutput(r.Context(), item); err != nil {
			fail(w, err, 400)
			return
		}
		write(w, 201, map[string]any{"subscription_output": item})
	default:
		method(w)
	}
}

func (s *Server) subscriptionOutput(w http.ResponseWriter, r *http.Request) {
	user, err := s.nodeWorkspaceSubject(r)
	if err != nil {
		nodeWorkspaceFail(w, err)
		return
	}
	parts := pathParts(r.URL.Path, "/api/v1/subscription-outputs/")
	id, err := nodeWorkspaceID(parts)
	if err != nil {
		fail(w, err, 400)
		return
	}
	if len(parts) == 2 && parts[1] == "preview" && r.Method == http.MethodPost {
		var request struct {
			Format model.SubscriptionFormat `json:"format"`
		}
		if !decode(w, r, &request) {
			return
		}
		if !core.IsSupportedSubscriptionFormat(core.NormalizeSubscriptionFormatForAPI(request.Format)) {
			fail(w, errors.New("unsupported subscription format"), 400)
			return
		}
		output, getErr := s.store.GetSubscriptionOutput(r.Context(), user.ID, id)
		if getErr != nil || !output.Enabled {
			nodeWorkspaceFail(w, sql.ErrNoRows)
			return
		}
		nodes, _, deduplicatedCount, filterStats, buildErr := s.workspaceSubscriptionNodesWithStats(r.Context(), *user, output)
		if buildErr != nil {
			fail(w, buildErr, 500)
			return
		}
		preview, previewErr := core.PreviewSubscriptionNodes(nodes, request.Format)
		if previewErr != nil {
			fail(w, previewErr, 500)
			return
		}
		write(w, 200, map[string]any{"profile_id": output.ID, "preview": subscriptionPreviewView(preview, true, filterStats), "deduplicated_count": deduplicatedCount})
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		current, getErr := s.store.GetSubscriptionOutput(r.Context(), user.ID, id)
		if getErr != nil {
			nodeWorkspaceFail(w, getErr)
			return
		}
		var request struct {
			Name     string                            `json:"name"`
			GroupIDs []int64                           `json:"group_ids"`
			Filters  *[]model.SubscriptionOutputFilter `json:"filters"`
			Enabled  *bool                             `json:"enabled"`
		}
		if !decode(w, r, &request) {
			return
		}
		current.Name, current.GroupIDs = request.Name, request.GroupIDs
		if request.Filters != nil {
			filters, filterErr := s.normalizeSubscriptionOutputFilterRequest(r.Context(), user.ID, request.Filters)
			if filterErr != nil {
				nodeWorkspaceFail(w, filterErr)
				return
			}
			current.Filters = filters
		}
		if request.Enabled != nil {
			current.Enabled = *request.Enabled
		}
		if err := s.store.SaveSubscriptionOutput(r.Context(), current); err != nil {
			fail(w, err, 400)
			return
		}
		write(w, 200, map[string]any{"subscription_output": current})
	case http.MethodDelete:
		if err := s.store.DeleteSubscriptionOutput(r.Context(), user.ID, id); err != nil {
			fail(w, err, 400)
			return
		}
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) normalizeSubscriptionOutputFilterRequest(ctx context.Context, userID int64, filters *[]model.SubscriptionOutputFilter) ([]model.SubscriptionOutputFilter, error) {
	if filters == nil {
		return nil, nil
	}
	groups, err := s.store.ListNodeGroups(ctx, userID)
	if err != nil {
		return nil, err
	}
	knownGroups := map[int64]bool{}
	for _, group := range groups {
		knownGroups[group.ID] = true
	}
	return core.NormalizeSubscriptionOutputFilters(*filters, knownGroups)
}

func (s *Server) importManualNodes(ctx context.Context, userID, groupID int64, content string) (*core.PrivateImportResult, error) {
	result, err := core.ParsePrivateSubscription(content)
	if err != nil {
		return nil, err
	}
	items, err := s.encryptImportedNodes(userID, groupID, nil, result.Nodes)
	if err != nil {
		return nil, err
	}
	if err := s.store.AddManualImportedNodes(ctx, userID, groupID, items); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Server) encryptImportedNodes(userID, groupID int64, sourceID *int64, nodes []core.ParsedPrivateNode) ([]model.ImportedNode, error) {
	items := make([]model.ImportedNode, 0, len(nodes))
	for _, node := range nodes {
		encoded, err := json.Marshal(node.Raw)
		if err != nil {
			return nil, err
		}
		wrapped, err := security.EncryptSecret(s.sessionSecret, privateNodePurpose(userID, node.Fingerprint), string(encoded))
		if err != nil {
			return nil, err
		}
		items = append(items, model.ImportedNode{UserID: userID, GroupID: groupID, SourceID: sourceID, Protocol: node.Protocol, Name: node.Name, Fingerprint: node.Fingerprint, ConfigEncrypted: wrapped, Enabled: true})
	}
	return items, nil
}

func (s *Server) sealNodeSourceURL(userID int64, rawURL string) (fingerprint, encrypted string, err error) {
	parsed, parseErr := validateNodeSourceURL(rawURL)
	if parseErr != nil {
		return "", "", parseErr
	}
	canonical := parsed.String()
	digest := sha256.Sum256([]byte(canonical))
	fingerprint = hex.EncodeToString(digest[:])
	encrypted, err = security.EncryptSecret(s.sessionSecret, nodeSourcePurpose(userID, fingerprint), canonical)
	if err != nil {
		return "", "", err
	}
	return fingerprint, encrypted, nil
}

func (s *Server) createNodeSource(ctx context.Context, userID, groupID int64, rawURL string) (*model.NodeSource, error) {
	fingerprint, encrypted, err := s.sealNodeSourceURL(userID, rawURL)
	if err != nil {
		return nil, err
	}
	source := &model.NodeSource{UserID: userID, GroupID: groupID, URLFingerprint: fingerprint, URLEncrypted: encrypted}
	if err := s.store.CreateNodeSource(ctx, source); err != nil {
		return nil, err
	}
	return source, nil
}

// updateNodeGroup applies one node group edit: rename, re-point a remote
// subscription URL with an immediate resync, or append manual node links.
// All validation happens before any write so a rejected edit changes nothing.
func (s *Server) updateNodeGroup(ctx context.Context, userID, groupID int64, name, rawURL, content string) (map[string]any, error) {
	group, err := s.store.GetNodeGroup(ctx, userID, groupID)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	rawURL = strings.TrimSpace(rawURL)
	content = strings.TrimSpace(content)
	if name == "" && rawURL == "" && content == "" {
		return nil, errors.New("没有需要更新的内容")
	}
	var fingerprint, encrypted string
	if rawURL != "" {
		if group.Kind != model.NodeGroupRemote {
			return nil, errors.New("仅远程节点组可以更新订阅 URL")
		}
		fingerprint, encrypted, err = s.sealNodeSourceURL(userID, rawURL)
		if err != nil {
			return nil, err
		}
	}
	if content != "" {
		if group.Kind != model.NodeGroupManual {
			return nil, errors.New("仅手动节点组可以导入节点链接")
		}
		if _, parseErr := core.ParsePrivateSubscription(content); parseErr != nil {
			return nil, parseErr
		}
	}
	if name != "" {
		group, err = s.store.RenameNodeGroup(ctx, userID, groupID, name)
		if err != nil {
			return nil, err
		}
	}
	response := map[string]any{"node_group": group}
	switch {
	case rawURL != "":
		source, sourceErr := s.store.GetNodeSourceByGroup(ctx, userID, groupID)
		if errors.Is(sourceErr, sql.ErrNoRows) {
			source = &model.NodeSource{UserID: userID, GroupID: groupID, URLFingerprint: fingerprint, URLEncrypted: encrypted}
			sourceErr = s.store.CreateNodeSource(ctx, source)
		} else if sourceErr == nil && source.URLFingerprint != fingerprint {
			_, sourceErr = s.store.UpdateNodeSourceURL(ctx, userID, source.ID, fingerprint, encrypted)
			if sourceErr == nil {
				source.URLFingerprint, source.URLEncrypted = fingerprint, encrypted
			}
		}
		if sourceErr != nil {
			return nil, sourceErr
		}
		response["node_source"] = sanitizeNodeSource(*source, s.nodeSourceDisplay(*source))
		result, refreshErr := s.refreshNodeSource(ctx, *source)
		if refreshErr != nil {
			response["refresh_error"] = refreshErr.Error()
		} else {
			response["import"] = summarizePrivateImport(result)
		}
	case content != "":
		result, importErr := s.importManualNodes(ctx, userID, groupID, content)
		if importErr != nil {
			return nil, importErr
		}
		response["import"] = summarizePrivateImport(result)
	}
	final, finalErr := s.store.GetNodeGroup(ctx, userID, groupID)
	if finalErr != nil {
		return nil, finalErr
	}
	response["node_group"] = final
	s.publishRealtime("nodes", "subscriptions")
	return response, nil
}

func (s *Server) refreshNodeSource(ctx context.Context, source model.NodeSource) (*core.PrivateImportResult, error) {
	if !s.beginNodeRefresh(source.UserID) {
		return nil, errors.New("该用户已有来源正在刷新")
	}
	defer s.endNodeRefresh(source.UserID)
	rawURL, err := security.DecryptSecret(s.sessionSecret, nodeSourcePurpose(source.UserID, source.URLFingerprint), source.URLEncrypted)
	if err != nil {
		return nil, errors.New("来源地址无法解密")
	}
	body, etag, lastModified, notModified, err := fetchNodeSource(ctx, rawURL, source.ETag, source.LastModified)
	if err != nil {
		_ = s.store.MarkNodeSourceFailed(ctx, source.UserID, source.ID, safeNodeSourceError(err), time.Now())
		return nil, err
	}
	if notModified {
		if err := s.store.MarkNodeSourceNotModified(ctx, source.UserID, source.ID, time.Now()); err != nil {
			return nil, err
		}
		return &core.PrivateImportResult{Nodes: []core.ParsedPrivateNode{}, Issues: []core.PrivateImportIssue{}}, nil
	}
	result, err := core.ParsePrivateSubscription(string(body))
	if err != nil {
		_ = s.store.MarkNodeSourceFailed(ctx, source.UserID, source.ID, safeNodeSourceError(err), time.Now())
		return nil, err
	}
	items, err := s.encryptImportedNodes(source.UserID, source.GroupID, &source.ID, result.Nodes)
	if err != nil {
		return nil, err
	}
	if err := s.store.ReplaceSourceNodes(ctx, source, items, etag, lastModified, time.Now()); err != nil {
		_ = s.store.MarkNodeSourceFailed(ctx, source.UserID, source.ID, safeNodeSourceError(err), time.Now())
		return nil, err
	}
	return &result, nil
}

func (s *Server) beginNodeRefresh(userID int64) bool {
	select {
	case s.nodeRefreshSem <- struct{}{}:
	default:
		return false
	}
	s.nodeRefreshMu.Lock()
	defer s.nodeRefreshMu.Unlock()
	if s.nodeRefreshUsers[userID] {
		<-s.nodeRefreshSem
		return false
	}
	s.nodeRefreshUsers[userID] = true
	return true
}

func (s *Server) endNodeRefresh(userID int64) {
	s.nodeRefreshMu.Lock()
	delete(s.nodeRefreshUsers, userID)
	s.nodeRefreshMu.Unlock()
	<-s.nodeRefreshSem
}

func (s *Server) scheduleNodeSourceRefreshes(ctx context.Context) {
	sources, err := s.store.ListNodeSourcesDueForRefresh(ctx, time.Now().Add(-nodeSourceRefreshPeriod), 100)
	if err != nil {
		s.logPeriodicError("node-source-refresh", "node source refresh scan failed: %v", err)
		return
	}
	for _, source := range sources {
		source := source
		go func() {
			refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeSourceTimeout)
			defer cancel()
			_, _ = s.refreshNodeSource(refreshCtx, source)
		}()
	}
}

func (s *Server) workspaceSubscriptionNodes(ctx context.Context, user model.User, output *model.SubscriptionOutput) ([]core.SubscriptionNode, []model.NodeGroup, error) {
	nodes, groups, _, _, err := s.workspaceSubscriptionNodesWithStats(ctx, user, output)
	return nodes, groups, err
}

func (s *Server) workspaceSubscriptionNodesWithStats(ctx context.Context, user model.User, output *model.SubscriptionOutput) ([]core.SubscriptionNode, []model.NodeGroup, int, core.SubscriptionFilterStats, error) {
	groups, err := s.store.ListNodeGroups(ctx, user.ID)
	if err != nil {
		return nil, nil, 0, core.SubscriptionFilterStats{}, err
	}
	if output == nil {
		output, err = s.store.GetDefaultSubscriptionOutput(ctx, user.ID)
		if err != nil {
			return nil, nil, 0, core.SubscriptionFilterStats{}, err
		}
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, nil, 0, core.SubscriptionFilterStats{}, err
	}
	snapshot, err := s.buildAccessSnapshot(ctx, data)
	if err != nil {
		return nil, nil, 0, core.SubscriptionFilterStats{}, err
	}
	sshServerHostKeys, err := s.subscriptionSSHServerHostKeys(ctx, user, data, snapshot.InboundUserBindings(), snapshot.ProxyPathUserBindings())
	if err != nil {
		return nil, nil, 0, core.SubscriptionFilterStats{}, err
	}
	orderPolicy, orderPositions, planNodeNames, err := s.store.GetEffectiveSubscriptionNodePresentation(ctx, user.ID, time.Now())
	if err != nil {
		return nil, nil, 0, core.SubscriptionFilterStats{}, err
	}
	metadata, err := s.store.ListNodeMetadata(ctx)
	if err != nil {
		return nil, nil, 0, core.SubscriptionFilterStats{}, err
	}
	globalNames := map[string]*string{}
	for key, item := range metadata {
		globalNames[key] = item.DisplayNameOverride
	}
	opts := core.SubscriptionOptions{Format: model.SubscriptionFormatSingBox, ProxyPaths: data.ProxyPaths, ProxyPathSteps: data.ProxyPathSteps, RoutingRules: data.RoutingRules, ProxyPathEgressResults: data.ProxyPathEgressResults, ExternalOutbounds: data.ExternalOutbounds, SSHServerHostKeys: sshServerHostKeys, EffectiveNodes: snapshot.EffectiveNodeKeys(user.ID), EffectiveNodeGroups: snapshot.EffectiveNodeGroups(user.ID), NodeOrderPositions: orderPositions, GlobalNodeNames: globalNames, PlanNodeNames: planNodeNames}
	if orderPolicy != nil {
		opts.NodeOrderPolicy = *orderPolicy
	}
	oboardNodes, err := core.BuildSubscriptionNodes(user, data.Servers, data.Inbounds, opts)
	if err != nil {
		return nil, nil, 0, core.SubscriptionFilterStats{}, err
	}
	for i := range groups {
		if groups[i].Kind == model.NodeGroupOBoard {
			groups[i].NodeCount = len(oboardNodes)
		}
	}
	merged, deduplicatedCount, filterStats, err := s.mergeWorkspaceOutputNodesWithStats(ctx, user, output, oboardNodes)
	return merged, groups, deduplicatedCount, filterStats, err
}

func (s *Server) workspaceAllNodes(ctx context.Context, user model.User) ([]core.SubscriptionNode, []model.NodeGroup, error) {
	groups, err := s.store.ListNodeGroups(ctx, user.ID)
	if err != nil {
		return nil, nil, err
	}
	groupIDs := make([]int64, 0, len(groups))
	for _, group := range groups {
		groupIDs = append(groupIDs, group.ID)
	}
	return s.workspaceSubscriptionNodes(ctx, user, &model.SubscriptionOutput{UserID: user.ID, GroupIDs: groupIDs, Enabled: true})
}

func (s *Server) mergeWorkspaceOutputNodes(ctx context.Context, user model.User, output *model.SubscriptionOutput, oboardNodes []core.SubscriptionNode) ([]core.SubscriptionNode, error) {
	nodes, _, _, err := s.mergeWorkspaceOutputNodesWithStats(ctx, user, output, oboardNodes)
	return nodes, err
}

func (s *Server) mergeWorkspaceOutputNodesWithStats(ctx context.Context, user model.User, output *model.SubscriptionOutput, oboardNodes []core.SubscriptionNode) ([]core.SubscriptionNode, int, core.SubscriptionFilterStats, error) {
	groups, err := s.store.ListNodeGroups(ctx, user.ID)
	if err != nil {
		return nil, 0, core.SubscriptionFilterStats{}, err
	}
	groupByID := map[int64]model.NodeGroup{}
	for _, group := range groups {
		groupByID[group.ID] = group
	}
	privateNodes, err := s.store.ListImportedNodes(ctx, user.ID)
	if err != nil {
		return nil, 0, core.SubscriptionFilterStats{}, err
	}
	privateByGroup := map[int64][]core.SubscriptionNode{}
	for _, item := range privateNodes {
		if !item.Enabled {
			continue
		}
		plain, decryptErr := security.DecryptSecret(s.sessionSecret, privateNodePurpose(user.ID, item.Fingerprint), item.ConfigEncrypted)
		if decryptErr != nil {
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(plain), &raw) != nil {
			continue
		}
		group := groupByID[item.GroupID]
		node := core.SubscriptionNodeFromPrivate(item, raw, group.Name)
		privateByGroup[item.GroupID] = append(privateByGroup[item.GroupID], node)
	}
	merged := []core.SubscriptionNode{}
	seen := map[string]bool{}
	deduplicatedCount := 0
	groupByNodeKey := map[string]int64{}
	for _, groupID := range output.GroupIDs {
		group, ok := groupByID[groupID]
		if !ok {
			continue
		}
		candidates := privateByGroup[groupID]
		if group.Kind == model.NodeGroupOBoard {
			candidates = oboardNodes
		}
		for _, node := range candidates {
			fingerprint, fingerprintErr := core.SubscriptionNodeFingerprint(node)
			if fingerprintErr != nil {
				continue
			}
			if seen[fingerprint] {
				deduplicatedCount++
				continue
			}
			seen[fingerprint] = true
			groupByNodeKey[node.Key] = groupID
			merged = append(merged, node)
		}
	}
	ordered := disambiguateWorkspaceNodeNames(merged)
	filtered, stats := core.ApplySubscriptionOutputFilters(ordered, output.Filters, groupByNodeKey)
	return filtered, deduplicatedCount, stats, nil
}

func fetchNodeSource(ctx context.Context, rawURL, etag, lastModified string) ([]byte, string, string, bool, error) {
	parsed, err := validateNodeSourceURL(rawURL)
	if err != nil {
		return nil, "", "", false, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: -1}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, splitErr
		}
		addresses, lookupErr := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if lookupErr != nil {
			return nil, lookupErr
		}
		for _, address := range addresses {
			if !isPublicNodeSourceIP(address) {
				return nil, errors.New("订阅来源解析到非公网地址")
			}
		}
		if len(addresses) == 0 {
			return nil, errors.New("订阅来源没有可用地址")
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	}}
	client := &http.Client{Timeout: nodeSourceTimeout, Transport: transport, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("订阅来源重定向过多")
		}
		_, redirectErr := validateNodeSourceURL(request.URL.String())
		return redirectErr
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", "", false, err
	}
	request.Header.Set("User-Agent", "OBoard-Subscription-Importer/1")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		request.Header.Set("If-Modified-Since", lastModified)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", "", false, errors.New("订阅来源请求失败")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return nil, etag, lastModified, true, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", "", false, fmt.Errorf("订阅来源返回 HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, nodeSourceMaxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", "", false, errors.New("读取订阅来源失败")
	}
	if len(body) > nodeSourceMaxBody {
		return nil, "", "", false, errors.New("订阅来源超过 1 MiB 限制")
	}
	return body, response.Header.Get("ETag"), response.Header.Get("Last-Modified"), false, nil
}

func validateNodeSourceURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("远程来源必须是无内嵌凭据的公网 HTTPS URL")
	}
	if parsed.Fragment != "" {
		parsed.Fragment = ""
	}
	if address, err := netip.ParseAddr(parsed.Hostname()); err == nil && !isPublicNodeSourceIP(address) {
		return nil, errors.New("远程来源不能使用内网、回环或保留地址")
	}
	return parsed, nil
}

func isPublicNodeSourceIP(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsUnspecified() {
		return false
	}
	reserved := []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001:db8::/32", "2001:10::/28", "fc00::/7", "fe80::/10", "ff00::/8"}
	for _, raw := range reserved {
		if prefix := netip.MustParsePrefix(raw); prefix.Contains(address) {
			return false
		}
	}
	return true
}

func (s *Server) nodeSourceDisplay(source model.NodeSource) string {
	plain, err := security.DecryptSecret(s.sessionSecret, nodeSourcePurpose(source.UserID, source.URLFingerprint), source.URLEncrypted)
	if err != nil {
		return "https://[不可用]"
	}
	parsed, err := url.Parse(plain)
	if err != nil {
		return "https://[不可用]"
	}
	return parsed.Scheme + "://" + parsed.Host + "/…"
}

func sanitizeNodeSource(source model.NodeSource, display string) model.NodeSource {
	source.URLDisplay = display
	source.URLFingerprint, source.URLEncrypted, source.ETag, source.LastModified = "", "", "", ""
	return source
}

func nodeSourcePurpose(userID int64, fingerprint string) string {
	return fmt.Sprintf("node-source-url:%d:%s", userID, fingerprint)
}

func privateNodePurpose(userID int64, fingerprint string) string {
	return fmt.Sprintf("private-node:%d:%s", userID, fingerprint)
}

func nodeWorkspaceID(parts []string) (int64, error) {
	if len(parts) == 0 {
		return 0, errors.New("missing id")
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func nodeWorkspaceFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, errors.New("resource not found"), 404)
	case err != nil && err.Error() == "forbidden":
		fail(w, err, 403)
	default:
		fail(w, err, 400)
	}
}

func privateImportPreviewNodes(nodes []core.ParsedPrivateNode) []map[string]any {
	result := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, map[string]any{"name": node.Name, "protocol": node.Protocol, "fingerprint": node.Fingerprint})
	}
	return result
}

func safeNodeSourceError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len([]rune(value)) > 300 {
		value = string([]rune(value)[:300])
	}
	return value
}

func nodeSourceKind(node core.SubscriptionNode) string {
	if strings.HasPrefix(node.Key, "private:") {
		return "private"
	}
	return "oboard"
}

func nodeLibraryEditable(node core.SubscriptionNode, groupID int64, groups []model.NodeGroup) bool {
	if !strings.HasPrefix(node.Key, "private:") {
		return false
	}
	for _, group := range groups {
		if group.ID == groupID && group.Kind == model.NodeGroupManual {
			return true
		}
	}
	return false
}

func privateImportedNodeID(key string) (int64, bool) {
	if !strings.HasPrefix(key, "private:") {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(key, "private:"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) importedNodeForEdit(ctx context.Context, userID int64, key string) (*model.ImportedNode, error) {
	id, ok := privateImportedNodeID(key)
	if !ok {
		return nil, errors.New("manual node id is required")
	}
	item, err := s.store.GetImportedNode(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if item.SourceID != nil {
		return nil, errors.New("remote source nodes are managed by refresh")
	}
	group, err := s.store.GetNodeGroup(ctx, userID, item.GroupID)
	if err != nil {
		return nil, err
	}
	if group.Kind != model.NodeGroupManual {
		return nil, errors.New("remote source nodes are managed by refresh")
	}
	return item, nil
}

func (s *Server) updateImportedNode(ctx context.Context, userID int64, key, name, content string, enabled *bool) (map[string]any, error) {
	item, err := s.importedNodeForEdit(ctx, userID, key)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name != "" && len([]rune(name)) > 80 {
		return nil, errors.New("node name must be between 1 and 80 characters")
	}
	if name == "" && strings.TrimSpace(content) == "" && enabled == nil {
		return nil, errors.New("nothing to update")
	}
	if strings.TrimSpace(content) != "" {
		result, parseErr := core.ParsePrivateSubscription(content)
		if parseErr != nil || len(result.Nodes) != 1 || len(result.Issues) != 0 {
			return nil, errors.New("edit content must contain exactly one valid node")
		}
		encrypted, encryptErr := s.encryptImportedNodes(userID, item.GroupID, nil, []core.ParsedPrivateNode{result.Nodes[0]})
		if encryptErr != nil {
			return nil, encryptErr
		}
		item.Protocol = result.Nodes[0].Protocol
		item.Name = result.Nodes[0].Name
		item.Fingerprint = result.Nodes[0].Fingerprint
		item.ConfigEncrypted = encrypted[0].ConfigEncrypted
	}
	if name != "" {
		item.Name = name
	}
	if enabled != nil {
		item.Enabled = *enabled
	}
	updated, updateErr := s.store.UpdateImportedNode(ctx, userID, item.ID, item)
	if updateErr != nil {
		return nil, updateErr
	}
	return map[string]any{"id": fmt.Sprintf("private:%d", updated.ID), "group_id": updated.GroupID, "name": updated.Name, "protocol": updated.Protocol, "source": "private", "editable": true, "enabled": updated.Enabled}, nil
}

func (s *Server) previewRemoteNodeSource(ctx context.Context, rawURL string) (*core.PrivateImportResult, error) {
	parsed, err := validateNodeSourceURL(rawURL)
	if err != nil {
		return nil, err
	}
	body, _, _, notModified, err := fetchNodeSource(ctx, parsed.String(), "", "")
	if err != nil {
		return nil, err
	}
	if notModified {
		return &core.PrivateImportResult{Nodes: []core.ParsedPrivateNode{}, Issues: []core.PrivateImportIssue{}}, nil
	}
	result, err := core.ParsePrivateSubscription(string(body))
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func groupIDForNode(node core.SubscriptionNode, groups []model.NodeGroup) int64 {
	if strings.HasPrefix(node.Key, "private:") {
		for _, group := range groups {
			if group.Name == node.Group {
				return group.ID
			}
		}
	}
	for _, group := range groups {
		if group.Kind == model.NodeGroupOBoard {
			return group.ID
		}
	}
	return 0
}

func disambiguateWorkspaceNodeNames(nodes []core.SubscriptionNode) []core.SubscriptionNode {
	counts := map[string]int{}
	for i := range nodes {
		base := strings.TrimSpace(nodes[i].Name)
		key := strings.ToLower(base)
		counts[key]++
		if counts[key] > 1 {
			nodes[i].Name = fmt.Sprintf("%s (%d)", base, counts[key])
			nodes[i].Raw["tag"] = nodes[i].Name
		}
	}
	return nodes
}
