package core

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	VPNGateCatalogMaxBytes   = 16 << 20
	VPNGateCatalogMaxRows    = 2000
	vpngateConfigMaxBytes    = 64 << 10
	vpngatePEMMaxBytes       = 16 << 10
	VPNGateDefaultMTU        = 1280
	VPNGateMaxPoolSize       = 8
	VPNGateDefaultPoolSize   = 4
	VPNGateMaxHotStandby     = 2
	VPNGateMinRefreshMinutes = 15
	VPNGateMaxRefreshMinutes = 24 * 60
	vpngateMaxCountries      = 32
	vpngateMaxASNs           = 64
	vpngateNameMaxRunes      = 64
)

var (
	vpngateSupportedCiphers = map[string]bool{"AES-128-CBC": true, "AES-256-CBC": true, "AES-128-GCM": true, "AES-256-GCM": true}
	vpngateSupportedAuth    = map[string]bool{"SHA1": true, "SHA256": true, "SHA512": true}
	vpngateASNPattern       = regexp.MustCompile(`^AS[1-9][0-9]{0,9}$`)
	vpngateCountryPattern   = regexp.MustCompile(`^[A-Z]{2}$`)
)

// VPNGatePoolTag is the kernel outbound tag of a pool.
func VPNGatePoolTag(poolID int64) string {
	return fmt.Sprintf("vpngate-%d", poolID)
}

// VPNGateMemberID is the stable member identity of a catalog node.
func VPNGateMemberID(nodeID int64) string {
	return fmt.Sprintf("n%d", nodeID)
}

// VPNGateStepPoolID returns the pool referenced by a vpngate path step.
func VPNGateStepPoolID(step model.ProxyPathStep) (int64, bool) {
	if step.NodeType != model.ProxyPathStepVPNGate {
		return 0, false
	}
	var cfg struct {
		PoolID int64 `json:"vpngate_pool_id"`
	}
	if json.Unmarshal([]byte(step.ConfigJSON), &cfg) != nil || cfg.PoolID <= 0 {
		return 0, false
	}
	return cfg.PoolID, true
}

// NormalizeVPNGatePool validates operator input and fills defaults.
func NormalizeVPNGatePool(pool *model.VPNGatePool) error {
	pool.Name = strings.TrimSpace(pool.Name)
	if pool.Name == "" || len([]rune(pool.Name)) > vpngateNameMaxRunes {
		return errors.New("出口池名称不能为空，且不超过 64 个字符")
	}
	countries := map[string]bool{}
	normalizedCountries := []string{}
	for _, raw := range pool.Countries {
		code := strings.ToUpper(strings.TrimSpace(raw))
		if code == "" {
			continue
		}
		if !vpngateCountryPattern.MatchString(code) {
			return fmt.Errorf("国家/地区代码 %q 无效，应为两位字母", raw)
		}
		if !countries[code] {
			countries[code] = true
			normalizedCountries = append(normalizedCountries, code)
		}
	}
	if len(normalizedCountries) > vpngateMaxCountries {
		return fmt.Errorf("最多选择 %d 个国家/地区", vpngateMaxCountries)
	}
	sort.Strings(normalizedCountries)
	pool.Countries = normalizedCountries
	asns := map[string]bool{}
	normalizedASNs := []string{}
	for _, raw := range pool.OperatorASNs {
		asn := strings.ToUpper(strings.TrimSpace(raw))
		if asn == "" {
			continue
		}
		if !strings.HasPrefix(asn, "AS") {
			asn = "AS" + asn
		}
		if !vpngateASNPattern.MatchString(asn) {
			return fmt.Errorf("运营商 ASN %q 无效，例如 AS4713", raw)
		}
		if !asns[asn] {
			asns[asn] = true
			normalizedASNs = append(normalizedASNs, asn)
		}
	}
	if len(normalizedASNs) > vpngateMaxASNs {
		return fmt.Errorf("最多指定 %d 个运营商 ASN", vpngateMaxASNs)
	}
	sort.Strings(normalizedASNs)
	pool.OperatorASNs = normalizedASNs
	pool.Network = strings.ToLower(strings.TrimSpace(pool.Network))
	if pool.Network == "" {
		pool.Network = "any"
	}
	if pool.Network != "any" && pool.Network != "tcp" && pool.Network != "udp" {
		return errors.New("协议只能是 any、tcp 或 udp")
	}
	if pool.SelectMode == "" {
		pool.SelectMode = model.VPNGateSelectLatency
	}
	if pool.SelectMode != model.VPNGateSelectLatency && pool.SelectMode != model.VPNGateSelectQuality {
		return errors.New("选优策略只能是 latency（延迟优先）或 quality（质量优先）")
	}
	if pool.MaxRTTMS < 0 || pool.MaxRTTMS > 5000 {
		return errors.New("延迟上限应在 0-5000 毫秒之间（0 表示不限制）")
	}
	if pool.MinQualityScore < 0 || pool.MinQualityScore > 100 {
		return errors.New("质量下限应在 0-100 之间")
	}
	if pool.HotStandby < 0 || pool.HotStandby > VPNGateMaxHotStandby {
		return fmt.Errorf("热备数量应在 0-%d 之间", VPNGateMaxHotStandby)
	}
	if pool.PoolSize == 0 {
		pool.PoolSize = VPNGateDefaultPoolSize
	}
	if pool.PoolSize < 1 || pool.PoolSize > VPNGateMaxPoolSize {
		return fmt.Errorf("候选节点数应在 1-%d 之间", VPNGateMaxPoolSize)
	}
	if pool.PoolSize < pool.HotStandby+1 {
		return errors.New("候选节点数不能少于 1 + 热备数量")
	}
	if pool.SwitchPolicy == "" {
		pool.SwitchPolicy = model.VPNGateSwitchOnFailure
	}
	if pool.SwitchPolicy != model.VPNGateSwitchOnFailure && pool.SwitchPolicy != model.VPNGateSwitchImprove {
		return errors.New("切换策略只能是 on_failure（仅失效时切换）或 improve（发现更优时切换）")
	}
	if pool.RefreshMinutes == 0 {
		pool.RefreshMinutes = 30
	}
	if pool.RefreshMinutes < VPNGateMinRefreshMinutes || pool.RefreshMinutes > VPNGateMaxRefreshMinutes {
		return fmt.Errorf("刷新周期应在 %d-%d 分钟之间", VPNGateMinRefreshMinutes, VPNGateMaxRefreshMinutes)
	}
	return nil
}

