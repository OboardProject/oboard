package core

import (
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	DefaultProxyPathChainMethod            = "2022-blake3-aes-128-gcm"
	DefaultProxyPathRealityHandshakeServer = "cdn.icloud-content.com"
	DefaultProxyPathRealityHandshakePort   = 443
)

var proxyPathChainMethods = map[string]int{
	"2022-blake3-aes-128-gcm":       1,
	"2022-blake3-aes-256-gcm":       2,
	"2022-blake3-chacha20-poly1305": 3,
}

// ProxyPathPortLedger holds the persisted generated-listener ports and records
// the ones a projection had to allocate. A projection prefers a stored port so
// that an unrelated change — a new inbound, a disabled branch, another path
// claiming a nearby port — cannot move a listener that is already deployed.
//
// The ledger is read-only with respect to the database. Controller persists
// Pending() after a projection succeeds, so a rejected deployment leaves no
// half-claimed ports behind.
type ProxyPathPortLedger struct {
	stored   map[proxyPathPortKey]int
	pending  map[proxyPathPortKey]int
	used     map[proxyPathPortKey]bool
	order    []proxyPathPortKey
	complete bool
}

type proxyPathPortKey struct {
	Kind     string
	ScopeKey string
	ServerID int64
}

// NewProxyPathPortLedger builds a ledger from persisted allocations. A nil or
// empty slice yields a ledger that allocates everything fresh, which is also the
// behavior pure-Core callers and fixtures get.
func NewProxyPathPortLedger(allocations []model.ProxyPathPortAllocation) *ProxyPathPortLedger {
	ledger := &ProxyPathPortLedger{stored: make(map[proxyPathPortKey]int, len(allocations)), pending: map[proxyPathPortKey]int{}}
	for _, item := range allocations {
		if item.Port > 0 {
			ledger.stored[proxyPathPortKey{Kind: item.Kind, ScopeKey: item.ScopeKey, ServerID: item.ServerID}] = item.Port
		}
	}
	return ledger
}

// resolve returns the port for one listener, allocating through allocate only
// when neither a stored nor an already-pending value exists.
func (l *ProxyPathPortLedger) resolve(kind, scopeKey string, serverID int64, allocate func() int) int {
	if l == nil {
		return allocate()
	}
	key := proxyPathPortKey{Kind: kind, ScopeKey: scopeKey, ServerID: serverID}
	if port, ok := l.stored[key]; ok {
		if l.used == nil {
			l.used = map[proxyPathPortKey]bool{}
		}
		l.used[key] = true
		return port
	}
	if port, ok := l.pending[key]; ok {
		return port
	}
	port := allocate()
	if port <= 0 {
		return 0
	}
	l.pending[key] = port
	l.order = append(l.order, key)
	return port
}

// markProjectionComplete records that a projection enumerated every enabled path
// without aborting, so the set of resolved listeners is authoritative.
func (l *ProxyPathPortLedger) markProjectionComplete() {
	if l != nil {
		l.complete = true
	}
}

// StaleProxyPathPortAllocationIDs reports the persisted records no listener
// claims anymore, so Controller can release those ports for future allocation.
//
// Releasing requires a complete projection. A run that aborted partway resolved
// only some listeners, and treating that partial view as authoritative would drop
// allocations that are still deployed.
func StaleProxyPathPortAllocationIDs(stored []model.ProxyPathPortAllocation, ledger *ProxyPathPortLedger) []int64 {
	if ledger == nil || !ledger.complete {
		return nil
	}
	out := []int64{}
	for _, item := range stored {
		key := proxyPathPortKey{Kind: item.Kind, ScopeKey: item.ScopeKey, ServerID: item.ServerID}
		if ledger.used[key] || ledger.pending[key] > 0 {
			continue
		}
		out = append(out, item.ID)
	}
	return out
}

// Pending returns the allocations this projection created, in allocation order.
func (l *ProxyPathPortLedger) Pending() []model.ProxyPathPortAllocation {
	if l == nil {
		return nil
	}
	out := make([]model.ProxyPathPortAllocation, 0, len(l.order))
	for _, key := range l.order {
		out = append(out, model.ProxyPathPortAllocation{Kind: key.Kind, ScopeKey: key.ScopeKey, ServerID: key.ServerID, Port: l.pending[key]})
	}
	return out
}

type proxyPathChainServiceKey struct {
	ServerID int64
	Protocol model.Protocol
	Profile  string
}

type proxyPathChainService struct {
	Key         proxyPathChainServiceKey
	ChainConfig ProxyPathChainConfig
	Inbound     model.Inbound
	Tag         string
	Users       []model.User
}

type ProxyPathChainConfig struct {
	Protocol               model.Protocol
	Method                 string
	RealityHandshakeServer string
	RealityHandshakePort   int
}

func ValidateProxyPathChainMethod(method string) error {
	method = normalizeProxyPathChainMethod(method)
	if _, ok := proxyPathChainMethods[method]; !ok {
		return fmt.Errorf("unsupported proxy path Shadowsocks method %q", method)
	}
	return nil
}

func normalizeProxyPathChainMethod(method string) string {
	method = strings.ToLower(strings.TrimSpace(method))
	if method == "" {
		return DefaultProxyPathChainMethod
	}
	return method
}

func proxyPathStepChainMethod(step model.ProxyPathStep) string {
	return normalizeProxyPathChainMethod(stringValue(parseStepConfig(step.ConfigJSON), "chain_method", ""))
}

func ParseProxyPathChainConfig(raw string) (ProxyPathChainConfig, error) {
	cfg := parseStepConfig(raw)
	protocol := model.Protocol(strings.ToLower(strings.TrimSpace(stringValue(cfg, "chain_protocol", string(model.ProtocolSS)))))
	if protocol == "" {
		protocol = model.ProtocolSS
	}
	out := ProxyPathChainConfig{Protocol: protocol}
	switch protocol {
	case model.ProtocolSS:
		out.Method = normalizeProxyPathChainMethod(stringValue(cfg, "chain_method", ""))
		if err := ValidateProxyPathChainMethod(out.Method); err != nil {
			return ProxyPathChainConfig{}, err
		}
	case model.ProtocolVLESS:
		out.RealityHandshakeServer = strings.ToLower(strings.TrimSpace(stringValue(cfg, "reality_handshake_server", DefaultProxyPathRealityHandshakeServer)))
		out.RealityHandshakePort = intValueFromMap(cfg, "reality_handshake_port", DefaultProxyPathRealityHandshakePort)
		if err := ValidateSafeHost(out.RealityHandshakeServer); err != nil {
			return ProxyPathChainConfig{}, fmt.Errorf("Reality handshake server: %w", err)
		}
		if err := ValidatePort(out.RealityHandshakePort); err != nil {
			return ProxyPathChainConfig{}, fmt.Errorf("Reality handshake port: %w", err)
		}
	case model.ProtocolMieru:
	default:
		return ProxyPathChainConfig{}, fmt.Errorf("unsupported generated proxy path protocol %q", protocol)
	}
	return out, nil
}

func proxyPathStepChainConfig(step model.ProxyPathStep) (ProxyPathChainConfig, error) {
	return ParseProxyPathChainConfig(step.ConfigJSON)
}

func (c ProxyPathChainConfig) profile() string {
	switch c.Protocol {
	case model.ProtocolVLESS:
		return fmt.Sprintf("reality:%s:%d", c.RealityHandshakeServer, c.RealityHandshakePort)
	case model.ProtocolMieru:
		return "tcp"
	default:
		return c.Method
	}
}

func proxyPathChainServiceKeyForStep(step model.ProxyPathStep, serverID int64) (proxyPathChainServiceKey, error) {
	cfg, err := proxyPathStepChainConfig(step)
	if err != nil {
		return proxyPathChainServiceKey{}, err
	}
	return proxyPathChainServiceKey{ServerID: serverID, Protocol: cfg.Protocol, Profile: cfg.profile()}, nil
}

