package controller

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

type MCPResourceRef struct {
	Type  string `json:"type"`
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Ref   string `json:"ref"`
	Label string `json:"label"`
}

type mcpResourceResolution struct {
	Value      *MCPResourceRef
	Candidates []MCPResourceRef
}

func normalizeMCPResourceName(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '_' || r == '-' || r == '/' {
			return -1
		}
		return unicode.ToLower(r)
	}, strings.TrimSpace(value))
}

func splitMCPResourceRef(value, expectedType string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", sql.ErrNoRows
	}
	resourceType, target, found := strings.Cut(value, ":")
	if !found {
		resourceType, target = expectedType, value
	}
	resourceType = strings.ToLower(strings.TrimSpace(resourceType))
	if resourceType != expectedType || strings.TrimSpace(target) == "" {
		return "", "", fmt.Errorf("expected %s resource reference", expectedType)
	}
	return resourceType, strings.TrimSpace(target), nil
}

func (s *Server) resolveServerRef(ctx context.Context, principal application.Principal, value string) (mcpResourceResolution, error) {
	_, target, err := splitMCPResourceRef(value, "server")
	if err != nil {
		return mcpResourceResolution{}, err
	}
	items, err := s.store.ListServers(ctx)
	if err != nil {
		return mcpResourceResolution{}, err
	}
	if id, parseErr := strconv.ParseInt(target, 10, 64); parseErr == nil && id > 0 {
		for _, item := range items {
			if item.ID == id && principal.AllowsInt64("server_ids", id) {
				ref := serverMCPResourceRef(item)
				return mcpResourceResolution{Value: &ref}, nil
			}
		}
		return mcpResourceResolution{}, sql.ErrNoRows
	}
	wanted := normalizeMCPResourceName(target)
	matches := []MCPResourceRef{}
	for _, item := range items {
		if principal.AllowsInt64("server_ids", item.ID) && normalizeMCPResourceName(item.Name) == wanted {
			matches = append(matches, serverMCPResourceRef(item))
		}
	}
	return finishMCPResourceResolution(matches)
}

func serverMCPResourceRef(item model.Server) MCPResourceRef {
	return MCPResourceRef{Type: "server", ID: item.ID, Name: item.Name, Ref: "server:" + strconv.FormatInt(item.ID, 10), Label: item.Name}
}

func (s *Server) resolveInboundRef(ctx context.Context, principal application.Principal, value string) (mcpResourceResolution, error) {
	_, target, err := splitMCPResourceRef(value, "inbound")
	if err != nil {
		return mcpResourceResolution{}, err
	}
	items, err := s.store.ListInbounds(ctx)
	if err != nil {
		return mcpResourceResolution{}, err
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return mcpResourceResolution{}, err
	}
	serverNames := map[int64]string{}
	for _, item := range servers {
		serverNames[item.ID] = item.Name
	}
	if id, parseErr := strconv.ParseInt(target, 10, 64); parseErr == nil && id > 0 {
		for _, item := range items {
			if item.ID == id && principal.AllowsInt64("server_ids", item.ServerID) {
				ref := inboundMCPResourceRef(item, serverNames[item.ServerID])
				return mcpResourceResolution{Value: &ref}, nil
			}
		}
		return mcpResourceResolution{}, sql.ErrNoRows
	}
	serverPart, inboundPart := "", target
	if left, right, found := strings.Cut(target, "/"); found {
		serverPart, inboundPart = left, right
	}
	wantedServer, wantedInbound := normalizeMCPResourceName(serverPart), normalizeMCPResourceName(inboundPart)
	matches := []MCPResourceRef{}
	for _, item := range items {
		if !principal.AllowsInt64("server_ids", item.ServerID) {
			continue
		}
		nameMatch := normalizeMCPResourceName(item.Name) == wantedInbound || normalizeMCPResourceName(string(item.Protocol)) == wantedInbound
		serverMatch := wantedServer == "" || normalizeMCPResourceName(serverNames[item.ServerID]) == wantedServer
		if nameMatch && serverMatch {
			matches = append(matches, inboundMCPResourceRef(item, serverNames[item.ServerID]))
		}
	}
	return finishMCPResourceResolution(matches)
}

func inboundMCPResourceRef(item model.Inbound, serverName string) MCPResourceRef {
	label := serverName + " / " + item.Name + " (" + string(item.Protocol) + ")"
	return MCPResourceRef{Type: "inbound", ID: item.ID, Name: item.Name, Ref: "inbound:" + strconv.FormatInt(item.ID, 10), Label: label}
}