// VPNGateCatalogEntry is one parsed and sanitized catalog row.
type VPNGateCatalogEntry struct {
	HostName    string
	IP          string
	Port        int
	Network     string
	CountryCode string
	CountryName string
	Score       int64
	PingMS      int
	SpeedBPS    int64
	Sessions    int
	UptimeMS    int64
	Operator    string
	Spec        model.OpenVPNSpec
}

// VPNGateNodeKey is the catalog identity of an endpoint.
func VPNGateNodeKey(ip string, port int, network string) string {
	return fmt.Sprintf("%s:%d/%s", ip, port, network)
}

// ParseVPNGateCatalog parses the VPNGate CSV API response. Rows that fail
// validation are skipped and counted; the downloaded data is untrusted and no
// configuration text is retained.
func ParseVPNGateCatalog(data []byte) ([]VPNGateCatalogEntry, int, error) {
	if len(data) == 0 || len(data) > VPNGateCatalogMaxBytes {
		return nil, 0, errors.New("VPNGate 目录为空或过大")
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var body strings.Builder
	headerSeen := false
	for _, line := range lines {
		if strings.HasPrefix(line, "*") || strings.TrimSpace(line) == "" {
			continue
		}
		if !headerSeen {
			if !strings.HasPrefix(line, "#HostName,") {
				return nil, 0, errors.New("VPNGate 目录格式无法识别")
			}
			line = strings.TrimPrefix(line, "#")
			headerSeen = true
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	if !headerSeen {
		return nil, 0, errors.New("VPNGate 目录缺少表头")
	}
	reader := csv.NewReader(strings.NewReader(body.String()))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	header, err := reader.Read()
	if err != nil {
		return nil, 0, errors.New("VPNGate 目录表头无效")
	}
	index := map[string]int{}
	for i, name := range header {
		index[strings.TrimSpace(name)] = i
	}
	for _, required := range []string{"HostName", "IP", "Score", "Ping", "Speed", "CountryLong", "CountryShort", "NumVpnSessions", "Uptime", "Operator", "OpenVPN_ConfigData_Base64"} {
		if _, ok := index[required]; !ok {
			return nil, 0, fmt.Errorf("VPNGate 目录缺少 %s 列", required)
		}
	}
	field := func(record []string, name string) string {
		i := index[name]
		if i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}
	var entries []VPNGateCatalogEntry
	skipped := 0
	seen := map[string]bool{}
	for rows := 0; ; rows++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			skipped++
			continue
		}
		if rows >= VPNGateCatalogMaxRows {
			break
		}
		raw, err := base64.StdEncoding.DecodeString(field(record, "OpenVPN_ConfigData_Base64"))
		if err != nil || len(raw) == 0 || len(raw) > vpngateConfigMaxBytes {
			skipped++
			continue
		}
		spec, err := SanitizeVPNGateOpenVPNConfig(string(raw))
		if err != nil {
			skipped++
			continue
		}
		listed, err := netip.ParseAddr(field(record, "IP"))
		if err != nil || listed.Unmap().String() != spec.Server {
			skipped++
			continue
		}
		country := strings.ToUpper(field(record, "CountryShort"))
		if !vpngateCountryPattern.MatchString(country) {
			skipped++
			continue
		}
		entry := VPNGateCatalogEntry{
			HostName:    boundedText(field(record, "HostName"), 128),
			IP:          spec.Server,
			Port:        int(spec.ServerPort),
			Network:     spec.Network,
			CountryCode: country,
			CountryName: boundedText(field(record, "CountryLong"), 64),
			Score:       parseInt64(field(record, "Score")),
			PingMS:      parseInt(field(record, "Ping")),
			SpeedBPS:    parseInt64(field(record, "Speed")),
			Sessions:    parseInt(field(record, "NumVpnSessions")),
			UptimeMS:    parseInt64(field(record, "Uptime")),
			Operator:    boundedText(field(record, "Operator"), 128),
			Spec:        spec,
		}
		key := VPNGateNodeKey(entry.IP, entry.Port, entry.Network)
		if seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, entry)
	}
	return entries, skipped, nil
}