func buildProxyPathChainServices(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, ledger *ProxyPathPortLedger) (map[proxyPathChainServiceKey]*proxyPathChainService, error) {
	serverByID := make(map[int64]model.Server, len(servers))
	for _, server := range servers {
		serverByID[server.ID] = server
	}
	pathByID := make(map[int64]model.ProxyPath, len(paths))
	for _, path := range paths {
		pathByID[path.ID] = path
	}
	orderedSteps := append([]model.ProxyPathStep(nil), steps...)
	sort.SliceStable(orderedSteps, func(i, j int) bool {
		if orderedSteps[i].PathID == orderedSteps[j].PathID {
			if orderedSteps[i].Position == orderedSteps[j].Position {
				return orderedSteps[i].ID < orderedSteps[j].ID
			}
			return orderedSteps[i].Position < orderedSteps[j].Position
		}
		return orderedSteps[i].PathID < orderedSteps[j].PathID
	})
	services := map[proxyPathChainServiceKey]*proxyPathChainService{}
	for _, step := range orderedSteps {
		path, ok := pathByID[step.PathID]
		if !ok || !path.Enabled || step.NodeType != model.ProxyPathStepServerInbound || (step.InboundID != nil && *step.InboundID != 0) {
			continue
		}
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if mode == model.ProxyPathTransportPortForward || step.ServerID == nil || *step.ServerID == 0 {
			continue
		}
		chainConfig, err := proxyPathStepChainConfig(step)
		if err != nil {
			return nil, fmt.Errorf("proxy path %s step %d: %w", path.Name, step.Position, err)
		}
		server, ok := serverByID[*step.ServerID]
		if !ok {
			return nil, fmt.Errorf("proxy path %s step %d: target server not found", path.Name, step.Position)
		}
		key := proxyPathChainServiceKey{ServerID: server.ID, Protocol: chainConfig.Protocol, Profile: chainConfig.profile()}
		service := services[key]
		if service == nil {
			service = &proxyPathChainService{Key: key, ChainConfig: chainConfig}
			services[key] = service
		}
		service.Users = append(service.Users, proxyPathInternalUser(path, step))
	}

	occupied := make(map[int64]model.Inbound, len(inbounds)+len(services))
	for _, inbound := range inbounds {
		occupied[inbound.ID] = inbound
	}
	keys := make([]proxyPathChainServiceKey, 0, len(services))
	for key := range services {
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].ServerID == keys[j].ServerID {
			if keys[i].Protocol == keys[j].Protocol {
				return keys[i].Profile < keys[j].Profile
			}
			return keys[i].Protocol < keys[j].Protocol
		}
		return keys[i].ServerID < keys[j].ServerID
	})
	for _, key := range keys {
		service := services[key]
		server := serverByID[key.ServerID]
		seed := int(stableProxyPathResourceID("proxy-path-chain-service", key.Protocol, key.Profile) % 1000000)
		start, end := proxyPathServerPortRange(server)
		portProtocol := model.ForwardProtocolTCP
		if key.Protocol == model.ProtocolSS {
			portProtocol = model.ForwardProtocolTCPUDP
		}
		port := ledger.resolve(model.ProxyPathPortKindChainService, proxyPathChainServiceScopeKey(key), server.ID, func() int {
			return proxyPathAvailablePortForProtocol(server, server.ID*977, seed, start, end, portProtocol, true, occupied)
		})
		if port == 0 {
			return nil, fmt.Errorf("server %s has no available port for shared %s chain service", server.Name, key.Protocol)
		}
		service.Tag = proxyPathChainServiceTag(key)
		configJSON, err := proxyPathChainServiceConfigJSON(server, key, service.ChainConfig)
		if err != nil {
			return nil, err
		}
		service.Inbound = model.Inbound{
			ID:         proxyPathChainServiceID(key),
			ServerID:   key.ServerID,
			Name:       fmt.Sprintf("共享链路 / %s", proxyPathChainServiceLabel(key)),
			Protocol:   key.Protocol,
			ListenIP:   firstNonEmpty(server.ListenIP, "0.0.0.0"),
			Port:       port,
			ConfigJSON: string(configJSON),
			Enabled:    true,
		}
		occupied[service.Inbound.ID] = service.Inbound
	}
	return services, nil
}

func proxyPathChainServiceForStep(services map[proxyPathChainServiceKey]*proxyPathChainService, step model.ProxyPathStep, targetServerID int64) (*proxyPathChainService, bool) {
	if step.InboundID != nil && *step.InboundID != 0 {
		return nil, false
	}
	mode := step.TransportMode
	if mode == "" {
		mode = model.ProxyPathTransportSingBox
	}
	if mode == model.ProxyPathTransportPortForward {
		return nil, false
	}
	key, err := proxyPathChainServiceKeyForStep(step, targetServerID)
	if err != nil {
		return nil, false
	}
	service, ok := services[key]
	return service, ok
}

func proxyPathChainServiceScopeKey(key proxyPathChainServiceKey) string {
	if key.Protocol == model.ProtocolSS {
		return key.Profile
	}
	return string(key.Protocol) + ":" + key.Profile
}

func proxyPathChainServiceTag(key proxyPathChainServiceKey) string {
	if key.Protocol == model.ProtocolSS {
		return "oboard-chain-ss-" + strings.NewReplacer("2022-blake3-", "", "-gcm", "", "-poly1305", "").Replace(key.Profile) + "-in"
	}
	sum := sha256.Sum256([]byte(key.Profile))
	return fmt.Sprintf("oboard-chain-%s-%s-in", key.Protocol, hex.EncodeToString(sum[:4]))
}

func proxyPathChainServiceID(key proxyPathChainServiceKey) int64 {
	if key.Protocol == model.ProtocolSS {
		return -(int64(1)<<41 + (key.ServerID&0xffffff)<<4 + int64(proxyPathChainMethods[normalizeProxyPathChainMethod(key.Profile)])&0xf)
	}
	return -stableProxyPathResourceID("proxy-path-chain-inbound", key.ServerID, key.Protocol, key.Profile)
}

func proxyPathChainServiceLabel(key proxyPathChainServiceKey) string {
	switch key.Protocol {
	case model.ProtocolVLESS:
		return "VLESS Reality"
	case model.ProtocolMieru:
		return "Mieru TCP"
	default:
		return key.Profile
	}
}

func proxyPathChainServicePassword(server model.Server, method string) string {
	seed := proxyPathServerChainSeed(server)
	sum := sha256.Sum256([]byte("oboard-chain-ss:" + seed + ":" + normalizeProxyPathChainMethod(method)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func proxyPathChainServiceConfigJSON(server model.Server, key proxyPathChainServiceKey, chainConfig ProxyPathChainConfig) (string, error) {
	var cfg map[string]any
	switch key.Protocol {
	case model.ProtocolSS:
		cfg = map[string]any{"method": key.Profile, "password": proxyPathChainServicePassword(server, key.Profile)}
	case model.ProtocolMieru:
		cfg = map[string]any{"transport": "TCP", "multiplexing": "MULTIPLEXING_DEFAULT", "user_hint_is_mandatory": true}
	case model.ProtocolVLESS:
		privateSeed := sha256.Sum256([]byte("oboard-chain-vless-private:" + proxyPathServerChainSeed(server) + ":" + key.Profile))
		privateKey, err := ecdh.X25519().NewPrivateKey(privateSeed[:])
		if err != nil {
			return "", err
		}
		shortSeed := sha256.Sum256([]byte("oboard-chain-vless-short-id:" + proxyPathServerChainSeed(server) + ":" + key.Profile))
		cfg = map[string]any{
			"flow": "xtls-rprx-vision",
			"tls": map[string]any{
				"enabled":     true,
				"server_name": chainConfig.RealityHandshakeServer,
				"reality": map[string]any{
					"enabled":     true,
					"private_key": base64.RawURLEncoding.EncodeToString(privateKey.Bytes()),
					"public_key":  base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()),
					"short_id":    hex.EncodeToString(shortSeed[:4]),
					"handshake": map[string]any{
						"server":      chainConfig.RealityHandshakeServer,
						"server_port": chainConfig.RealityHandshakePort,
					},
				},
			},
		}
	default:
		return "", fmt.Errorf("unsupported generated proxy path protocol %q", key.Protocol)
	}
	b, err := json.Marshal(cfg)
	return string(b), err
}

func stableProxyPathResourceID(kind string, values ...any) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(kind))
	for _, value := range values {
		_, _ = h.Write([]byte(fmt.Sprintf("|%v", value)))
	}
	return 850000000000 + int64(h.Sum64()%40000000000)
}

