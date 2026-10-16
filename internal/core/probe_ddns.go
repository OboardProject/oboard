package core

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// ProbeDDNSInventoryMaxAge is how long a list_network_interfaces result may
// be used to publish a bound address.
const ProbeDDNSInventoryMaxAge = 24 * time.Hour

// ProbeDDNSTarget is one A or AAAA value to publish.
type ProbeDDNSTarget struct {
	Type    string
	Content string
}

// ProbeDDNSAddressRequest is the evidence available when choosing what a DDNS
// record may publish. Interfaces come from the latest successful
// list_network_interfaces result, not from a typed name.
type ProbeDDNSAddressRequest struct {
	Record      model.ProbeDDNSRecord
	Server      model.Server
	Interfaces  []model.NetworkInterfaceInfo
	InventoryAt time.Time
	Now         time.Time
}

// ResolveProbeDDNSTargets returns the addresses to publish, or a Chinese error
// that must be shown instead of publishing. A private address is never
// returned when the record excludes private addresses.
func ResolveProbeDDNSTargets(req ProbeDDNSAddressRequest) ([]ProbeDDNSTarget, error) {
	if req.Record.InterfaceBound() {
		return resolveBoundProbeDDNS(req)
	}
	return resolvePublicProbeDDNS(req.Record, req.Server)
}

func resolvePublicProbeDDNS(record model.ProbeDDNSRecord, server model.Server) ([]ProbeDDNSTarget, error) {
	want4, want6, err := probeDDNSFamilies(record.RecordTypes, record.AddressFamily)
	if err != nil {
		return nil, err
	}
	var out []ProbeDDNSTarget
	if want4 {
		if target, ok, err := publicProbeTarget("A", server.PublicIPv4, record.ExcludePrivate); err != nil {
			return nil, err
		} else if ok {
			out = append(out, target)
		}
	}
	if want6 {
		if target, ok, err := publicProbeTarget("AAAA", ServerEntryIPv6(server), record.ExcludePrivate); err != nil {
			return nil, err
		} else if ok {
			out = append(out, target)
		}
	}
	if len(out) == 0 {
		if record.ExcludePrivate {
			return nil, fmt.Errorf("按过滤条件排除内网地址后，没有可发布的公网地址")
		}
		return nil, fmt.Errorf("服务器没有可供发布的公网地址")
	}
	return out, nil
}

func publicProbeTarget(kind, raw string, excludePrivate bool) (ProbeDDNSTarget, bool, error) {
	raw = strings.Trim(strings.TrimSpace(raw), "[]")
	if raw == "" {
		return ProbeDDNSTarget{}, false, nil
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return ProbeDDNSTarget{}, false, fmt.Errorf("服务器公网地址 %s 无法识别", raw)
	}
	addr = addr.Unmap()
	if excludePrivate && probeDDNSInternal(addr) {
		return ProbeDDNSTarget{}, false, nil
	}
	if kind == "A" && !addr.Is4() {
		return ProbeDDNSTarget{}, false, nil
	}
	if kind == "AAAA" && !addr.Is6() {
		return ProbeDDNSTarget{}, false, nil
	}
	return ProbeDDNSTarget{Type: kind, Content: addr.String()}, true, nil
}