func (s *Server) resolveProxyPathRef(ctx context.Context, principal application.Principal, value string) (mcpResourceResolution, error) {
	_, target, err := splitMCPResourceRef(value, "proxy_path")
	if err != nil {
		return mcpResourceResolution{}, err
	}
	paths, err := s.store.ListProxyPaths(ctx)
	if err != nil {
		return mcpResourceResolution{}, err
	}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return mcpResourceResolution{}, err
	}
	allowed := map[int64]bool{}
	for _, inbound := range inbounds {
		allowed[inbound.ID] = principal.AllowsInt64("server_ids", inbound.ServerID)
	}
	wanted := normalizeMCPResourceName(target)
	matches := []MCPResourceRef{}
	for _, item := range paths {
		if !allowed[item.InboundID] || !principal.AllowsInt64("proxy_path_ids", item.ID) {
			continue
		}
		idMatch := strconv.FormatInt(item.ID, 10) == target
		if idMatch || normalizeMCPResourceName(item.Name) == wanted {
			matches = append(matches, MCPResourceRef{Type: "proxy_path", ID: item.ID, Name: item.Name, Ref: "proxy_path:" + strconv.FormatInt(item.ID, 10), Label: item.Name})
		}
	}
	return finishMCPResourceResolution(matches)
}

func (s *Server) resolveUserRef(ctx context.Context, principal application.Principal, value string) (mcpResourceResolution, error) {
	_, target, err := splitMCPResourceRef(value, "user")
	if err != nil {
		return mcpResourceResolution{}, err
	}
	items, err := s.store.ListUsers(ctx)
	if err != nil {
		return mcpResourceResolution{}, err
	}
	wanted := normalizeMCPResourceName(target)
	matches := []MCPResourceRef{}
	for _, item := range items {
		idMatch := strconv.FormatInt(item.ID, 10) == target
		nameMatch := normalizeMCPResourceName(item.Username) == wanted || normalizeMCPResourceName(item.Nickname) == wanted
		if principal.AllowsInt64("user_ids", item.ID) && (idMatch || nameMatch) {
			matches = append(matches, MCPResourceRef{Type: "user", ID: item.ID, Name: item.Username, Ref: "user:" + strconv.FormatInt(item.ID, 10), Label: item.Username})
		}
	}
	return finishMCPResourceResolution(matches)
}

func (s *Server) resolveCertificateRef(ctx context.Context, principal application.Principal, value string) (mcpResourceResolution, error) {
	_, target, err := splitMCPResourceRef(value, "certificate")
	if err != nil {
		return mcpResourceResolution{}, err
	}
	items, err := s.store.ListCertificates(ctx)
	if err != nil {
		return mcpResourceResolution{}, err
	}
	wanted := normalizeMCPResourceName(target)
	matches := []MCPResourceRef{}
	for _, item := range items {
		if item.IssuanceServerID != nil && !principal.AllowsInt64("server_ids", *item.IssuanceServerID) {
			continue
		}
		idMatch := strconv.FormatInt(item.ID, 10) == target
		nameMatch := normalizeMCPResourceName(item.Name) == wanted || normalizeMCPResourceName(item.PrimaryDomain) == wanted
		if idMatch || nameMatch {
			matches = append(matches, MCPResourceRef{Type: "certificate", ID: item.ID, Name: item.Name, Ref: "certificate:" + strconv.FormatInt(item.ID, 10), Label: item.Name})
		}
	}
	return finishMCPResourceResolution(matches)
}

func (s *Server) resolveExternalOutboundRef(ctx context.Context, principal application.Principal, value string) (mcpResourceResolution, error) {
	_, target, err := splitMCPResourceRef(value, "external_outbound")
	if err != nil {
		return mcpResourceResolution{}, err
	}
	items, err := s.store.ListExternalOutbounds(ctx)
	if err != nil {
		return mcpResourceResolution{}, err
	}
	wanted := normalizeMCPResourceName(target)
	matches := []MCPResourceRef{}
	for _, item := range items {
		if item.ServerID != nil && !principal.AllowsInt64("server_ids", *item.ServerID) {
			continue
		}
		idMatch := strconv.FormatInt(item.ID, 10) == target
		if idMatch || normalizeMCPResourceName(item.Name) == wanted {
			matches = append(matches, MCPResourceRef{Type: "external_outbound", ID: item.ID, Name: item.Name, Ref: "external_outbound:" + strconv.FormatInt(item.ID, 10), Label: item.Name})
		}
	}
	return finishMCPResourceResolution(matches)
}

func finishMCPResourceResolution(matches []MCPResourceRef) (mcpResourceResolution, error) {
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Label == matches[j].Label {
			return matches[i].ID < matches[j].ID
		}
		return matches[i].Label < matches[j].Label
	})
	if len(matches) == 0 {
		return mcpResourceResolution{}, sql.ErrNoRows
	}
	if len(matches) == 1 {
		return mcpResourceResolution{Value: &matches[0]}, nil
	}
	return mcpResourceResolution{Candidates: matches}, nil
}
