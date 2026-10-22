package core

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func vpngateTestPEM(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "vpngate-test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

func vpngateTestConfig(t *testing.T, remote, proto string, extra ...string) string {
	cert, key := vpngateTestPEM(t)
	lines := []string{
		"###############################",
		"# OpenVPN 2.0 Sample Configuration File",
		"dev tun",
		"proto " + proto,
		"remote " + remote,
		"cipher AES-128-CBC",
		"data-ciphers AES-128-CBC",
		"auth SHA1",
		"resolv-retry infinite",
		"nobind",
		"persist-key",
		"persist-tun",
		"client",
		"verb 3",
		";http-proxy-retry",
	}
	lines = append(lines, extra...)
	lines = append(lines, "<ca>", strings.TrimSpace(cert), "</ca>", "<cert>", strings.TrimSpace(cert), "</cert>", "<key>", strings.TrimSpace(key), "</key>")
	return strings.Join(lines, "\r\n") + "\r\n"
}

func TestSanitizeVPNGateConfigAcceptsCatalogShape(t *testing.T) {
	spec, err := SanitizeVPNGateOpenVPNConfig(vpngateTestConfig(t, "219.100.37.14 443", "tcp"))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Server != "219.100.37.14" || spec.ServerPort != 443 || spec.Network != "tcp" || spec.Cipher != "AES-128-CBC" || spec.Auth != "SHA1" || spec.MTU != VPNGateDefaultMTU {
		t.Fatalf("unexpected spec %+v", spec)
	}
	if spec.Username != "" || spec.Password != "" {
		t.Fatal("credentials must not be invented")
	}
}

func TestSanitizeVPNGateConfigRejectsUnsafeDirectives(t *testing.T) {
	cases := map[string]string{
		"script":          vpngateTestConfig(t, "219.100.37.14 443", "tcp", "script-security 2", "up /bin/sh"),
		"plugin":          vpngateTestConfig(t, "219.100.37.14 443", "tcp", "plugin /tmp/x.so"),
		"route":           vpngateTestConfig(t, "219.100.37.14 443", "tcp", "route 10.0.0.0 255.0.0.0"),
		"redirect":        vpngateTestConfig(t, "219.100.37.14 443", "tcp", "redirect-gateway def1"),
		"auth file":       vpngateTestConfig(t, "219.100.37.14 443", "tcp", "auth-user-pass /etc/passwd"),
		"tls-auth block":  vpngateTestConfig(t, "219.100.37.14 443", "tcp", "<tls-auth>", "x", "</tls-auth>"),
		"domain remote":   vpngateTestConfig(t, "vpn.example.com 443", "tcp"),
		"private remote":  vpngateTestConfig(t, "10.1.2.3 443", "tcp"),
		"two remotes":     vpngateTestConfig(t, "219.100.37.14 443", "tcp", "remote 219.100.37.15 443"),
		"ipv6 proto":      vpngateTestConfig(t, "219.100.37.14 443", "tcp6"),
		"weak cipher":     strings.Replace(vpngateTestConfig(t, "219.100.37.14 443", "tcp"), "cipher AES-128-CBC", "cipher BF-CBC", 1),
		"compression":     vpngateTestConfig(t, "219.100.37.14 443", "tcp", "comp-lzo"),
		"dev tap":         strings.Replace(vpngateTestConfig(t, "219.100.37.14 443", "tcp"), "dev tun", "dev tap", 1),
		"unclosed block":  "dev tun\nproto tcp\nremote 219.100.37.14 443\ncipher AES-128-CBC\n<ca>\nabc\n",
		"missing key":     strings.Split(vpngateTestConfig(t, "219.100.37.14 443", "tcp"), "<key>")[0],
		"setenv override": vpngateTestConfig(t, "219.100.37.14 443", "tcp", "setenv FORWARD_COMPATIBLE 1"),
	}
	for name, config := range cases {
		if _, err := SanitizeVPNGateOpenVPNConfig(config); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestParseVPNGateCatalogSkipsInvalidRows(t *testing.T) {
	good := base64.StdEncoding.EncodeToString([]byte(vpngateTestConfig(t, "219.100.37.14 443", "tcp")))
	udp := base64.StdEncoding.EncodeToString([]byte(vpngateTestConfig(t, "1.33.22.11 1194", "udp")))
	mismatch := base64.StdEncoding.EncodeToString([]byte(vpngateTestConfig(t, "203.0.113.200 443", "tcp")))
	bad := base64.StdEncoding.EncodeToString([]byte(vpngateTestConfig(t, "219.100.37.15 443", "tcp", "up /bin/sh")))
	data := strings.Join([]string{
		"*vpn_servers",
		"#HostName,IP,Score,Ping,Speed,CountryLong,CountryShort,NumVpnSessions,Uptime,TotalUsers,TotalTraffic,LogType,Operator,Message,OpenVPN_ConfigData_Base64",
		"public-vpn-1,219.100.37.14,3099616,13,120069686,Japan,JP,192,1021765737,27713522,1740909648586130,2weeks,Daiyuu Nobori_ Japan. Academic Use Only.,," + good,
		"vpn2,1.33.22.11,1000,20,5000,Japan,JP,3,90000000,1,1,2weeks,someone,," + udp,
		"vpn3,219.100.37.99,1000,20,5000,Japan,JP,3,9,1,1,2weeks,someone,," + mismatch,
		"vpn4,219.100.37.15,1000,20,5000,Japan,JP,3,9,1,1,2weeks,someone,," + bad,
		"vpn5,219.100.37.16,1000,20,5000,Japan,JP,3,9,1,1,2weeks,someone,,not-base64!",
		"*",
	}, "\r\n")
	entries, skipped, err := ParseVPNGateCatalog([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || skipped != 3 {
		t.Fatalf("entries=%d skipped=%d", len(entries), skipped)
	}
	if entries[0].CountryCode != "JP" || entries[0].Sessions != 192 || entries[1].Network != "udp" || entries[1].Port != 1194 {
		t.Fatalf("unexpected entries %+v", entries)
	}
	if _, _, err := ParseVPNGateCatalog([]byte("<html>blocked</html>")); err == nil {
		t.Fatal("non-catalog response must be rejected")
	}
}

func TestParseVPNGateCatalogIntegerBounds(t *testing.T) {
	config := base64.StdEncoding.EncodeToString([]byte(vpngateTestConfig(t, "219.100.37.14 443", "tcp")))
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name, raw string
		want      int
	}{
		{"normal", "192", 192},
		{"whitespace", " 13 ", 13},
		{"maximum", strconv.Itoa(maxInt), maxInt},
		{"overflow", strconv.FormatUint(uint64(maxInt)+1, 10), 0},
		{"huge", "18446744073709551616", 0},
		{"negative", "-1", 0},
		{"invalid", "unknown", 0},
		{"empty", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := "#HostName,IP,Score,Ping,Speed,CountryLong,CountryShort,NumVpnSessions,Uptime,Operator,OpenVPN_ConfigData_Base64\n" +
				"vpn,219.100.37.14,1," + tc.raw + ",1,Japan,JP," + tc.raw + ",1,operator," + config + "\n"
			entries, skipped, err := ParseVPNGateCatalog([]byte(data))
			if err != nil || skipped != 0 || len(entries) != 1 {
				t.Fatalf("entries=%d skipped=%d err=%v", len(entries), skipped, err)
			}
			if entries[0].PingMS != tc.want || entries[0].Sessions != tc.want {
				t.Fatalf("ping=%d sessions=%d want=%d", entries[0].PingMS, entries[0].Sessions, tc.want)
			}
		})
	}
}

func TestNormalizeVPNGatePool(t *testing.T) {
	pool := model.VPNGatePool{Name: " 日本家宽 ", Countries: []string{"jp", "JP", " kr"}, OperatorASNs: []string{"4713", "as2516", "AS4713"}}
	if err := NormalizeVPNGatePool(&pool); err != nil {
		t.Fatal(err)
	}
	if strings.Join(pool.Countries, ",") != "JP,KR" || strings.Join(pool.OperatorASNs, ",") != "AS2516,AS4713" {
		t.Fatalf("normalization: %+v", pool)
	}
	if pool.SelectMode != model.VPNGateSelectLatency || pool.SwitchPolicy != model.VPNGateSwitchOnFailure || pool.PoolSize != VPNGateDefaultPoolSize || pool.RefreshMinutes != 30 || pool.Network != "any" {
		t.Fatalf("defaults: %+v", pool)
	}
	for name, mutate := range map[string]func(*model.VPNGatePool){
		"empty name":   func(p *model.VPNGatePool) { p.Name = "" },
		"bad country":  func(p *model.VPNGatePool) { p.Countries = []string{"JPN"} },
		"bad asn":      func(p *model.VPNGatePool) { p.OperatorASNs = []string{"NTT"} },
		"bad mode":     func(p *model.VPNGatePool) { p.SelectMode = "random" },
		"pool too big": func(p *model.VPNGatePool) { p.PoolSize = 9 },
		"standby":      func(p *model.VPNGatePool) { p.HotStandby = 2; p.PoolSize = 2 },
		"refresh":      func(p *model.VPNGatePool) { p.RefreshMinutes = 5 },
	} {
		candidate := model.VPNGatePool{Name: "x"}
		mutate(&candidate)
		if err := NormalizeVPNGatePool(&candidate); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }

func TestVPNGateEligibilityIsFailClosedOnOperator(t *testing.T) {
	pool := model.VPNGatePool{Countries: []string{"JP"}, OperatorASNs: []string{"AS4713"}}
	candidate := model.VPNGateCandidate{Status: "succeeded", EgressIP: "153.1.2.3", CountryCode: "JP", RTTMS: 100}
	if reason := VPNGateEligibility(pool, candidate, nil); reason == "" {
		t.Fatal("unknown operator must not be eligible when an operator filter is set")
	}
	if reason := VPNGateEligibility(pool, candidate, &model.VPNGateIPQuality{ASN: "AS2516", Country: "JP"}); reason == "" {
		t.Fatal("other operator must not be eligible")
	}
	if reason := VPNGateEligibility(pool, candidate, &model.VPNGateIPQuality{ASN: "AS4713", Country: "US"}); reason == "" {
		t.Fatal("egress country mismatch must not be eligible")
	}
	if reason := VPNGateEligibility(pool, candidate, &model.VPNGateIPQuality{ASN: "AS4713", Country: "JP"}); reason != "" {
		t.Fatalf("matching candidate rejected: %s", reason)
	}
	candidate.Status = "failed"
	if reason := VPNGateEligibility(pool, candidate, &model.VPNGateIPQuality{ASN: "AS4713", Country: "JP"}); reason == "" {
		t.Fatal("failed probe must not be eligible")
	}
}

func TestVPNGateQualityScore(t *testing.T) {
	residential := &model.VPNGateIPQuality{Hosting: boolPtr(false), Risk: intPtr(0), Abuse: intPtr(0)}
	hosting := &model.VPNGateIPQuality{Hosting: boolPtr(true), Risk: intPtr(60), Abuse: intPtr(40)}
	good := VPNGateQualityScore(residential, 1, model.VPNGateNode{UptimeMS: 48 * 3600 * 1000})
	bad := VPNGateQualityScore(hosting, 0, model.VPNGateNode{Sessions: 500})
	if good <= 80 || bad >= 20 || good > 100 || bad < 0 {
		t.Fatalf("good=%d bad=%d", good, bad)
	}
}

func vpngateCandidate(id int64, rtt int64, quality int) model.VPNGateCandidate {
	return model.VPNGateCandidate{NodeID: id, MemberID: fmt.Sprintf("n%d", id), RTTMS: rtt, Quality: quality, Eligible: true, Status: "succeeded"}
}

func TestSelectVPNGateMembersKeepsPrimaryOnFailurePolicy(t *testing.T) {
	pool := model.VPNGatePool{SelectMode: model.VPNGateSelectLatency, SwitchPolicy: model.VPNGateSwitchOnFailure, PoolSize: 3}
	current := []model.VPNGateCandidate{vpngateCandidate(1, 200, 50), vpngateCandidate(2, 220, 50)}
	ranked := RankVPNGateCandidates(pool, []model.VPNGateCandidate{vpngateCandidate(3, 50, 50), vpngateCandidate(1, 200, 50), vpngateCandidate(2, 220, 50), vpngateCandidate(4, 60, 50)})
	selection := SelectVPNGateMembers(pool, current, ranked, 0)
	if selection.Members[0].NodeID != 1 || selection.Members[1].NodeID != 2 || selection.Members[2].NodeID != 3 || len(selection.Members) != 3 {
		t.Fatalf("on_failure must keep current members: %+v", selection.Members)
	}
	// Primary disappears: the surviving standby, which the kernel already
	// moved traffic to, stays first; the best new candidate fills in behind it.
	ranked = RankVPNGateCandidates(pool, []model.VPNGateCandidate{vpngateCandidate(3, 50, 50), vpngateCandidate(2, 220, 50)})
	selection = SelectVPNGateMembers(pool, current, ranked, 0)
	if selection.Members[0].NodeID != 2 || selection.Members[1].NodeID != 3 || selection.Reason == "" {
		t.Fatalf("failed primary must hand over to the surviving standby: %+v", selection)
	}
	// Nothing survives: the best ranked candidate takes over.
	ranked = RankVPNGateCandidates(pool, []model.VPNGateCandidate{vpngateCandidate(3, 50, 50)})
	selection = SelectVPNGateMembers(pool, current, ranked, 0)
	if len(selection.Members) != 1 || selection.Members[0].NodeID != 3 {
		t.Fatalf("best candidate must take over: %+v", selection)
	}
}

func TestSelectVPNGateMembersImprovePolicyNeedsConsecutiveWins(t *testing.T) {
	pool := model.VPNGatePool{SelectMode: model.VPNGateSelectLatency, SwitchPolicy: model.VPNGateSwitchImprove, PoolSize: 2}
	current := []model.VPNGateCandidate{vpngateCandidate(1, 200, 50)}
	ranked := RankVPNGateCandidates(pool, []model.VPNGateCandidate{vpngateCandidate(1, 200, 50), vpngateCandidate(2, 100, 50)})
	first := SelectVPNGateMembers(pool, current, ranked, 0)
	if first.Members[0].NodeID != 1 || first.Improvements != 1 {
		t.Fatalf("first win must not switch yet: %+v", first)
	}
	second := SelectVPNGateMembers(pool, current, ranked, first.Improvements)
	if second.Members[0].NodeID != 2 {
		t.Fatalf("second consecutive win must switch: %+v", second)
	}
	// A marginal improvement never switches.
	ranked = RankVPNGateCandidates(pool, []model.VPNGateCandidate{vpngateCandidate(1, 200, 50), vpngateCandidate(2, 190, 50)})
	marginal := SelectVPNGateMembers(pool, current, ranked, 5)
	if marginal.Members[0].NodeID != 1 || marginal.Improvements != 0 {
		t.Fatalf("marginal improvement must not count: %+v", marginal)
	}
}

func TestRankVPNGateCandidatesByMode(t *testing.T) {
	candidates := []model.VPNGateCandidate{vpngateCandidate(1, 100, 40), vpngateCandidate(2, 300, 90), vpngateCandidate(3, 80, 10)}
	candidates = append(candidates, model.VPNGateCandidate{NodeID: 4, RTTMS: 1, Quality: 100})
	latency := RankVPNGateCandidates(model.VPNGatePool{SelectMode: model.VPNGateSelectLatency}, candidates)
	quality := RankVPNGateCandidates(model.VPNGatePool{SelectMode: model.VPNGateSelectQuality}, candidates)
	if len(latency) != 3 || latency[0].NodeID != 3 || quality[0].NodeID != 2 {
		t.Fatalf("latency=%v quality=%v", latency, quality)
	}
}

func TestVPNGateStepPoolID(t *testing.T) {
	step := model.ProxyPathStep{NodeType: model.ProxyPathStepVPNGate, ConfigJSON: `{"vpngate_pool_id":12}`}
	if id, ok := VPNGateStepPoolID(step); !ok || id != 12 {
		t.Fatalf("got %d %v", id, ok)
	}
	if _, ok := VPNGateStepPoolID(model.ProxyPathStep{NodeType: model.ProxyPathStepVPNGate, ConfigJSON: `{}`}); ok {
		t.Fatal("missing pool id must not resolve")
	}
}

func TestVPNGateStepGeneratesFailClosedPoolOutbound(t *testing.T) {
	server := model.Server{ID: 1, Name: "edge", AgentID: "agent", KernelCapabilities: []string{"vpngate_pool_v1"}, PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackPreferIPv4, PortRangeStart: 30000, PortRangeEnd: 30100}
	root := model.Inbound{ID: 10, ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 50, Kind: model.ProxyPathKindChain, Name: "vpngate-exit", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	step := model.ProxyPathStep{ID: 201, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepVPNGate, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{"vpngate_pool_id":7}`}
	user := model.User{ID: 1, Username: "alice", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass-a"}
	opts := ConfigOptions{
		Servers: []model.Server{server}, Inbounds: []model.Inbound{root}, ProxyPaths: []model.ProxyPath{path},
		ProxyPathSteps: []model.ProxyPathStep{step},
		InboundUsers:   []model.InboundUser{{InboundID: root.ID, UserID: user.ID, Enabled: true}},
	}
	config := mustServerConfig(t, server, []model.Inbound{root}, []model.User{user}, opts)
	pool := findOutbound(config, "vpngate-7")
	if pool["type"] != "vpngate-pool" || pool["pool_id"] == nil {
		t.Fatalf("pool outbound missing: %s", config)
	}
	for _, field := range []string{"members", "server", "hot_standby", "detour"} {
		if _, ok := pool[field]; ok {
			t.Fatalf("pool outbound must not carry %s: %#v", field, pool)
		}
	}
	if !strings.Contains(config, `"outbound": "vpngate-7"`) {
		t.Fatalf("path traffic does not route to the pool: %s", config)
	}
	if strings.Contains(config, `"vpngate-7","direct"`) {
		t.Fatalf("pool must not have a direct fallback: %s", config)
	}

	old := server
	old.KernelCapabilities = nil
	if _, err := GenerateServerConfigWithOptions(old, []model.Inbound{root}, nil, nil, []model.User{user}, opts); err == nil {
		t.Fatal("kernels without vpngate_pool_v1 must be refused")
	}
	wrong := opts
	wrong.ProxyPathSteps = []model.ProxyPathStep{{ID: 1, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &server.ID}, {ID: 2, PathID: path.ID, Position: 2, NodeType: model.ProxyPathStepVPNGate, ConfigJSON: `{"vpngate_pool_id":7}`}, {ID: 3, PathID: path.ID, Position: 3, NodeType: model.ProxyPathStepWARP}}
	if _, err := BuildProxyPathPlans(wrong.ProxyPaths, wrong.ProxyPathSteps, wrong.Servers, wrong.Inbounds); err == nil {
		t.Fatal("VPNGate must be the last step")
	}
}