func resolveBoundProbeDDNS(req ProbeDDNSAddressRequest) ([]ProbeDDNSTarget, error) {
	record := req.Record
	name := strings.TrimSpace(record.InterfaceName)
	wanted := strings.TrimSpace(record.InterfaceAddress)
	if name == "" || (wanted == "" && record.InterfacePrefix == "") {
		return nil, fmt.Errorf("绑定网卡时必须指定网卡名称及固定地址或 CIDR 前缀")
	}
	if req.InventoryAt.IsZero() || len(req.Interfaces) == 0 {
		return nil, fmt.Errorf("尚未读取该服务器的网卡，未更新解析")
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if now.Sub(req.InventoryAt) > ProbeDDNSInventoryMaxAge {
		return nil, fmt.Errorf("网卡列表已过期（上次读取 %s），未更新解析", req.InventoryAt.UTC().Format(time.RFC3339))
	}
	normalizedWanted, err := normalizeProbeDDNSAddress(wanted)
	if err != nil && record.InterfacePrefix == "" {
		return nil, fmt.Errorf("绑定地址 %s 无法识别", wanted)
	}
	var iface *model.NetworkInterfaceInfo
	for i := range req.Interfaces {
		if req.Interfaces[i].Name == name {
			iface = &req.Interfaces[i]
			break
		}
	}
	if iface == nil {
		return nil, fmt.Errorf("网卡 %s 已不在最近一次读取结果中，未更新解析", name)
	}
	if iface.Loopback {
		return nil, fmt.Errorf("不能使用回环网卡 %s 的地址，未更新解析", name)
	}
	if !iface.Up {
		return nil, fmt.Errorf("网卡 %s 当前未启用，未更新解析", name)
	}
	if !iface.Running {
		return nil, fmt.Errorf("网卡 %s 当前未运行，未更新解析", name)
	}
	if record.InterfacePrefix != "" {
		network, err := netip.ParsePrefix(record.InterfacePrefix)
		if err != nil || network.Addr().Is4In6() || wanted != "" {
			return nil, fmt.Errorf("地址前缀无效或与固定地址冲突")
		}
		want4, want6, err := probeDDNSFamilies(record.RecordTypes, record.AddressFamily)
		if err != nil {
			return nil, err
		}
		matches := map[netip.Addr]bool{}
		for _, raw := range iface.Addresses {
			value, err := normalizeProbeDDNSAddress(raw)
			if err != nil {
				continue
			}
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				continue
			}
			addr := prefix.Addr().Unmap()
			if !network.Contains(addr) || (addr.Is4() && !want4) || (addr.Is6() && !want6) || (record.ExcludePrivate && probeDDNSInternal(addr)) {
				continue
			}
			matches[addr] = true
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("网卡 %s 没有符合前缀 %s 和过滤条件的地址，未更新解析", name, network)
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("网卡 %s 的前缀 %s 匹配到多个地址，请缩小前缀，未更新解析", name, network)
		}
		for addr := range matches {
			kind := "AAAA"
			if addr.Is4() {
				kind = "A"
			}
			return []ProbeDDNSTarget{{Type: kind, Content: addr.String()}}, nil
		}
	}
	var matched string
	for _, raw := range iface.Addresses {
		normalized, err := normalizeProbeDDNSAddress(raw)
		if err != nil {
			continue
		}
		if normalized == normalizedWanted {
			matched = normalized
			break
		}
	}
	if matched == "" {
		return nil, fmt.Errorf("地址 %s 已不在网卡 %s 的最近一次读取结果中，未更新解析", normalizedWanted, name)
	}
	prefix, err := netip.ParsePrefix(matched)
	if err != nil {
		return nil, fmt.Errorf("地址 %s 无法识别", matched)
	}
	addr := prefix.Addr().Unmap()
	if record.ExcludePrivate && probeDDNSInternal(addr) {
		return nil, fmt.Errorf("地址 %s 是内网地址，已按过滤条件跳过，未发布解析", addr.String())
	}
	kind := "AAAA"
	if addr.Is4() {
		kind = "A"
	}
	if record.AddressFamily == "ipv4" && kind != "A" {
		return nil, fmt.Errorf("地址 %s 不是 IPv4，未更新解析", addr.String())
	}
	if record.AddressFamily == "ipv6" && kind != "AAAA" {
		return nil, fmt.Errorf("地址 %s 不是 IPv6，未更新解析", addr.String())
	}
	switch record.RecordTypes {
	case "a":
		if kind != "A" {
			return nil, fmt.Errorf("地址 %s 与 A 记录不一致，未更新解析", addr.String())
		}
	case "aaaa":
		if kind != "AAAA" {
			return nil, fmt.Errorf("地址 %s 与 AAAA 记录不一致，未更新解析", addr.String())
		}
	case "both":
		return nil, fmt.Errorf("绑定单个网卡地址时不能同时发布 A 和 AAAA")
	default:
		return nil, fmt.Errorf("记录类型无效")
	}
	return []ProbeDDNSTarget{{Type: kind, Content: addr.String()}}, nil
}

func probeDDNSFamilies(recordTypes, family string) (bool, bool, error) {
	recordTypes = strings.ToLower(strings.TrimSpace(recordTypes))
	family = strings.ToLower(strings.TrimSpace(family))
	var want4, want6 bool
	switch recordTypes {
	case "a":
		want4 = true
	case "aaaa":
		want6 = true
	case "both":
		want4, want6 = true, true
	default:
		return false, false, fmt.Errorf("记录类型无效")
	}
	switch family {
	case "", "any":
	case "ipv4":
		if !want4 {
			return false, false, fmt.Errorf("记录类型与 IPv4 过滤不一致")
		}
		want6 = false
	case "ipv6":
		if !want6 {
			return false, false, fmt.Errorf("记录类型与 IPv6 过滤不一致")
		}
		want4 = false
	default:
		return false, false, fmt.Errorf("地址族无效")
	}
	return want4, want6, nil
}

func normalizeProbeDDNSAddress(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if prefix, err := netip.ParsePrefix(raw); err == nil && prefix.IsValid() {
		addr := prefix.Addr().Unmap()
		return netip.PrefixFrom(addr, prefix.Bits()).String(), nil
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return "", err
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()).String(), nil
}

func probeDDNSInternal(addr netip.Addr) bool {
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	if addr.Is4() {
		octets := addr.As4()
		if octets[0] == 100 && octets[1]&0xc0 == 64 {
			return true
		}
	}
	return false
}
