package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/OboardProject/oboard/internal/version"
)

const mcpAPIVersion = "2026-08-25"
const mcpMinProtocol = "2025-11-25"

type mcpDeprecatedTool struct {
	Name        string `json:"name"`
	Replacement string `json:"replacement,omitempty"`
	RemoveAfter string `json:"remove_after,omitempty"`
}

type mcpCapabilityManifest struct {
	ServerVersion      string              `json:"server_version"`
	APIVersion         string              `json:"api_version"`
	CapabilityRevision int64               `json:"capability_revision"`
	ToolsetHash        string              `json:"toolset_hash"`
	MinMCPProtocol     string              `json:"min_mcp_protocol"`
	Features           map[string]bool     `json:"features"`
	DeprecatedTools    []mcpDeprecatedTool `json:"deprecated_tools"`
	ToolCount          int                 `json:"tool_count"`
	InstructionsHash   string              `json:"instructions_hash"`
}

var (
	mcpRegistryMu       sync.RWMutex
	mcpRegistryManifest *mcpCapabilityManifest
	mcpRegistryHash     string
	mcpRegistryRevision int64
)

func mcpCapabilityFeatures() map[string]bool {
	return map[string]bool{
		"server_management":       true,
		"node_management":         true,
		"topology_management":     true,
		"subscription_management": true,
		"dns_management":          true,
		"certificate_management":  true,
		"audit":                   true,
		"traffic_monitor":         true,
		"deployment":              true,
		"speedtest":               false,
		"waf_placeholder":         false,
	}
}

func mcpDeprecatedTools() []mcpDeprecatedTool {
	return []mcpDeprecatedTool{}
}

func (s *Server) mcpToolsetHash() string {
	mcpRegistryMu.RLock()
	if mcpRegistryHash != "" && mcpRegistryManifest != nil {
		h := mcpRegistryHash
		mcpRegistryMu.RUnlock()
		return h
	}
	mcpRegistryMu.RUnlock()
	mcpRegistryMu.Lock()
	defer mcpRegistryMu.Unlock()
	if mcpRegistryHash != "" && mcpRegistryManifest != nil {
		return mcpRegistryHash
	}
	return s.computeMCPToolsetHashLocked()
}

func mcpRevisionFromHash(hash string) int64 {
	trimmed := strings.TrimPrefix(hash, "sha256:")
	if len(trimmed) < 16 {
		return 1
	}
	decoded, err := hex.DecodeString(trimmed[:16])
	if err != nil || len(decoded) < 8 {
		return 1
	}
	var v int64
	for i := 0; i < 8; i++ {
		v = (v << 8) | int64(decoded[i])
	}
	if v < 0 {
		v = -v
	}
	if v == 0 {
		v = 1
	}
	return v
}

func mcpInstructionsHash() string {
	sum := sha256.Sum256([]byte(mcpServerInstructions))
	return "sha256:" + hex.EncodeToString(sum[:8])
}

func (s *Server) mcpCurrentManifest() mcpCapabilityManifest {
	mcpRegistryMu.RLock()
	if mcpRegistryManifest != nil {
		cached := *mcpRegistryManifest
		mcpRegistryMu.RUnlock()
		return cached
	}
	mcpRegistryMu.RUnlock()

	mcpRegistryMu.Lock()
	defer mcpRegistryMu.Unlock()
	if mcpRegistryManifest != nil {
		return *mcpRegistryManifest
	}
	hash := s.computeMCPToolsetHashLocked()
	rev := mcpRevisionFromHash(hash)
	manifest := mcpCapabilityManifest{
		ServerVersion:      version.Version,
		APIVersion:         mcpAPIVersion,
		CapabilityRevision: rev,
		ToolsetHash:        hash,
		MinMCPProtocol:     mcpMinProtocol,
		Features:           mcpCapabilityFeatures(),
		DeprecatedTools:    mcpDeprecatedTools(),
		ToolCount:          s.mcpToolCountLocked(),
		InstructionsHash:   mcpInstructionsHash(),
	}
	mcpRegistryManifest = &manifest
	mcpRegistryHash = hash
	mcpRegistryRevision = rev
	return manifest
}

func (s *Server) computeMCPToolsetHashLocked() string {
	descriptors := s.capabilities.AllMCPDescriptors()
	type hashEntry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Input       string `json:"input_schema"`
		Output      string `json:"output_schema"`
		Version     string `json:"version"`
	}
	entries := make([]hashEntry, 0, len(descriptors)+16)
	for _, d := range descriptors {
		entries = append(entries, hashEntry{
			Name: d.Name, Description: d.Description,
			Input: string(d.InputSchema), Output: string(d.OutputSchema),
			Version: d.Version,
		})
	}
	for _, name := range []string{
		"oboard_task", "oboard_commit_task",
		"oboard_discover", "oboard_get_capability_schema",
		"oboard_plan_desired_state", "oboard_validate_desired_state", "oboard_validate_form", "oboard_submit_changeset",
		"oboard_get_changeset", "oboard_get_workflow", "oboard_cancel_workflow", "oboard_retry_workflow_step", "oboard_redeem_external_action",
		"system_get_capabilities", "system_bootstrap",
		"server_terminal_command", "server_terminal_open", "server_terminal_io", "server_terminal_resize", "server_terminal_close",
	} {
		entries = append(entries, hashEntry{Name: name, Version: "1"})
	}
	for _, r := range s.mcpRecipes() {
		entries = append(entries, hashEntry{Name: "recipe:" + r.ID, Version: r.Version})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	raw, _ := json.Marshal(entries)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:12])
}

func (s *Server) mcpToolCountLocked() int {
	count := 0
	for range s.capabilities.AllMCPDescriptors() {
		count++
	}
	count += 18
	return count
}

func (s *Server) mcpInvalidateRegistry() {
	mcpRegistryMu.Lock()
	mcpRegistryManifest = nil
	mcpRegistryHash = ""
	mcpRegistryRevision = 0
	mcpRegistryMu.Unlock()
	newManifest := s.mcpCurrentManifest()
	_ = newManifest
	s.notifyMCPToolsChanged()
}
