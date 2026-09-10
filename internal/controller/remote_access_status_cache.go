package controller

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/OboardProject/oboard/internal/model"
)

// remoteAccessStatusCache remembers, per server, the remote access capability
// content this Controller has already committed. A steady Agent re-reports the
// same capability on every health report; without this the Controller would
// rewrite an identical row on every heartbeat for every node.
//
// The cache is only ever advanced after a successful database commit, so a
// failed write is retried by the next report instead of being remembered as
// persisted. Each entry is bound to the Agent identity that produced it: a
// re-enrolled or replaced Agent never inherits the previous Agent's state.
type remoteAccessStatusCache struct {
	mu      sync.Mutex
	entries map[int64]remoteAccessStatusEntry
}

type remoteAccessStatusEntry struct {
	agentID  string
	identity string
}

// normalizeRemoteAccessReport returns the canonical form of one report.
// Capabilities are a set, so they are trimmed, de-duplicated and sorted; every
// other field keeps its reported value and default, because only the set is
// order-insensitive.
func normalizeRemoteAccessReport(report model.RemoteAccessReport) model.RemoteAccessReport {
	seen := make(map[string]bool, len(report.Capabilities))
	capabilities := make([]string, 0, len(report.Capabilities))
	for _, capability := range report.Capabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" || seen[capability] {
			continue
		}
		seen[capability] = true
		capabilities = append(capabilities, capability)
	}
	sort.Strings(capabilities)
	report.Capabilities = capabilities
	report.LocalMode = strings.TrimSpace(report.LocalMode)
	if report.LocalMode == "" {
		report.LocalMode = model.RemoteAccessModeStandard
	}
	return report
}

// remoteAccessReportIdentity renders the content identity of a normalized
// report. Two reports share an identity exactly when persisting either one
// would leave the same stored capability state.
func remoteAccessReportIdentity(report model.RemoteAccessReport) string {
	var b strings.Builder
	b.WriteString(report.LocalMode)
	b.WriteByte('|')
	for _, capability := range report.Capabilities {
		b.WriteString(capability)
		b.WriteByte(',')
	}
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(report.LocalAllow.RemoteTerminal))
	b.WriteString(strconv.FormatBool(report.LocalAllow.MCPEnabled))
	b.WriteString(strconv.FormatBool(report.LocalAllow.ScriptsEnabled))
	b.WriteString(strconv.FormatBool(report.LocalAllow.HostPowerEnabled))
	return b.String()
}

// persistRemoteAccessStatus stores the Agent-reported capability only when its
// content differs from the last state committed for the same Agent identity.
// It never caches the admin privilege decision itself: Web and MCP keep
// resolving privileges from the stored status through their own authority.
func (s *Server) persistRemoteAccessStatus(ctx context.Context, serverID int64, agentID string, report model.RemoteAccessReport) error {
	if serverID <= 0 {
		return nil
	}
	agentID = strings.TrimSpace(agentID)
	normalized := normalizeRemoteAccessReport(report)
	identity := remoteAccessReportIdentity(normalized)

	s.remoteAccessStatusCache.mu.Lock()
	entry, ok := s.remoteAccessStatusCache.entries[serverID]
	s.remoteAccessStatusCache.mu.Unlock()
	if ok && entry.agentID == agentID && entry.identity == identity {
		s.hotPath.remoteAccessSkipped.Add(1)
		return nil
	}

	if err := s.store.UpsertServerRemoteAccessStatus(ctx, serverID, normalized); err != nil {
		s.hotPath.remoteAccessFailed.Add(1)
		// Leave the cache untouched: the next report must retry this write.
		return err
	}
	s.hotPath.remoteAccessWritten.Add(1)
	s.remoteAccessStatusCache.mu.Lock()
	if s.remoteAccessStatusCache.entries == nil {
		s.remoteAccessStatusCache.entries = map[int64]remoteAccessStatusEntry{}
	}
	s.remoteAccessStatusCache.entries[serverID] = remoteAccessStatusEntry{agentID: agentID, identity: identity}
	s.remoteAccessStatusCache.mu.Unlock()
	return nil
}

// forgetRemoteAccessStatus drops the committed-content memory for one server so
// a deleted or re-enrolled node is persisted from scratch.
func (s *Server) forgetRemoteAccessStatus(serverID int64) {
	s.remoteAccessStatusCache.mu.Lock()
	delete(s.remoteAccessStatusCache.entries, serverID)
	s.remoteAccessStatusCache.mu.Unlock()
}