func parseInt(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func parseInt64(raw string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func boundedText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

// SanitizeVPNGateOpenVPNConfig turns a downloaded .ovpn into an OpenVPN spec.
// Only a fixed allowlist of client directives is accepted; anything that can
// run code, change routing, load files, or that the kernel client does not
// implement makes the whole configuration unusable.
func SanitizeVPNGateOpenVPNConfig(text string) (model.OpenVPNSpec, error) {
	spec := model.OpenVPNSpec{MTU: VPNGateDefaultMTU}
	if len(text) > vpngateConfigMaxBytes {
		return spec, errors.New("配置过大")
	}
	text = strings.ReplaceAll(text, "\r", "")
	block := ""
	var blockBody strings.Builder
	blocks := map[string]string{}
	remotes := 0
	proto := ""
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if block != "" {
			if line == "</"+block+">" {
				if _, duplicate := blocks[block]; duplicate {
					return spec, fmt.Errorf("重复的 <%s>", block)
				}
				blocks[block] = blockBody.String()
				block = ""
				blockBody.Reset()
				continue
			}
			blockBody.WriteString(line)
			blockBody.WriteByte('\n')
			if blockBody.Len() > vpngatePEMMaxBytes {
				return spec, fmt.Errorf("<%s> 过大", block)
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "<") {
			name := strings.TrimSuffix(strings.TrimPrefix(line, "<"), ">")
			switch name {
			case "ca", "cert", "key":
				block = name
				continue
			default:
				return spec, fmt.Errorf("不支持的内嵌块 <%s>", name)
			}
		}
		fields := strings.Fields(line)
		directive := strings.ToLower(fields[0])
		args := fields[1:]
		switch directive {
		case "client", "nobind", "persist-key", "persist-tun", "float", "mute-replay-warnings":
		case "verb", "resolv-retry", "mute", "connect-retry", "connect-retry-max", "hand-window", "tls-timeout", "reneg-sec":
		case "dev":
			if len(args) != 1 || args[0] != "tun" {
				return spec, errors.New("只支持 dev tun")
			}
		case "proto":
			if len(args) != 1 {
				return spec, errors.New("proto 参数无效")
			}
			proto = args[0]
		case "remote":
			remotes++
			if remotes > 1 {
				return spec, errors.New("只支持一个 remote")
			}
			if len(args) < 2 || len(args) > 3 {
				return spec, errors.New("remote 参数无效")
			}
			addr, err := netip.ParseAddr(args[0])
			if err != nil || !addr.Unmap().Is4() || !vpngatePublicAddr(addr.Unmap()) {
				return spec, errors.New("remote 必须是公网 IPv4 地址")
			}
			port, err := strconv.Atoi(args[1])
			if err != nil || port < 1 || port > 65535 {
				return spec, errors.New("remote 端口无效")
			}
			spec.Server = addr.Unmap().String()
			spec.ServerPort = uint16(port)
			if len(args) == 3 {
				proto = args[2]
			}
		case "cipher":
			if len(args) != 1 {
				return spec, errors.New("cipher 参数无效")
			}
			spec.Cipher = strings.ToUpper(args[0])
		case "data-ciphers", "ncp-ciphers":
		case "auth":
			if len(args) != 1 {
				return spec, errors.New("auth 参数无效")
			}
			spec.Auth = strings.ToUpper(args[0])
		case "remote-cert-tls":
			if len(args) != 1 || args[0] != "server" {
				return spec, errors.New("remote-cert-tls 只支持 server")
			}
		case "auth-user-pass":
			if len(args) != 0 {
				return spec, errors.New("auth-user-pass 不能引用文件")
			}
		default:
			return spec, fmt.Errorf("不支持的指令 %s", directive)
		}
	}
	if block != "" {
		return spec, fmt.Errorf("<%s> 未闭合", block)
	}
	switch proto {
	case "tcp", "tcp-client", "tcp4", "tcp4-client":
		spec.Network = "tcp"
	case "udp", "udp4":
		spec.Network = "udp"
	default:
		return spec, errors.New("只支持 IPv4 的 tcp 或 udp")
	}
	if spec.Server == "" {
		return spec, errors.New("缺少 remote")
	}
	if !vpngateSupportedCiphers[spec.Cipher] {
		return spec, fmt.Errorf("不支持的加密算法 %q", spec.Cipher)
	}
	if spec.Auth == "" {
		spec.Auth = "SHA1"
	}
	if !vpngateSupportedAuth[spec.Auth] {
		return spec, fmt.Errorf("不支持的认证算法 %q", spec.Auth)
	}
	spec.CA, spec.Cert, spec.Key = blocks["ca"], blocks["cert"], blocks["key"]
	if !x509.NewCertPool().AppendCertsFromPEM([]byte(spec.CA)) {
		return spec, errors.New("缺少有效的 <ca>")
	}
	if _, err := tls.X509KeyPair([]byte(spec.Cert), []byte(spec.Key)); err != nil {
		return spec, errors.New("<cert> 与 <key> 不是有效的一对")
	}
	return spec, nil
}

func vpngatePublicAddr(addr netip.Addr) bool {
	return addr.IsGlobalUnicast() && !addr.IsPrivate() && !addr.IsLoopback() && !addr.IsLinkLocalUnicast()
}

// VPNGatePlanDigest is the content identity bound to a membership revision.
func VPNGatePlanDigest(hotStandby int, members []model.VPNGatePoolMember) string {
	encoded, _ := json.Marshal(struct {
		HotStandby int                       `json:"hot_standby"`
		Members    []model.VPNGatePoolMember `json:"members"`
	}{hotStandby, members})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// VPNGateQualityScore rates a candidate exit from 0 to 100. The weights are
// fixed and documented; operators set thresholds, not weights. A VPN flag is
// informational only: every VPNGate address is a public VPN.
func VPNGateQualityScore(quality *model.VPNGateIPQuality, successRate float64, node model.VPNGateNode) int {
	score := 50.0
	if quality != nil {
		if quality.Hosting != nil {
			if *quality.Hosting {
				score -= 25
			} else {
				score += 15
			}
		}
		if quality.Risk != nil {
			score -= float64(clampInt(*quality.Risk, 0, 100)) * 0.3
		}
		if quality.Abuse != nil {
			score -= float64(clampInt(*quality.Abuse, 0, 100)) * 0.3
		}
	}
	score += (successRate - 0.5) * 30
	if node.UptimeMS >= 24*60*60*1000 {
		score += 5
	}
	if node.Sessions > 200 {
		score -= 5
	}
	return clampInt(int(score+0.5), 0, 100)
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// VPNGateEligibility applies the hard filters. An empty reason means eligible.
func VPNGateEligibility(pool model.VPNGatePool, candidate model.VPNGateCandidate, quality *model.VPNGateIPQuality) string {
	if candidate.Status != "succeeded" {
		return "连接测试失败"
	}
	egress, err := netip.ParseAddr(candidate.EgressIP)
	if err != nil || !vpngatePublicAddr(egress.Unmap()) {
		return "出口 IP 无效"
	}
	if len(pool.Countries) > 0 {
		if !vpngateContains(pool.Countries, candidate.CountryCode) {
			return "国家/地区不符"
		}
		if quality != nil && quality.Country != "" && !vpngateContains(pool.Countries, strings.ToUpper(quality.Country)) {
			return "出口 IP 国家/地区不符"
		}
	}
	if len(pool.OperatorASNs) > 0 {
		if quality == nil || quality.ASN == "" {
			return "无法确认出口运营商"
		}
		if !vpngateContains(pool.OperatorASNs, strings.ToUpper(quality.ASN)) {
			return "运营商不符"
		}
	}
	if quality != nil && quality.Tor != nil && *quality.Tor {
		return "出口 IP 是 Tor 节点"
	}
	if pool.MaxRTTMS > 0 && candidate.RTTMS > int64(pool.MaxRTTMS) {
		return "延迟超过上限"
	}
	if pool.MinQualityScore > 0 && candidate.Quality < pool.MinQualityScore {
		return "质量分低于下限"
	}
	return ""
}

func vpngateContains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// RankVPNGateCandidates orders eligible candidates by the pool's mode.
func RankVPNGateCandidates(pool model.VPNGatePool, candidates []model.VPNGateCandidate) []model.VPNGateCandidate {
	ranked := make([]model.VPNGateCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Eligible {
			ranked = append(ranked, candidate)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if pool.SelectMode == model.VPNGateSelectQuality {
			if a.Quality != b.Quality {
				return a.Quality > b.Quality
			}
			if a.RTTMS != b.RTTMS {
				return a.RTTMS < b.RTTMS
			}
		} else {
			if a.RTTMS != b.RTTMS {
				return a.RTTMS < b.RTTMS
			}
			if a.Quality != b.Quality {
				return a.Quality > b.Quality
			}
		}
		return a.NodeID < b.NodeID
	})
	return ranked
}

// VPNGateSelection is the outcome of one selection round.
type VPNGateSelection struct {
	Members      []model.VPNGateCandidate
	Improvements int
	Reason       string
}

const vpngateImproveRounds = 2

// SelectVPNGateMembers chooses the ordered member list. The current primary
// is kept while it stays eligible; with switch policy "improve" a better
// candidate replaces it only after winning clearly for consecutive rounds, so
// long-lived connections and the visible exit IP do not flap.
func SelectVPNGateMembers(pool model.VPNGatePool, current []model.VPNGateCandidate, ranked []model.VPNGateCandidate, improvements int) VPNGateSelection {
	eligible := map[int64]model.VPNGateCandidate{}
	for _, candidate := range ranked {
		eligible[candidate.NodeID] = candidate
	}
	var primary *model.VPNGateCandidate
	if len(current) > 0 {
		if candidate, ok := eligible[current[0].NodeID]; ok {
			primary = &candidate
		}
	}
	result := VPNGateSelection{}
	if primary != nil && pool.SwitchPolicy == model.VPNGateSwitchImprove && len(ranked) > 0 && ranked[0].NodeID != primary.NodeID {
		if vpngateClearlyBetter(pool, ranked[0], *primary) {
			result.Improvements = improvements + 1
			if result.Improvements >= vpngateImproveRounds {
				primary = nil
				result.Improvements = 0
				result.Reason = "发现更好的节点，已切换"
			}
		}
	}
	if primary == nil && len(current) > 0 && result.Reason == "" {
		result.Reason = "当前出口失效，已切换"
	}
	used := map[int64]bool{}
	if primary != nil {
		result.Members = append(result.Members, *primary)
		used[primary.NodeID] = true
	}
	// Keep surviving standbys in their order before adding new ones.
	for _, member := range current {
		if len(result.Members) >= pool.PoolSize {
			break
		}
		if candidate, ok := eligible[member.NodeID]; ok && !used[member.NodeID] && pool.SwitchPolicy == model.VPNGateSwitchOnFailure {
			result.Members = append(result.Members, candidate)
			used[member.NodeID] = true
		}
	}
	for _, candidate := range ranked {
		if len(result.Members) >= pool.PoolSize {
			break
		}
		if used[candidate.NodeID] {
			continue
		}
		result.Members = append(result.Members, candidate)
		used[candidate.NodeID] = true
	}
	for i := range result.Members {
		result.Members[i].Selected = true
	}
	return result
}

func vpngateClearlyBetter(pool model.VPNGatePool, challenger, incumbent model.VPNGateCandidate) bool {
	if pool.SelectMode == model.VPNGateSelectQuality {
		return challenger.Quality >= incumbent.Quality+10
	}
	return incumbent.RTTMS > 0 && float64(challenger.RTTMS) <= float64(incumbent.RTTMS)*0.7
}

// MedianRTT returns the median of probe samples.
func MedianRTT(samples []int64) int64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]int64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

// SameVPNGateMembers reports whether two member lists name the same nodes in
// the same order.
func SameVPNGateMembers(a, b []model.VPNGateCandidate) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].NodeID != b[i].NodeID {
			return false
		}
	}
	return true
}