func BuildProxyPathPlans(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound) ([]model.ProxyPathPlan, error) {
	plans, _, err := buildProxyPathPlansWithInbounds(paths, steps, servers, inbounds, nil)
	return plans, err
}

// BuildProxyPathPlansWithLedger projects the paths while preferring persisted
// generated-listener ports. Newly allocated ports are recorded on the ledger for
// the caller to persist once the deployment is accepted.
func BuildProxyPathPlansWithLedger(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, ledger *ProxyPathPortLedger) ([]model.ProxyPathPlan, error) {
	plans, _, err := buildProxyPathPlansWithInbounds(paths, steps, servers, inbounds, ledger)
	return plans, err
}

// buildProxyPathPlansWithInbounds also returns the synthetic inbound table the
// projection allocated, keyed by inbound ID. Config generation must reuse this
// table instead of recomputing ports: the allocator picks the first free port
// from a seed, so a different occupancy set would silently yield a different
// port and the derived forward would target a listener nobody owns.
func buildProxyPathPlansWithInbounds(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, ledger *ProxyPathPortLedger) ([]model.ProxyPathPlan, map[int64]model.Inbound, error) {
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	serverByID := map[int64]model.Server{}
	for _, server := range servers {
		serverByID[server.ID] = server
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	if err := validateProxyPathTransportSet(paths, stepsByPath, inboundByID); err != nil {
		return nil, nil, err
	}
	transparentGroups := buildTransparentProxyPathGroups(paths, stepsByPath)
	chainServices, err := buildProxyPathChainServices(paths, steps, servers, inbounds, ledger)
	if err != nil {
		return nil, nil, err
	}
	for _, service := range chainServices {
		inboundByID[service.Inbound.ID] = service.Inbound
	}
	sharedTunnels := map[string]model.Tunnel{}
	orderedPaths := append([]model.ProxyPath(nil), paths...)
	sort.SliceStable(orderedPaths, func(i, j int) bool { return orderedPaths[i].ID < orderedPaths[j].ID })
	out := []model.ProxyPathPlan{}
	for _, path := range orderedPaths {
		pathSteps := append([]model.ProxyPathStep(nil), stepsByPath[path.ID]...)
		sort.SliceStable(pathSteps, func(i, j int) bool {
			if pathSteps[i].Position == pathSteps[j].Position {
				return pathSteps[i].ID < pathSteps[j].ID
			}
			return pathSteps[i].Position < pathSteps[j].Position
		})
		plan := model.ProxyPathPlan{PathID: path.ID, Name: path.Name, InboundID: path.InboundID, Enabled: path.Enabled}
		root, ok := inboundByID[path.InboundID]
		if !ok {
			if path.Enabled {
				return nil, nil, fmt.Errorf("代理路径 %s 的入口不存在", path.Name)
			}
			plan.Warnings = append(plan.Warnings, "入口不存在")
			out = append(out, plan)
			continue
		}
		previousServerID := root.ServerID
		sourceListenPort := root.Port
		processingCount := 0
		for _, step := range pathSteps {
			mode := step.TransportMode
			if mode == "" {
				mode = model.ProxyPathTransportSingBox
			}
			plan.Steps = append(plan.Steps, model.ProxyPathPlanStep{ID: step.ID, Position: step.Position, NodeType: step.NodeType, TransportMode: mode, ProcessingRole: step.ProcessingRole, ServerID: step.ServerID, InboundID: step.InboundID, ExternalOutboundID: step.ExternalOutboundID})
			planStepIndex := len(plan.Steps) - 1
			if step.ProcessingRole {
				processingCount++
			}
			targetServerID, targetInbound, targetOK := proxyPathStepTargetServer(step, inboundByID)
			var plannedInbound model.Inbound
			if targetOK {
				plannedInbound = proxyPathPlanTargetInbound(path, step, targetServerID, targetInbound, serverByID, inboundByID, chainServices, transparentGroups[path.ID], ledger)
				if step.TransportMode == model.ProxyPathTransportPortForward && step.ProcessingRole {
					plannedInbound.Protocol = root.Protocol
					plannedInbound.ConfigJSON = root.ConfigJSON
				}
				// Only an enabled path reserves runtime ports. A disabled branch
				// deploys nothing, so letting it occupy a port would make enabling or
				// disabling it silently change the allocation of unrelated paths.
				if path.Enabled {
					inboundByID[plannedInbound.ID] = plannedInbound
				}
				if path.Enabled && mode == model.ProxyPathTransportSingBox {
					sourceServer, sourceOK := serverByID[previousServerID]
					targetServer, targetServerOK := serverByID[targetServerID]
					if !sourceOK || !targetServerOK {
						return nil, nil, fmt.Errorf("代理路径 %s 第 %d 跳无法确定源/目标服务器", path.Name, step.Position)
					}
					if _, err := ResolveReachableEntryAddress(sourceServer, plannedInbound, targetServer); err != nil {
						return nil, nil, fmt.Errorf("代理路径 %s 第 %d 跳: %w", path.Name, step.Position, err)
					}
				}
			}
			switch mode {
			case model.ProxyPathTransportPortForward:
				if !targetOK {
					if path.Enabled {
						return nil, nil, fmt.Errorf("代理路径 %s 第 %d 跳端口转发需要目标服务器", path.Name, step.Position)
					}
					plan.Warnings = append(plan.Warnings, fmt.Sprintf("第 %d 跳端口转发需要目标服务器", step.Position))
					continue
				}
				f, err := proxyPathManagedPortForward(path, step, root, previousServerID, targetServerID, sourceListenPort, plannedInbound, serverByID, transparentGroups[path.ID])
				if err != nil {
					if path.Enabled {
						return nil, nil, err
					}
					plan.Warnings = append(plan.Warnings, err.Error())
					continue
				}
				plan.PortForwards = append(plan.PortForwards, f)
				sourceListenPort = plannedInbound.Port
			case model.ProxyPathTransportTunnel:
				if !targetOK {
					if path.Enabled {
						return nil, nil, fmt.Errorf("代理路径 %s 第 %d 跳隧道需要目标服务器", path.Name, step.Position)
					}
					plan.Warnings = append(plan.Warnings, fmt.Sprintf("第 %d 跳隧道需要目标服务器", step.Position))
					continue
				}
				tunnelKey := proxyPathTunnelReuseKey(step, previousServerID, targetServerID, plannedInbound)
				t, exists := sharedTunnels[tunnelKey]
				if !exists {
					var err error
					t, err = proxyPathManagedTunnel(path, step, previousServerID, targetServerID, plannedInbound, serverByID, inboundByID, ledger)
					if err != nil {
						if path.Enabled {
							return nil, nil, err
						}
						plan.Warnings = append(plan.Warnings, err.Error())
						continue
					}
					sharedTunnels[tunnelKey] = t
					reserveProxyPathTunnelPorts(t, inboundByID)
				}
				plan.Tunnels = append(plan.Tunnels, t)
				plan.Steps[planStepIndex].TunnelID = t.ID
			}
			if targetOK {
				previousServerID = targetServerID
			}
		}
		_ = processingCount // processing_role is an internal marker for a transparent prefix.
		if path.Enabled && processingCount == 1 && len(plan.PortForwards) > 0 {
			for _, step := range pathSteps {
				if !step.ProcessingRole {
					continue
				}
				processingServerID, _, ok := proxyPathStepTargetServer(step, inboundByID)
				entryServer, entryOK := serverByID[root.ServerID]
				processingServer, processingOK := serverByID[processingServerID]
				if !ok || !entryOK || !processingOK {
					return nil, nil, fmt.Errorf("代理路径 %s 无法生成可信转发凭据", path.Name)
				}
				group := transparentGroups[path.ID]
				receiverID := proxyPathTrustedForwardReceiverID(path.ID, step.ID)
				key := proxyPathTrustedForwardKey(entryServer, processingServer, path.ID, step.ID)
				if group != nil {
					receiverID = proxyPathSharedTrustedForwardReceiverID(group.InboundID, group.PrefixLength)
					key = proxyPathSharedTrustedForwardKey(entryServer, processingServer, group.InboundID, group.PrefixLength)
				}
				plan.PortForwards[0].TrustedForward = &model.TrustedForwardSender{
					Version:             1,
					ReceiverID:          receiverID,
					Key:                 key,
					MaxClockSkewSeconds: 120,
				}
				break
			}
		}
		out = append(out, plan)
	}
	ledger.markProjectionComplete()
	return out, inboundByID, nil
}

type transparentProxyPathGroup struct {
	InboundID    int64
	PrefixLength int
	OwnerPathID  int64
	Paths        []model.ProxyPath
}

func buildTransparentProxyPathGroups(paths []model.ProxyPath, stepsByPath map[int64][]model.ProxyPathStep) map[int64]*transparentProxyPathGroup {
	byInbound := map[int64]*transparentProxyPathGroup{}
	byPath := map[int64]*transparentProxyPathGroup{}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		prefixLength := transparentProxyPathPrefixLength(orderedProxyPathSteps(stepsByPath[path.ID]))
		if prefixLength == 0 {
			continue
		}
		group := byInbound[path.InboundID]
		if group == nil {
			group = &transparentProxyPathGroup{InboundID: path.InboundID, PrefixLength: prefixLength, OwnerPathID: path.ID}
			byInbound[path.InboundID] = group
		}
		if path.ID < group.OwnerPathID {
			group.OwnerPathID = path.ID
		}
		group.Paths = append(group.Paths, path)
		byPath[path.ID] = group
	}
	for _, group := range byInbound {
		sort.SliceStable(group.Paths, func(i, j int) bool { return group.Paths[i].ID < group.Paths[j].ID })
	}
	return byPath
}

