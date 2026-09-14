package confighealth

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func (c *collector) checkInbounds() {
	for _, inbound := range c.input.Inbounds {
		c.checkInboundDocument(inbound)
		c.checkInboundReferences(inbound)
	}
	c.checkInboundPortConflicts()
}

// checkInboundDocument runs the same validation the store applies on write. A
// failure here is the case the normal form cannot repair: the operator's
// correction is rejected together with the pre-existing defect, so the record
// is stuck until something can rewrite it without that gate.
func (c *collector) checkInboundDocument(inbound model.Inbound) {
	validationErr := core.ValidateStoredInbound(inbound)
	if validationErr == nil {
		return
	}
	finding := Finding{
		Code:         "inbound.config.invalid",
		Severity:     SeverityBlocking,
		Scope:        ScopeInbound,
		ResourceID:   inbound.ID,
		ResourceName: inbound.Name,
		ServerID:     inbound.ServerID,
		Title:        "入口配置不符合当前协议模型",
		Detail:       validationErr.Error(),
		Remedy:       Remedy{Kind: RemedyNone, Summary: "需要手动修正该入口的协议参数"},
	}
	if fieldErr := asConfigFieldError(validationErr); fieldErr != nil {
		finding.Path = fieldErr.Path
	}
	repair, err := core.NormalizeInboundConfig(inbound)
	switch {
	case err == nil && repair.Resolved && len(repair.RemovedPaths) > 0:
		finding.Remedy = Remedy{
			Kind:    RemedyNormalize,
			Summary: "移除当前协议不支持的字段后即可恢复正常",
			Fields:  repair.RemovedPaths,
		}
	case inbound.Enabled:
		finding.Remedy = Remedy{
			Kind:    RemedyDisable,
			Summary: "无法自动规范化，可先停用该入口让其余配置恢复下发",
		}
	}
	c.add(finding)
}

// checkInboundReferences reports stored references whose target is gone or
// disabled. These do not break the running configuration - the referenced
// values were merged into the document when the inbound was saved - but every
// later save of that inbound re-resolves the reference and fails, so the
// operator cannot edit the入口 at all until the dangling key is cleared.
func (c *collector) checkInboundReferences(inbound model.Inbound) {
	_, ok := c.serverByID[inbound.ServerID]
	if !ok {
		c.add(Finding{
			Code:         "inbound.server.missing",
			Severity:     SeverityBlocking,
			Scope:        ScopeInbound,
			ResourceID:   inbound.ID,
			ResourceName: inbound.Name,
			Title:        "入口所属的服务器已不存在",
			Detail:       fmt.Sprintf("服务器 %d 已被删除", inbound.ServerID),
			Remedy:       Remedy{Kind: RemedyDelete, Summary: "删除这个没有归属服务器的入口", Destructive: true},
		})
	}
	document := decodeDocument(inbound.ConfigJSON)

	if id := documentInt64(document, "node_preset_id"); id > 0 && !c.input.UsableNodePresetIDs[id] {
		c.add(Finding{
			Code:         "inbound.node_preset.unusable",
			Severity:     SeverityWarning,
			Scope:        ScopeInbound,
			ResourceID:   inbound.ID,
			ResourceName: inbound.Name,
			ServerID:     inbound.ServerID,
			Title:        "入口引用的节点预设已删除或已停用",
			Detail:       fmt.Sprintf("预设 %d 不可用；已生效的参数不受影响，但保存该入口时会失败", id),
			Path:         "config_json.node_preset_id",
			Remedy: Remedy{
				Kind:    RemedyNormalize,
				Summary: "清除该预设引用，保留已合并的参数",
				Fields:  []string{"node_preset_id"},
			},
		})
	}
	if id := documentInt64(document, "snell_profile_id"); id > 0 && !c.input.UsableSnellProfileIDs[id] {
		c.add(Finding{
			Code:         "inbound.snell_profile.unusable",
			Severity:     SeverityWarning,
			Scope:        ScopeInbound,
			ResourceID:   inbound.ID,
			ResourceName: inbound.Name,
			ServerID:     inbound.ServerID,
			Title:        "入口引用的 Snell 参数集已删除或已停用",
			Detail:       fmt.Sprintf("参数集 %d 不可用；已生效的参数不受影响，但保存该入口时会失败", id),
			Path:         "config_json.snell_profile_id",
			Remedy: Remedy{
				Kind:    RemedyNormalize,
				Summary: "清除该参数集引用，保留已合并的参数",
				Fields:  []string{"snell_profile_id"},
			},
		})
	}
	if inbound.CertificateID != nil && *inbound.CertificateID > 0 && !c.input.CertificateIDs[*inbound.CertificateID] {
		c.add(Finding{
			Code:         "inbound.certificate.missing",
			Severity:     SeverityBlocking,
			Scope:        ScopeInbound,
			ResourceID:   inbound.ID,
			ResourceName: inbound.Name,
			ServerID:     inbound.ServerID,
			Title:        "入口绑定的证书已不存在",
			Detail:       fmt.Sprintf("证书 %d 已被删除，该入口无法完成 TLS 下发", *inbound.CertificateID),
			Remedy:       Remedy{Kind: RemedyNone, Summary: "请为该入口重新选择证书或改用其他证书模式"},
		})
	}
	if inbound.DNSSyncEnabled && inbound.DNSCredentialID != nil && *inbound.DNSCredentialID > 0 && !c.input.DNSCredentialIDs[*inbound.DNSCredentialID] {
		c.add(Finding{
			Code:         "inbound.dns_credential.missing",
			Severity:     SeverityWarning,
			Scope:        ScopeInbound,
			ResourceID:   inbound.ID,
			ResourceName: inbound.Name,
			ServerID:     inbound.ServerID,
			Title:        "入口开启了 DNS 记录同步但凭据已删除",
			Detail:       fmt.Sprintf("DNS 凭据 %d 不存在，解析记录不会再被更新", *inbound.DNSCredentialID),
			Remedy:       Remedy{Kind: RemedyNone, Summary: "请重新选择 DNS 凭据或关闭记录同步"},
		})
	}
}