func transparentProxyPathPrefixLength(steps []model.ProxyPathStep) int {
	length := 0
	for _, step := range steps {
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if mode != model.ProxyPathTransportPortForward {
			break
		}
		length++
	}
	return length
}

func transparentProxyPathPrefixSignature(steps []model.ProxyPathStep) string {
	type transparentStep struct {
		ServerID   *int64 `json:"server_id,omitempty"`
		InboundID  *int64 `json:"inbound_id,omitempty"`
		ConfigJSON string `json:"config_json"`
	}
	prefix := make([]transparentStep, 0)
	for _, step := range steps {
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if mode != model.ProxyPathTransportPortForward {
			break
		}
		prefix = append(prefix, transparentStep{ServerID: step.ServerID, InboundID: step.InboundID, ConfigJSON: canonicalJSONObject(step.ConfigJSON)})
	}
	if len(prefix) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(prefix)
	return string(encoded)
}

func validateProxyPathTransportSet(paths []model.ProxyPath, stepsByPath map[int64][]model.ProxyPathStep, inboundByID map[int64]model.Inbound) error {
	enabledByInbound := map[int64][]model.ProxyPath{}
	transparentSignatureByInbound := map[int64]string{}
	transparentCountByInbound := map[int64]int{}
	directSignatures := map[string]bool{}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		root, ok := inboundByID[path.InboundID]
		if !ok {
			continue
		}
		enabledByInbound[path.InboundID] = append(enabledByInbound[path.InboundID], path)
		ordered := orderedProxyPathSteps(stepsByPath[path.ID])
		for _, step := range ordered {
			if step.InboundID == nil || *step.InboundID == 0 {
				continue
			}
			target, ok := inboundByID[*step.InboundID]
			if !ok || target.Protocol != model.ProtocolMieru {
				continue
			}
			ports, err := MieruInboundPorts(target)
			if err != nil {
				return fmt.Errorf("代理路径 %s 的 Mieru 节点端口无效：%w", path.Name, err)
			}
			mode := step.TransportMode
			if mode == "" {
				mode = model.ProxyPathTransportSingBox
			}
			if len(ports) > 1 && (mode == model.ProxyPathTransportPortForward || mode == model.ProxyPathTransportTunnel) {
				return fmt.Errorf("代理路径 %s 的多端口 Mieru 节点只能使用 sing-box 出站链", path.Name)
			}
		}
		for index, step := range ordered {
			if step.NodeType != model.ProxyPathStepWARP {
				continue
			}
			if path.Kind != "" && path.Kind != model.ProxyPathKindChain {
				return fmt.Errorf("WARP 只能作为普通代理链路的出口")
			}
			if index != len(ordered)-1 {
				return fmt.Errorf("代理路径 %s 的 WARP 必须是最后一个节点", path.Name)
			}
			if step.TransportMode != "" && step.TransportMode != model.ProxyPathTransportSingBox {
				return fmt.Errorf("代理路径 %s 的 WARP 只能使用 sing-box 出站", path.Name)
			}
			if index > 0 && ordered[index-1].NodeType != model.ProxyPathStepServerInbound {
				return fmt.Errorf("代理路径 %s 的 WARP 必须直接连接在可控服务器之后", path.Name)
			}
		}
		if path.Kind == model.ProxyPathKindDirect {
			if len(ordered) > 0 {
				last := ordered[len(ordered)-1]
				if last.NodeType != model.ProxyPathStepServerInbound {
					return fmt.Errorf("直接出口分支 %s 的最后一个节点必须是可控服务器", path.Name)
				}
				if _, _, ok := proxyPathStepTargetServer(last, inboundByID); !ok {
					return fmt.Errorf("直接出口分支 %s 的出口服务器不存在", path.Name)
				}
			}
			signature := directProxyPathSignature(path.InboundID, ordered)
			if directSignatures[signature] {
				return fmt.Errorf("入口 %d 已存在相同位置的直接出口分支", path.InboundID)
			}
			directSignatures[signature] = true
		}
		transparent, err := validateProxyPathTransportSemantics(path, root, ordered)
		if err != nil {
			return err
		}
		if transparent {
			signature := transparentProxyPathPrefixSignature(ordered)
			if previous := transparentSignatureByInbound[path.InboundID]; previous != "" && previous != signature {
				return fmt.Errorf("入口 %d 的启用分支必须复用完全相同的透明转发前缀，并在处理加解密节点或其后分叉", path.InboundID)
			}
			transparentSignatureByInbound[path.InboundID] = signature
			transparentCountByInbound[path.InboundID]++
		}
	}
	for inboundID, signature := range transparentSignatureByInbound {
		pathsForInbound := enabledByInbound[inboundID]
		if signature == "" || transparentCountByInbound[inboundID] != len(pathsForInbound) {
			return fmt.Errorf("入口 %d 使用透明转发时，所有启用分支都必须复用相同前缀，不能在处理加解密节点之前分叉", inboundID)
		}
		if len(pathsForInbound) > 1 {
			root, ok := inboundByID[inboundID]
			if !ok || !InboundSupportsMultipleUsers(root) {
				return fmt.Errorf("入口 %d 的协议不支持通过多个用户名复用透明转发前缀", inboundID)
			}
		}
	}
	return nil
}

// ProxyPathWARPServerIDs resolves every enabled WARP terminal to the controlled
// server immediately before it. The same derivation drives config generation,
// deployment requests, and UI status so ownership cannot drift between layers.
func ProxyPathWARPServerIDs(paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound) (map[int64]bool, error) {
	inboundByID := make(map[int64]model.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	out := map[int64]bool{}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		root, ok := inboundByID[path.InboundID]
		if !ok {
			return nil, fmt.Errorf("代理路径 %s 的入口不存在", path.Name)
		}
		currentServerID := root.ServerID
		ordered := orderedProxyPathSteps(stepsByPath[path.ID])
		for index, step := range ordered {
			switch step.NodeType {
			case model.ProxyPathStepServerInbound:
				serverID, _, ok := proxyPathStepTargetServer(step, inboundByID)
				if ok {
					currentServerID = serverID
				}
			case model.ProxyPathStepWARP:
				if index != len(ordered)-1 || (index > 0 && ordered[index-1].NodeType != model.ProxyPathStepServerInbound) {
					return nil, fmt.Errorf("代理路径 %s 的 WARP 必须直接连接在最后一台可控服务器之后", path.Name)
				}
				if currentServerID == 0 {
					return nil, fmt.Errorf("代理路径 %s 无法确定 WARP 出口服务器", path.Name)
				}
				out[currentServerID] = true
			}
		}
	}
	return out, nil
}

func directProxyPathSignature(inboundID int64, steps []model.ProxyPathStep) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d", inboundID)
	for _, step := range steps {
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		fmt.Fprintf(&b, "|%s:%s:", step.NodeType, mode)
		if step.ServerID != nil {
			fmt.Fprintf(&b, "s%d", *step.ServerID)
		}
		if step.InboundID != nil {
			fmt.Fprintf(&b, "i%d", *step.InboundID)
		}
		if step.ExternalOutboundID != nil {
			fmt.Fprintf(&b, "e%d", *step.ExternalOutboundID)
		}
		b.WriteString(":")
		b.WriteString(strings.TrimSpace(step.ConfigJSON))
	}
	return b.String()
}

func orderedProxyPathSteps(steps []model.ProxyPathStep) []model.ProxyPathStep {
	out := append([]model.ProxyPathStep(nil), steps...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Position == out[j].Position {
			return out[i].ID < out[j].ID
		}
		return out[i].Position < out[j].Position
	})
	return out
}

// ProxyPathAccountingServerID returns the server where the user protocol is
// first authenticated and decrypted. Downstream shared chain services and
// tunnels deliberately carry no billable end-user identity.
func ProxyPathAccountingServerID(path model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound) (int64, bool) {
	rootServerID := int64(0)
	for _, inbound := range inbounds {
		if inbound.ID == path.InboundID && inbound.Enabled {
			rootServerID = inbound.ServerID
			break
		}
	}
	if !path.Enabled || rootServerID == 0 {
		return 0, false
	}
	ordered := orderedProxyPathSteps(steps)
	for _, step := range ordered {
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if mode != model.ProxyPathTransportPortForward {
			break
		}
		if !step.ProcessingRole {
			continue
		}
		if step.ServerID != nil && *step.ServerID != 0 {
			return *step.ServerID, true
		}
		if step.InboundID != nil && *step.InboundID != 0 {
			for _, inbound := range inbounds {
				if inbound.ID == *step.InboundID {
					return inbound.ServerID, true
				}
			}
		}
	}
	return rootServerID, true
}

func TrafficAccountingUsersForServer(serverID int64, paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound, bindings []model.InboundUser, pathBindingSets ...[]model.ProxyPathUser) map[int64]bool {
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	pathsByInbound := map[int64][]model.ProxyPath{}
	for _, path := range paths {
		if path.Enabled {
			pathsByInbound[path.InboundID] = append(pathsByInbound[path.InboundID], path)
		}
	}
	accountingByInbound := map[int64]bool{}
	for _, inbound := range inbounds {
		if !inbound.Enabled {
			continue
		}
		inboundPaths := pathsByInbound[inbound.ID]
		if len(inboundPaths) == 0 {
			accountingByInbound[inbound.ID] = inbound.ServerID == serverID
			continue
		}
		for _, path := range inboundPaths {
			accountingServerID, ok := ProxyPathAccountingServerID(path, stepsByPath[path.ID], inbounds)
			if ok && accountingServerID == serverID {
				accountingByInbound[inbound.ID] = true
				break
			}
		}
	}
	users := map[int64]bool{}
	if len(pathBindingSets) > 0 {
		accountingByPath := map[int64]bool{}
		for _, path := range paths {
			accountingServerID, ok := ProxyPathAccountingServerID(path, stepsByPath[path.ID], inbounds)
			accountingByPath[path.ID] = ok && accountingServerID == serverID
		}
		for _, binding := range pathBindingSets[0] {
			if binding.Enabled && accountingByPath[binding.ProxyPathID] {
				users[binding.UserID] = true
			}
		}
		for _, binding := range bindings {
			if binding.Enabled && accountingByInbound[binding.InboundID] && len(pathsByInbound[binding.InboundID]) == 0 {
				users[binding.UserID] = true
			}
		}
		return users
	}
	for _, binding := range bindings {
		if binding.Enabled && accountingByInbound[binding.InboundID] {
			users[binding.UserID] = true
		}
	}
	return users
}

func IsProxyPathAccountingLocation(serverID, inboundID, pathID int64, paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound) bool {
	var selected model.ProxyPath
	found := false
	for _, path := range paths {
		if path.ID == pathID && path.InboundID == inboundID && path.Enabled {
			selected = path
			found = true
			break
		}
	}
	if !found {
		return false
	}
	pathSteps := make([]model.ProxyPathStep, 0)
	for _, step := range steps {
		if step.PathID == pathID {
			pathSteps = append(pathSteps, step)
		}
	}
	accountingServerID, ok := ProxyPathAccountingServerID(selected, pathSteps, inbounds)
	return ok && accountingServerID == serverID
}