// checkInboundPortConflicts reports two enabled listeners that would bind the
// same socket. Only pairs whose networks certainly overlap are reported: a
// missed conflict is recoverable, a false alarm on a working topology is not.
func (c *collector) checkInboundPortConflicts() {
	type key struct {
		serverID int64
		port     int
		network  string
	}
	groups := map[key][]model.Inbound{}
	for _, inbound := range c.input.Inbounds {
		if !inbound.Enabled || inbound.Port <= 0 {
			continue
		}
		for _, network := range inboundNetworks(inbound) {
			k := key{serverID: inbound.ServerID, port: inbound.Port, network: network}
			groups[k] = append(groups[k], inbound)
		}
	}
	keys := make([]key, 0, len(groups))
	for k, members := range groups {
		if len(members) > 1 {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].serverID != keys[j].serverID {
			return keys[i].serverID < keys[j].serverID
		}
		if keys[i].port != keys[j].port {
			return keys[i].port < keys[j].port
		}
		return keys[i].network < keys[j].network
	})
	for _, k := range keys {
		members := groups[k]
		sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
		for i := 1; i < len(members); i++ {
			if !listenScopesOverlap(members[i-1].ListenIP, members[i].ListenIP) {
				continue
			}
			c.add(Finding{
				Code:         "inbound.port.conflict",
				Severity:     SeverityBlocking,
				Scope:        ScopeInbound,
				ResourceID:   members[i].ID,
				ResourceName: members[i].Name,
				ServerID:     members[i].ServerID,
				Title:        "入口端口与同服务器上的另一个入口冲突",
				Detail: fmt.Sprintf("与入口「%s」(#%d) 同时监听 %s/%d，后启动的一个会失败",
					members[i-1].Name, members[i-1].ID, strings.ToUpper(k.network), k.port),
				Path:   "port",
				Remedy: Remedy{Kind: RemedyDisable, Summary: "先停用其中一个入口，再为它改配一个空闲端口"},
			})
		}
	}
}

// inboundNetworks lists the transport networks a listener certainly occupies.
// A protocol whose UDP behaviour depends on runtime options contributes only
// its certain network, so the conflict check cannot produce a false alarm.
func inboundNetworks(inbound model.Inbound) []string {
	switch inbound.Protocol {
	case model.ProtocolHY2:
		return []string{"udp"}
	case model.ProtocolMieru:
		transport := strings.ToLower(strings.TrimSpace(documentString(decodeDocument(inbound.ConfigJSON), "transport")))
		if transport == "udp" {
			return []string{"udp"}
		}
		return []string{"tcp"}
	default:
		return []string{"tcp"}
	}
}

// listenScopesOverlap reports whether two listen addresses contend for the same
// port. An empty or wildcard address covers every address on the host.
func listenScopesOverlap(a, b string) bool {
	na, nb := normalizeListenIP(a), normalizeListenIP(b)
	if na == "" || nb == "" {
		return true
	}
	return na == nb
}

func normalizeListenIP(value string) string {
	trimmed := strings.TrimSpace(value)
	switch trimmed {
	case "", "0.0.0.0", "::", "[::]":
		return ""
	default:
		return trimmed
	}
}

func asConfigFieldError(err error) *core.ConfigFieldError {
	var fieldErr *core.ConfigFieldError
	if errors.As(err, &fieldErr) {
		return fieldErr
	}
	return nil
}

func decodeDocument(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var document map[string]any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil
	}
	return document
}

func documentInt64(document map[string]any, key string) int64 {
	if document == nil {
		return 0
	}
	switch typed := document[key].(type) {
	case json.Number:
		value, err := typed.Int64()
		if err != nil {
			return 0
		}
		return value
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func documentString(document map[string]any, key string) string {
	if document == nil {
		return ""
	}
	value, _ := document[key].(string)
	return value
}