func TrustedForwardServerIDs(paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound) map[int64]bool {
	inboundByID := make(map[int64]model.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	required := map[int64]bool{}
	for _, path := range paths {
		root, ok := inboundByID[path.InboundID]
		if !path.Enabled || !ok || !root.Enabled {
			continue
		}
		ordered := orderedProxyPathSteps(stepsByPath[path.ID])
		usesTrustedForward := false
		pathServers := map[int64]bool{root.ServerID: true}
		for _, step := range ordered {
			mode := step.TransportMode
			if mode == "" {
				mode = model.ProxyPathTransportSingBox
			}
			if mode != model.ProxyPathTransportPortForward {
				break
			}
			if step.ServerID != nil && *step.ServerID > 0 {
				pathServers[*step.ServerID] = true
			} else if step.InboundID != nil {
				if inbound, exists := inboundByID[*step.InboundID]; exists {
					pathServers[inbound.ServerID] = true
				}
			}
			if step.ProcessingRole {
				usesTrustedForward = true
				break
			}
		}
		if usesTrustedForward {
			for id := range pathServers {
				required[id] = true
			}
		}
	}
	return required
}

// ProxyPathRequiresAccountingPathID reports whether traffic for an inbound can
// only be accounted on a downstream transparent-processing server. In that
// case an Agent report must identify the path; accepting a path-less report
// from the root server would allow the same user bytes to be counted twice.
func ProxyPathRequiresAccountingPathID(inboundID int64, paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound) bool {
	rootServerID := int64(0)
	for _, inbound := range inbounds {
		if inbound.ID == inboundID && inbound.Enabled {
			rootServerID = inbound.ServerID
			break
		}
	}
	if rootServerID == 0 {
		return false
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	for _, path := range paths {
		if !path.Enabled || path.InboundID != inboundID {
			continue
		}
		accountingServerID, ok := ProxyPathAccountingServerID(path, stepsByPath[path.ID], inbounds)
		if ok && accountingServerID != rootServerID {
			return true
		}
	}
	return false
}

// validateProxyPathTransportSemantics returns true when the user protocol must
// remain encrypted until a downstream processing server.
func validateProxyPathTransportSemantics(path model.ProxyPath, root model.Inbound, steps []model.ProxyPathStep) (bool, error) {
	if root.Protocol == model.ProtocolSSH {
		for _, step := range steps {
			mode := step.TransportMode
			if mode == "" {
				mode = model.ProxyPathTransportSingBox
			}
			if mode == model.ProxyPathTransportPortForward {
				return false, fmt.Errorf("代理路径 %s 的 SSH 入口已在 Agent 解密，不能使用透明端口转发", path.Name)
			}
		}
	}
	processingIndex := -1
	for index, step := range steps {
		if !step.ProcessingRole {
			continue
		}
		if processingIndex >= 0 {
			return false, fmt.Errorf("代理路径 %s 只能设置一个处理加解密节点", path.Name)
		}
		if step.NodeType != model.ProxyPathStepServerInbound {
			return false, fmt.Errorf("代理路径 %s 的处理加解密节点必须是可控服务器", path.Name)
		}
		if step.InboundID != nil && *step.InboundID != 0 {
			return false, fmt.Errorf("代理路径 %s 的处理加解密节点必须选择服务器，由系统创建隐藏处理入口，不能复用已有入口", path.Name)
		}
		processingIndex = index
	}
	if processingIndex < 0 {
		for _, step := range steps {
			mode := step.TransportMode
			if mode == "" {
				mode = model.ProxyPathTransportSingBox
			}
			if mode == model.ProxyPathTransportPortForward {
				return false, fmt.Errorf("代理路径 %s 使用%s前必须设置后端处理加解密服务器", path.Name, proxyPathTransportName(mode))
			}
		}
		return false, nil
	}
	if root.Protocol == model.ProtocolMieru {
		ports, err := MieruInboundPorts(root)
		if err != nil {
			return false, fmt.Errorf("代理路径 %s 的 Mieru 入口端口无效：%w", path.Name, err)
		}
		if len(ports) > 1 {
			return false, fmt.Errorf("代理路径 %s 的多端口 Mieru 入口不能使用可信透明转发", path.Name)
		}
	}
	for index, step := range steps {
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if index <= processingIndex {
			if mode == model.ProxyPathTransportTunnel {
				return false, fmt.Errorf("代理路径 %s 的隧道尚未接入透明用户入口数据面；请先使用端口转发", path.Name)
			}
			if mode != model.ProxyPathTransportPortForward {
				return false, fmt.Errorf("代理路径 %s 在处理节点之前必须使用端口转发透明传递，不能提前由 sing-box 解密", path.Name)
			}
			continue
		}
		if mode == model.ProxyPathTransportPortForward {
			return false, fmt.Errorf("代理路径 %s 在透明转发结束后不能再次使用端口转发", path.Name)
		}
	}
	if root.Port <= 0 {
		return false, fmt.Errorf("代理路径 %s 的入口端口无效", path.Name)
	}
	return true, nil
}

func proxyPathTransportName(mode model.ProxyPathStepTransportMode) string {
	switch mode {
	case model.ProxyPathTransportPortForward:
		return "端口转发"
	case model.ProxyPathTransportTunnel:
		return "隧道"
	default:
		return "sing-box 出站链"
	}
}

func inboundUsesTransparentProcessing(inboundID int64, paths []model.ProxyPath, steps []model.ProxyPathStep) bool {
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	for _, path := range paths {
		if !path.Enabled || path.InboundID != inboundID {
			continue
		}
		for _, step := range stepsByPath[path.ID] {
			if step.ProcessingRole {
				return true
			}
		}
	}
	return false
}

func transparentForwardProtocol(inbound model.Inbound) model.ForwardProtocol {
	switch inbound.Protocol {
	case model.ProtocolHY2:
		return model.ForwardProtocolUDP
	case model.ProtocolVLESS, model.ProtocolAnyTLS:
		return model.ForwardProtocolTCP
	case model.ProtocolSS:
		network := strings.ToLower(strings.TrimSpace(stringValue(parseStepConfig(inbound.ConfigJSON), "network", "")))
		switch network {
		case "tcp":
			return model.ForwardProtocolTCP
		case "udp":
			return model.ForwardProtocolUDP
		default:
			return model.ForwardProtocolTCPUDP
		}
	case model.ProtocolMieru:
		if MieruInboundTransport(inbound) == "UDP" {
			return model.ForwardProtocolUDP
		}
		return model.ForwardProtocolTCP
	default:
		return model.ForwardProtocolTCP
	}
}

func proxyPathPlanTargetInbound(path model.ProxyPath, step model.ProxyPathStep, targetServerID int64, targetInbound *model.Inbound, servers map[int64]model.Server, inbounds map[int64]model.Inbound, services map[proxyPathChainServiceKey]*proxyPathChainService, transparentGroup *transparentProxyPathGroup, ledger *ProxyPathPortLedger) model.Inbound {
	if targetInbound != nil {
		return *targetInbound
	}
	if service, ok := proxyPathChainServiceForStep(services, step, targetServerID); ok {
		return service.Inbound
	}
	server := servers[targetServerID]
	if transparentGroup != nil && step.Position <= transparentGroup.PrefixLength {
		if planned, ok := inbounds[proxyPathSharedTransparentInboundID(path.InboundID, step.Position)]; ok {
			return planned
		}
		return proxyPathSharedTransparentInbound(path.InboundID, step, server, inbounds, ledger)
	}
	return proxyPathInternalInbound(path, step, server, inbounds, ledger)
}

func DerivedPortForwardsFromProxyPaths(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound) ([]model.PortForward, error) {
	return DerivedPortForwardsFromProxyPathsWithLedger(paths, steps, servers, inbounds, nil)
}

func DerivedPortForwardsFromProxyPathsWithLedger(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, ledger *ProxyPathPortLedger) ([]model.PortForward, error) {
	plans, err := BuildProxyPathPlansWithLedger(paths, steps, servers, inbounds, ledger)
	if err != nil {
		return nil, err
	}
	out := []model.PortForward{}
	seen := map[int64]model.PortForward{}
	for _, plan := range plans {
		if !plan.Enabled {
			continue
		}
		for _, forward := range plan.PortForwards {
			if previous, ok := seen[forward.ID]; ok {
				if previous.SourceServerID != forward.SourceServerID || previous.TargetServerID != forward.TargetServerID || previous.ListenIP != forward.ListenIP || previous.ListenPort != forward.ListenPort || previous.TargetAddress != forward.TargetAddress || previous.TargetPort != forward.TargetPort || previous.Protocol != forward.Protocol || previous.Backend != forward.Backend || !sameTrustedForwardSender(previous.TrustedForward, forward.TrustedForward) {
					return nil, fmt.Errorf("共享透明转发资源 %d 的投影不一致", forward.ID)
				}
				continue
			}
			seen[forward.ID] = forward
			out = append(out, forward)
		}
	}
	return out, nil
}

func sameTrustedForwardSender(left, right *model.TrustedForwardSender) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func DerivedTunnelsFromProxyPaths(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound) ([]model.Tunnel, error) {
	return DerivedTunnelsFromProxyPathsWithLedger(paths, steps, servers, inbounds, nil)
}

func DerivedTunnelsFromProxyPathsWithLedger(paths []model.ProxyPath, steps []model.ProxyPathStep, servers []model.Server, inbounds []model.Inbound, ledger *ProxyPathPortLedger) ([]model.Tunnel, error) {
	plans, err := BuildProxyPathPlansWithLedger(paths, steps, servers, inbounds, ledger)
	if err != nil {
		return nil, err
	}
	out := []model.Tunnel{}
	seen := map[int64]bool{}
	for _, plan := range plans {
		if !plan.Enabled {
			continue
		}
		for _, tunnel := range plan.Tunnels {
			if seen[tunnel.ID] {
				continue
			}
			seen[tunnel.ID] = true
			out = append(out, tunnel)
		}
	}
	return out, nil
}

func proxyPathStepTargetServer(step model.ProxyPathStep, inboundByID map[int64]model.Inbound) (int64, *model.Inbound, bool) {
	if step.InboundID != nil && *step.InboundID != 0 {
		inbound, ok := inboundByID[*step.InboundID]
		if !ok {
			return 0, nil, false
		}
		return inbound.ServerID, &inbound, true
	}
	if step.ServerID != nil && *step.ServerID != 0 {
		return *step.ServerID, nil, true
	}
	return 0, nil, false
}

func proxyPathManagedPortForward(path model.ProxyPath, step model.ProxyPathStep, root model.Inbound, sourceServerID, targetServerID int64, listenPort int, targetInbound model.Inbound, servers map[int64]model.Server, transparentGroup *transparentProxyPathGroup) (model.PortForward, error) {
	if sourceServerID == 0 || targetServerID == 0 || sourceServerID == targetServerID {
		return model.PortForward{}, fmt.Errorf("路径 %s 第 %d 跳端口转发的源/目标服务器无效", path.Name, step.Position)
	}
	target, ok := servers[targetServerID]
	if !ok {
		return model.PortForward{}, fmt.Errorf("路径 %s 第 %d 跳目标服务器不存在", path.Name, step.Position)
	}
	if listenPort <= 0 {
		return model.PortForward{}, fmt.Errorf("路径 %s 第 %d 跳端口转发监听端口无效", path.Name, step.Position)
	}
	source, ok := servers[sourceServerID]
	if !ok {
		return model.PortForward{}, fmt.Errorf("路径 %s 第 %d 跳源服务器不存在", path.Name, step.Position)
	}
	backend := model.ForwardBackendAuto
	if v := stringValue(parseStepConfig(step.ConfigJSON), "backend", ""); v != "" {
		backend = model.ForwardBackend(v)
	}
	protocol := transparentForwardProtocol(root)
	targetAddress, err := proxyPathReachableServerAddress(source, target)
	if err != nil {
		return model.PortForward{}, fmt.Errorf("路径 %s 第 %d 跳: %w", path.Name, step.Position, err)
	}
	id := syntheticProxyPathID(path.ID, step.ID, 10)
	name := fmt.Sprintf("%s / 第%d跳", firstNonEmpty(path.Name, "代理路径"), step.Position)
	configJSON := managedConfigJSON(path.ID, step.ID)
	if transparentGroup != nil {
		id = stableProxyPathResourceID("proxy-path-transparent-forward", transparentGroup.InboundID, step.Position)
		name = fmt.Sprintf("%s / 透明第%d跳", firstNonEmpty(root.Name, fmt.Sprintf("入口 %d", root.ID)), step.Position)
		configJSON = managedTransparentConfigJSON(transparentGroup.InboundID, step.Position)
	}
	return model.PortForward{ID: id, Name: name, SourceServerID: sourceServerID, TargetServerID: targetServerID, ListenIP: EffectiveListenIP(source, firstNonEmpty(stringValue(parseStepConfig(step.ConfigJSON), "listen_ip", ""), source.ListenIP)), ListenPort: listenPort, TargetAddress: targetAddress, TargetPort: targetInbound.Port, Protocol: protocol, Backend: backend, ProbeMode: "apply", ProbeIntervalSeconds: 300, Priority: 1000 + step.Position, ConfigJSON: configJSON, Enabled: true}, nil
}

func proxyPathReachableServerAddress(source, target model.Server) (string, error) {
	return ResolveReachableServerEntryAddress(source, target)
}

func proxyPathManagedTunnel(path model.ProxyPath, step model.ProxyPathStep, sourceServerID, targetServerID int64, targetInbound model.Inbound, servers map[int64]model.Server, inbounds map[int64]model.Inbound, ledger *ProxyPathPortLedger) (model.Tunnel, error) {
	if sourceServerID == 0 || targetServerID == 0 || sourceServerID == targetServerID {
		return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳隧道的源/目标服务器无效", path.Name, step.Position)
	}
	source, sourceOK := servers[sourceServerID]
	target, ok := servers[targetServerID]
	if !sourceOK {
		return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳源服务器不存在", path.Name, step.Position)
	}
	if !ok {
		return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳目标服务器不存在", path.Name, step.Position)
	}
	targetEndpoint, err := ResolveReachableServerEntryAddress(source, target)
	if err != nil {
		return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳: %w", path.Name, step.Position, err)
	}
	cfg := parseStepConfig(step.ConfigJSON)
	typeName := model.TunnelType(strings.ToLower(stringValue(cfg, "type", string(model.TunnelTypeSSH))))
	reuseKey := proxyPathTunnelReuseKey(step, sourceServerID, targetServerID, targetInbound)
	tunnel := model.Tunnel{ID: stableProxyPathResourceID("proxy-path-tunnel", reuseKey), Name: fmt.Sprintf("共享隧道 / %s -> %s", source.Name, target.Name), SourceServerID: sourceServerID, TargetServerID: targetServerID, Type: typeName, Priority: 1000, Enabled: true}
	switch typeName {
	case model.TunnelTypeSSH:
		clientPrivateKey, clientPublicKey, err := proxyPathSSHKeyPair(source, target, reuseKey)
		if err != nil {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳生成共享 SSH 凭据: %w", path.Name, step.Position, err)
		}
		listenPort := ledger.resolve(model.ProxyPathPortKindTunnelSSH, fmt.Sprint(tunnel.ID), source.ID, func() int {
			return proxyPathTunnelPort(source, tunnel.ID, 0, 31, inbounds)
		})
		sshPort := intValueFromMap(cfg, "ssh_port", 0)
		if sshPort == 0 {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳未设置目标端隧道服务端口", path.Name, step.Position)
		}
		if err := ValidatePort(sshPort); err != nil {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳目标端隧道服务端口: %w", path.Name, step.Position, err)
		}
		if !proxyPathPortAvailable(target.ID, sshPort, model.ForwardProtocolTCP, inbounds) {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳目标端隧道服务端口 %d 已被目标服务器的 TCP 服务占用", path.Name, step.Position, sshPort)
		}
		sshConfig := map[string]any{
			"managed_pair":       true,
			"client_private_key": clientPrivateKey,
			"client_public_key":  clientPublicKey,
			"user":               "oboard_tunnel",
			"local_forward":      fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", listenPort, targetInbound.Port),
			"permit_open":        fmt.Sprintf("127.0.0.1:%d", targetInbound.Port),
			"managed_by":         "proxy_path_shared",
		}
		b, _ := json.Marshal(sshConfig)
		tunnel.ListenPort = listenPort
		tunnel.TargetEndpoint = targetEndpoint
		tunnel.TargetPort = sshPort
		tunnel.ConfigJSON = string(b)
	case model.TunnelTypeWireGuard:
		sourcePrivateKey, sourcePublicKey, err := proxyPathWireGuardKeyPair(source, target, reuseKey, "wireguard-source")
		if err != nil {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳生成共享 WireGuard 源凭据: %w", path.Name, step.Position, err)
		}
		targetPrivateKey, targetPublicKey, err := proxyPathWireGuardKeyPair(source, target, reuseKey, "wireguard-target")
		if err != nil {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳生成共享 WireGuard 目标凭据: %w", path.Name, step.Position, err)
		}
		sourceAddress, targetAddress := proxyPathWireGuardAddresses(tunnel.ID, 0)
		listenPort := ledger.resolve(model.ProxyPathPortKindTunnelWG, fmt.Sprint(tunnel.ID), target.ID, func() int {
			return proxyPathTunnelPort(target, tunnel.ID, 0, 47, inbounds)
		})
		if listenPort == 0 {
			return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳的目标服务器端口范围内没有可用 WireGuard UDP 端口", path.Name, step.Position)
		}
		pair := map[string]any{
			"managed_pair":         true,
			"source_address":       sourceAddress,
			"target_address":       targetAddress,
			"source_private_key":   sourcePrivateKey,
			"source_public_key":    sourcePublicKey,
			"target_private_key":   targetPrivateKey,
			"target_public_key":    targetPublicKey,
			"persistent_keepalive": intValueFromMap(cfg, "persistent_keepalive", 25),
			"managed_by":           "proxy_path_shared",
		}
		b, _ := json.Marshal(pair)
		tunnel.LocalAddress = sourceAddress
		tunnel.PeerAddress = prefixHost(targetAddress) + "/32"
		tunnel.TargetEndpoint = targetEndpoint
		tunnel.TargetPort = listenPort
		tunnel.ConfigJSON = string(b)
	default:
		return model.Tunnel{}, fmt.Errorf("路径 %s 第 %d 跳隧道类型必须是 ssh 或 wireguard", path.Name, step.Position)
	}
	return tunnel, nil
}

func proxyPathTunnelReuseKey(step model.ProxyPathStep, sourceServerID, targetServerID int64, targetInbound model.Inbound) string {
	cfg := parseStepConfig(step.ConfigJSON)
	typeName := strings.ToLower(stringValue(cfg, "type", string(model.TunnelTypeSSH)))
	switch model.TunnelType(typeName) {
	case model.TunnelTypeSSH:
		sshPort := intValueFromMap(cfg, "ssh_port", 0)
		return fmt.Sprintf("ssh:%d:%d:%d:%d", sourceServerID, targetServerID, sshPort, targetInbound.Port)
	case model.TunnelTypeWireGuard:
		// persistent_keepalive is a tuning value, not part of the peer identity.
		// Including it would rotate the whole key pair and the interface addresses
		// when an operator only adjusts the keepalive interval.
		return fmt.Sprintf("wireguard:%d:%d", sourceServerID, targetServerID)
	default:
		return fmt.Sprintf("%s:%d:%d", typeName, sourceServerID, targetServerID)
	}
}

func proxyPathTunnelDialTarget(tunnel model.Tunnel) (string, int, error) {
	switch tunnel.Type {
	case model.TunnelTypeSSH:
		return "127.0.0.1", tunnel.ListenPort, nil
	case model.TunnelTypeWireGuard:
		return prefixHost(tunnel.PeerAddress), 0, nil
	default:
		return "", 0, errors.New("隧道类型必须是 ssh 或 wireguard")
	}
}

func proxyPathTunnelPort(server model.Server, pathID int64, position, salt int, inbounds map[int64]model.Inbound) int {
	// SSH local forwarding listens only on loopback. Keeping it in a separate
	// internal pool prevents a one-port server range from colliding with the
	// public inbound that already owns that port.
	if salt == 31 {
		return proxyPathAvailablePort(server, pathID*193, position*71+salt, 20000, 29999, inbounds)
	}
	start, end := proxyPathServerPortRange(server)
	protocol := model.ForwardProtocolTCP
	if salt == 47 {
		protocol = model.ForwardProtocolUDP
	}
	return proxyPathAvailablePortForProtocol(server, pathID*193, position*71+salt, start, end, protocol, false, inbounds)
}

func proxyPathAvailablePort(server model.Server, pathSeed int64, positionSeed, start, end int, inbounds map[int64]model.Inbound) int {
	return proxyPathAvailablePortForProtocol(server, pathSeed, positionSeed, start, end, "", true, inbounds)
}

func proxyPathAvailablePortForProtocol(server model.Server, pathSeed int64, positionSeed, start, end int, protocol model.ForwardProtocol, fallback bool, inbounds map[int64]model.Inbound) int {
	occupied := map[int]bool{}
	for _, inbound := range inbounds {
		// Disabled inbounds are reserved too. Allocation is deterministic, so
		// handing a disabled inbound's port to a generated listener would create a
		// listener conflict the operator cannot resolve by re-enabling it.
		if inbound.ServerID == server.ID && inbound.Port > 0 && (protocol == "" || proxyPathInboundUsesProtocol(inbound, protocol)) {
			occupied[inbound.Port] = true
		}
	}
	choose := func(rangeStart, rangeEnd int) int {
		span := rangeEnd - rangeStart + 1
		if span <= 0 {
			return 0
		}
		seed := int((pathSeed + int64(positionSeed)) % int64(span))
		if seed < 0 {
			seed = -seed
		}
		for offset := 0; offset < span; offset++ {
			candidate := rangeStart + (seed+offset)%span
			if !occupied[candidate] {
				return candidate
			}
		}
		return 0
	}
	if port := choose(start, end); port != 0 {
		return port
	}
	if !fallback {
		return 0
	}
	// Hidden proxy-path listeners are implementation details rather than user
	// entry ports. When the configured range is exhausted, use the dedicated
	// internal pool instead of reusing an occupied public port.
	return choose(30000, 60000)
}

func proxyPathPortAvailable(serverID int64, port int, protocol model.ForwardProtocol, inbounds map[int64]model.Inbound) bool {
	for _, inbound := range inbounds {
		if inbound.ServerID != serverID || inbound.Port != port {
			continue
		}
		if proxyPathInboundUsesProtocol(inbound, protocol) {
			return false
		}
	}
	return true
}

func proxyPathInboundUsesProtocol(inbound model.Inbound, protocol model.ForwardProtocol) bool {
	inboundProtocol := transparentForwardProtocol(inbound)
	return inboundProtocol == model.ForwardProtocolTCPUDP || inboundProtocol == protocol
}

func proxyPathServerPortRange(server model.Server) (int, int) {
	if server.PortRangeStart > 0 && server.PortRangeEnd >= server.PortRangeStart {
		return server.PortRangeStart, server.PortRangeEnd
	}
	return 30000, 60000
}

func reserveProxyPathTunnelPorts(tunnel model.Tunnel, inbounds map[int64]model.Inbound) {
	if tunnel.TargetServerID == 0 || tunnel.TargetPort <= 0 {
		return
	}
	if tunnel.Type == model.TunnelTypeWireGuard {
		id := -tunnel.ID
		inbounds[id] = model.Inbound{ID: id, ServerID: tunnel.TargetServerID, Protocol: model.ProtocolHY2, Port: tunnel.TargetPort, Enabled: true}
	}
	if tunnel.Type == model.TunnelTypeSSH && tunnel.SourceServerID != 0 && tunnel.ListenPort > 0 {
		localID := -tunnel.ID - 1
		inbounds[localID] = model.Inbound{ID: localID, ServerID: tunnel.SourceServerID, Protocol: model.ProtocolVLESS, ListenIP: "127.0.0.1", Port: tunnel.ListenPort, Enabled: true}
	}
}

func proxyPathWireGuardAddresses(pathID int64, position int) (string, string) {
	network := (pathID*4099 + int64(position)*131) & 0x3ffff
	second := 16 + (network >> 16)
	third := (network >> 8) & 0xff
	fourth := (network & 0xff) & 0xfc
	return fmt.Sprintf("172.%d.%d.%d/30", second, third, fourth+1), fmt.Sprintf("172.%d.%d.%d/30", second, third, fourth+2)
}

func prefixHost(value string) string {
	if index := strings.IndexByte(value, '/'); index >= 0 {
		return value[:index]
	}
	return value
}

func parseStepConfig(raw string) map[string]any {
	var m map[string]any
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{}
	}
	return m
}

func intValueFromMap(m map[string]any, key string, fallback int) int {
	switch v := m[key].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

const (
	// syntheticProxyPathIDBase keeps derived IDs far away from autoincrement rows
	// and from the negative ranges used by generated inbounds.
	syntheticProxyPathIDBase  = int64(1) << 52
	syntheticProxyPathIDKind  = int64(1) << 48
	syntheticProxyPathIDShift = 24
	syntheticProxyPathIDField = int64(1)<<syntheticProxyPathIDShift - 1
)

// syntheticProxyPathID derives a stable resource ID for a component owned by one
// path step. Path and step IDs occupy disjoint bit fields: step IDs come from a
// global autoincrement, so a decimal layout would let a step ID above its field
// width carry into the neighbouring path's range and silently share one derived
// resource between two paths. The result stays below 2^53 so it survives the
// JSON round-trip through the Web UI without losing precision.
func syntheticProxyPathID(pathID, stepID int64, kind int64) int64 {
	return syntheticProxyPathIDBase +
		kind*syntheticProxyPathIDKind +
		(pathID&syntheticProxyPathIDField)<<syntheticProxyPathIDShift +
		(stepID & syntheticProxyPathIDField)
}

func managedConfigJSON(pathID, stepID int64) string {
	b, _ := json.Marshal(map[string]any{"managed_by": "proxy_path", "path_id": pathID, "step_id": stepID})
	return string(b)
}

func managedTransparentConfigJSON(inboundID int64, position int) string {
	b, _ := json.Marshal(map[string]any{"managed_by": "proxy_path_transparent_prefix", "inbound_id": inboundID, "position": position})
	return string(b)
}
